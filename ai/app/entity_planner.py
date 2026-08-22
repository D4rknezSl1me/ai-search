"""LLM query planner for entity discovery (docs/15-DISCOVERY-AGENT.md §4.1, §6).

Layers the local LLM as the *reasoner* over the deterministic query generation:
given the brief, the model proposes additional, cleverer search queries (e.g.
locale-aware phrasings, likely-username guesses, venue/roster angles a fixed
template won't invent). Its output is **unioned with** — never replaces — the
deterministic backbone, and every model query must pass the §5 guardrail (carry a
real discriminator), so the reasoner can only *add* recall, never fan a bare name
out or break the loop when it's down.

Degrades hard to the deterministic queries when the LLM is absent, errors, or
returns nothing (CLAUDE.md north star: query planning can never fail a lookup).

Pure except for the injected LLM (same `LLMFn` shape as `understand.py`) — the
parsing/sanitizing/merging is unit-testable offline with a fake model.
"""

from __future__ import annotations

import json
import logging
from typing import Awaitable, Callable

from app.entity_brief import TargetBrief
from app.entity_queries import GeneratedQuery, generate_queries
from app.entity_resolve import _norm

log = logging.getLogger("entity_planner")

# Same abstraction as understand.py: messages + temperature → raw assistant text.
LLMFn = Callable[[list[dict[str, str]], float], Awaitable[str]]

_MAX_QUERY_CHARS = 200

PLANNER_SYSTEM = (
    "You are an OSINT search-query planner. Given a structured brief about ONE "
    "person we are trying to locate, propose the most effective web/social search "
    "queries to find them. Rules: (1) EVERY query must include at least one "
    "specific discriminating detail from the brief — a school, city, employer, or "
    "the name of a related person — never a bare common name alone. (2) Quote "
    "multi-word names and schools for exact-phrase matching. (3) You may scope a "
    "query to a platform with a site: filter. Return ONLY a compact JSON array of "
    "objects like {\"q\": \"...\", \"platform\": \"open_web\"} and nothing else — "
    "no prose, no keys other than q and platform."
)


def _brief_for_prompt(brief: TargetBrief) -> dict:
    """A compact, model-friendly view of the brief (only what aids query design)."""
    return {
        "goal": brief.goal.value,
        "surname": brief.surname,
        "given_name": brief.given_name or None,
        "known_attributes": brief.known_attributes,
        "relationships": [{"type": r.type, "of": r.of} for r in brief.relationships],
        "platforms": list(brief.platforms),
    }


def _build_messages(brief: TargetBrief, max_queries: int) -> list[dict[str, str]]:
    user = (
        f"Brief:\n{json.dumps(_brief_for_prompt(brief), ensure_ascii=False)}\n\n"
        f"Propose up to {max_queries} search queries as a JSON array."
    )
    return [
        {"role": "system", "content": PLANNER_SYSTEM},
        {"role": "user", "content": user},
    ]


def _discriminator_tokens(brief: TargetBrief) -> list[str]:
    """Normalized tokens that count as a real discriminator for the guardrail."""
    toks = [_norm(v) for v in brief.discriminators().values()]
    toks += [_norm(r.of) for r in brief.relationships]
    return [t for t in toks if t]


def _has_site_scope(text: str) -> bool:
    # Checked on the raw text: _norm strips the ':' so "site:" vanishes there.
    return "site:" in text.casefold()


def _has_discriminator(text: str, query_norm: str, discriminators: list[str]) -> bool:
    if _has_site_scope(text):            # a platform scope is itself a narrowing
        return True
    return any(d in query_norm for d in discriminators)


def parse_planner_queries(text: str) -> list[tuple[str, str]]:
    """Parse the model reply into (query, platform) pairs. Tolerant: accept a bare
    JSON array, an array of strings, or objects; never throw on model output."""
    if not text:
        return []
    start, end = text.find("["), text.rfind("]")
    if not (0 <= start < end):
        return []
    try:
        data = json.loads(text[start : end + 1])
    except (json.JSONDecodeError, ValueError):
        return []
    if not isinstance(data, list):
        return []
    out: list[tuple[str, str]] = []
    for item in data:
        if isinstance(item, str):
            out.append((item, "open_web"))
        elif isinstance(item, dict):
            q = item.get("q") or item.get("query") or item.get("text")
            if isinstance(q, str) and q.strip():
                platform = item.get("platform")
                out.append((q, platform if isinstance(platform, str) and platform else "open_web"))
    return out


def sanitize(pairs: list[tuple[str, str]], brief: TargetBrief) -> list[GeneratedQuery]:
    """Clean, guardrail-filter, and dedupe model queries into GeneratedQuery.

    Drops queries with no discriminator (§5), over-long queries, and duplicates.
    Specificity = discriminators present (+1 for a site: scope) so model queries
    rank alongside deterministic ones.
    """
    discriminators = _discriminator_tokens(brief)
    have_discriminators = bool(discriminators)
    seen: set[str] = set()
    out: list[GeneratedQuery] = []
    for raw_q, platform in pairs:
        text = " ".join((raw_q or "").split())
        if not text or len(text) > _MAX_QUERY_CHARS:
            continue
        qn = _norm(text)
        # Guardrail: when the brief has discriminators, every query must carry one.
        if have_discriminators and not _has_discriminator(text, qn, discriminators):
            continue
        if qn in seen:
            continue
        seen.add(qn)
        specificity = sum(1 for d in discriminators if d in qn) + (1 if _has_site_scope(text) else 0)
        out.append(GeneratedQuery(text=text, platform=platform, specificity=specificity))
    return out


def _merge(llm_qs: list[GeneratedQuery], det_qs: list[GeneratedQuery], max_queries: int) -> list[GeneratedQuery]:
    """Union LLM + deterministic queries, dedupe, rank most-specific-first, cap.

    Deterministic queries are appended first into the seen-set-free merge so the
    guaranteed backbone is always represented; final ordering is by specificity.
    """
    seen: set[str] = set()
    merged: list[GeneratedQuery] = []
    for q in [*det_qs, *llm_qs]:
        key = q.text.casefold()
        if q.text and key not in seen:
            seen.add(key)
            merged.append(q)
    merged.sort(key=lambda q: (-q.specificity, len(q.text)))
    return merged[:max_queries]


def make_llm_planner(llm: LLMFn | None, *, max_queries: int = 12, temperature: float = 0.4):
    """Build a PlanFn that unions LLM-proposed queries with the deterministic ones.

    Returns an async `plan(brief) -> [GeneratedQuery]`. With no LLM (or on any
    error / empty reply) it yields exactly the deterministic queries.
    """
    async def plan(brief: TargetBrief) -> list[GeneratedQuery]:
        det = generate_queries(brief, max_queries=max_queries)
        if llm is None:
            return det
        try:
            reply = await llm(_build_messages(brief, max_queries), temperature)
        except Exception:
            log.exception("planner LLM call failed; using deterministic queries only")
            return det
        llm_qs = sanitize(parse_planner_queries(reply), brief)
        if llm_qs:
            log.info("LLM planner added %d candidate quer(y|ies)", len(llm_qs))
        return _merge(llm_qs, det, max_queries)

    return plan
