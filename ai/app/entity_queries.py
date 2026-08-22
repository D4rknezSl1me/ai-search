"""Attribute-anchored query generation for entity discovery (docs/15 §4.1).

Turn a `TargetBrief` into a ranked batch of search queries, most-specific first.
This is the deterministic backbone of the agent's PLAN step — and the graceful
fallback when the local LLM planner is down (docs/15 §6: "degrades to a fixed
query-generation heuristic ... recall-first"). The orchestrator runs these
through the existing `POST /internal/discover` (metasearch) and social-adapter
search; nothing here fetches anything.

Guardrail (docs/15 §5): every generated query carries **≥1 discriminating
attribute** (school / city / employer / relationship), so metasearch load stays
proportional to the *target*, not to how common the surname is. A brief with no
discriminators at all yields only the plain name query (we never fan a bare
common surname across the web).

Pure and dependency-free — unit-testable offline.
"""

from __future__ import annotations

import re
from dataclasses import dataclass

from app.entity_brief import Goal, TargetBrief

_WS = re.compile(r"\s+")
_MAX_QUERY_CHARS = 200

# Platform → the host a `site:` dork should target (open-web scoped search).
_PLATFORM_SITE: dict[str, str] = {
    "instagram": "instagram.com",
    "tiktok": "tiktok.com",
    "linkedin": "linkedin.com",
    "facebook": "facebook.com",
    "twitter": "twitter.com",
    "x": "x.com",
    "youtube": "youtube.com",
    "reddit": "reddit.com",
    "github": "github.com",
}

# Goal → extra intent terms appended to the name+attribute core, nudging results
# toward the kind of page that carries the answer. Kept short (recall-first).
_GOAL_TERMS: dict[Goal, tuple[str, ...]] = {
    Goal.SOCIAL_HANDLE: ("profile", "instagram OR tiktok OR linkedin"),
    Goal.REAL_NAME: (),
    Goal.CONTACT: ("email OR contact",),
    Goal.PHOTOS: ("photo OR photos",),
    Goal.ANY_INFO: (),
}


@dataclass(frozen=True)
class GeneratedQuery:
    """One planned query plus where it should run and why (for logging/eval)."""

    text: str
    platform: str        # "open_web" or a specific platform key
    specificity: int     # count of discriminating signals baked in (higher = tighter)

    def __post_init__(self) -> None:  # normalize whitespace once
        object.__setattr__(self, "text", _WS.sub(" ", self.text).strip())


def _quote(term: str) -> str:
    """Quote a multi-word term for exact-phrase matching; leave single words bare."""
    term = term.strip()
    return f'"{term}"' if " " in term else term


def _name_terms(brief: TargetBrief) -> list[str]:
    """The strongest name expression available, plus a surname-only fallback."""
    out: list[str] = []
    if brief.full_name:
        out.append(_quote(brief.full_name))
    if brief.surname and brief.surname != brief.full_name:
        out.append(_quote(brief.surname))
    return out


def _goal_terms(goal: Goal) -> list[str]:
    return list(_GOAL_TERMS.get(goal, ()))


def _dedupe(seq: list[str]) -> list[str]:
    seen: set[str] = set()
    out: list[str] = []
    for s in seq:
        k = s.casefold()
        if k and k not in seen:
            seen.add(k)
            out.append(s)
    return out


def generate_queries(brief: TargetBrief, *, max_queries: int = 12) -> list[GeneratedQuery]:
    """Build a ranked batch of attribute-anchored queries for one brief.

    Ordering: most-specific first (more discriminators + tighter platform scope
    rank higher), because the orchestrator spends its fetch budget top-down and
    the tightest queries are the ones that actually pin a needle (docs/15 §5).
    """
    if max_queries <= 0:
        return []

    names = _name_terms(brief)
    if not names:
        return []  # nothing to search on — the caller reports an empty plan

    # Phrase-quote every anchor so a multi-word value ("Liceo Volta") matches as
    # an exact phrase, not as loose tokens scattered across a page.
    discriminators = [_quote(v) for v in brief.discriminators().values()]
    rel_terms = [_quote(r.of) for r in brief.relationships]
    anchors = _dedupe(discriminators + rel_terms)  # the narrowing signals
    goal_terms = _goal_terms(brief.goal)

    candidates: list[GeneratedQuery] = []

    def add(parts: list[str], platform: str, specificity: int) -> None:
        text = " ".join(p for p in parts if p)
        if len(text) <= _MAX_QUERY_CHARS:
            candidates.append(GeneratedQuery(text=text, platform=platform, specificity=specificity))

    # 1) Name × each discriminator, optionally platform-scoped — the tightest,
    #    highest-signal queries (each carries a real discriminator, §5 guardrail).
    for name in names:
        for anchor in anchors:
            add([name, anchor, *goal_terms], "open_web", specificity=2)
            for platform in brief.platforms:
                site = _PLATFORM_SITE.get(platform)
                if site:
                    add([f"site:{site}", name, anchor], platform, specificity=3)

    # 2) Name × ALL discriminators together — the single most-specific open query.
    if len(anchors) > 1:
        for name in names:
            add([name, *anchors], "open_web", specificity=len(anchors) + 1)

    # 3) Bare name + goal terms — only kept when there are NO discriminators, so a
    #    common surname is never fanned out unanchored (guardrail). With
    #    discriminators present these low-signal queries are dropped.
    if not anchors:
        for name in names:
            add([name, *goal_terms], "open_web", specificity=0)

    # Rank: specificity desc, then shorter (tighter) text first; dedupe on text.
    candidates.sort(key=lambda q: (-q.specificity, len(q.text)))
    seen: set[str] = set()
    ranked: list[GeneratedQuery] = []
    for q in candidates:
        key = q.text.casefold()
        if q.text and key not in seen:
            seen.add(key)
            ranked.append(q)
        if len(ranked) >= max_queries:
            break
    return ranked
