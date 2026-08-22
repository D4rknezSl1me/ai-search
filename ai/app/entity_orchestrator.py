"""Discovery orchestrator loop (docs/15-DISCOVERY-AGENT.md §2, §4).

Ties the three Phase-6 pieces into the agent's plan → act → observe → refine
loop for a targeted lookup:

  PLAN    generate_queries(brief)            (entity_queries)
  ACT     search(query) -> [Candidate]       (injected: the crawler/discovery
                                              tools + candidate extraction)
  OBSERVE score_candidate(brief, candidate)  (entity_resolve)
  REFINE  brief.with_attribute/with_handle   (entity_brief) → new queries

This module is the **pure control flow**: the ACT step (which really calls the
Go crawler's `POST /internal/discover`, fetches, and extracts candidates via
NER + an LLM read) is dependency-injected as an async `search` callable — so the
loop's budget accounting, brief-enrichment feedback, dedupe/ranking, and stop
conditions are all unit-testable offline with a fake corpus (the same DI style
as `understand.py`). No network, no model here.

Recall-first, **bounded** (docs/15 §5): the loop always terminates on one of
{confident match, budget spent, no new leads} and returns whatever it found —
ranked candidates each carrying the per-signal evidence for the citation trail.
"""

from __future__ import annotations

import logging
import time
from dataclasses import dataclass, field
from typing import Awaitable, Callable

from app.entity_brief import TargetBrief
from app.entity_queries import GeneratedQuery, generate_queries
from app.entity_resolve import (
    DEFAULT_MATCH_THRESHOLD,
    Candidate,
    MatchScore,
    propose_enrichments,
    score_candidate,
)

log = logging.getLogger("entity_orchestrator")

# ACT: given one planned query, return the candidates discovered for it. The real
# adapter calls metasearch/social + fetch + extract; tests supply a fake.
SearchFn = Callable[[GeneratedQuery], Awaitable[list[Candidate]]]
# PLAN: given the current (enriched) brief, return the queries to run this hop.
# Default is the deterministic `generate_queries`; the LLM planner (entity_planner)
# is the same shape and unions its ideas with that backbone.
PlanFn = Callable[[TargetBrief], Awaitable[list[GeneratedQuery]]]
Clock = Callable[[], float]

# A candidate at/above this score ends the loop early — the target is resolved.
DEFAULT_RESOLVE_THRESHOLD = 0.85


@dataclass
class ScoredCandidate:
    candidate: Candidate
    match: MatchScore

    @property
    def score(self) -> float:
        return self.match.score


@dataclass
class DiscoveryResult:
    """Outcome of one discovery run (docs/15 §4.4)."""

    status: str                       # resolved | candidates | no_match
    brief: TargetBrief                # the final, enriched brief
    candidates: list[ScoredCandidate] = field(default_factory=list)  # ranked desc
    hops: int = 0
    fetches: int = 0
    elapsed_s: float = 0.0

    @property
    def best(self) -> ScoredCandidate | None:
        return self.candidates[0] if self.candidates else None


def _identity(c: Candidate) -> str:
    """Stable key to merge repeat observations of the same candidate."""
    from app.entity_resolve import _norm  # local import: shared normalizer
    handle = _norm(c.handle).replace(" ", "")
    if handle:
        return f"h:{handle}"
    return f"n:{_norm(c.name)}|{_norm(c.source_url)}"


@dataclass
class RunConfig:
    match_threshold: float = DEFAULT_MATCH_THRESHOLD
    resolve_threshold: float = DEFAULT_RESOLVE_THRESHOLD
    max_queries_per_hop: int = 12


async def discover_entity(
    brief: TargetBrief,
    search: SearchFn,
    *,
    plan: PlanFn | None = None,
    config: RunConfig | None = None,
    now: Clock = time.monotonic,
) -> DiscoveryResult:
    """Run the bounded plan→act→observe→refine loop for one target brief.

    `plan` is the PLAN step; by default the deterministic `generate_queries`. Pass
    the LLM planner (entity_planner.make_llm_planner) to layer model-proposed
    queries on top — it degrades to the deterministic backbone on its own, so the
    loop never depends on the model being up (recall-first).
    """
    cfg = config or RunConfig()
    budget = brief.budget
    start = now()

    async def _default_plan(b: TargetBrief) -> list[GeneratedQuery]:
        return generate_queries(b, max_queries=cfg.max_queries_per_hop)

    planner = plan or _default_plan

    best_by_id: dict[str, ScoredCandidate] = {}
    seen_queries: set[str] = set()
    fetches = 0
    hops = 0
    status = "no_match"

    def timed_out() -> bool:
        return (now() - start) >= budget.max_wall_s

    while hops < budget.max_hops and fetches < budget.max_fetches and not timed_out():
        # PLAN — queries reflect the current (possibly enriched) brief.
        try:
            queries = await planner(brief)
        except Exception:
            log.exception("planner failed; falling back to deterministic queries")
            queries = generate_queries(brief, max_queries=cfg.max_queries_per_hop)
        fresh = [q for q in queries if q.text.casefold() not in seen_queries]
        if not fresh:
            break  # fixpoint: refinement produced no new leads → stop (§5)

        enriched = False
        resolved = False
        for q in fresh:
            if fetches >= budget.max_fetches or timed_out():
                break
            seen_queries.add(q.text.casefold())
            fetches += 1
            try:
                candidates = await search(q)
            except Exception:
                log.exception("search failed for query %r; skipping", q.text)
                continue

            for cand in candidates:
                ms = score_candidate(brief, cand)
                # OBSERVE — keep the strongest observation of each candidate.
                key = _identity(cand)
                prev = best_by_id.get(key)
                if prev is None or ms.score > prev.score:
                    best_by_id[key] = ScoredCandidate(candidate=cand, match=ms)

                if ms.is_match(cfg.match_threshold):
                    # REFINE — a matched candidate teaches the brief; only
                    # above-threshold enrichment (noise never pollutes it).
                    for k, v in propose_enrichments(brief, cand).items():
                        new_brief = brief.with_attribute(k, v)
                        enriched = enriched or (new_brief is not brief)
                        brief = new_brief
                    if cand.handle:
                        nb = brief.with_handle(cand.handle)
                        enriched = enriched or (nb is not brief)
                        brief = nb
                    if ms.score >= cfg.resolve_threshold:
                        resolved = True

            if resolved:
                break

        hops += 1
        if resolved:
            status = "resolved"
            break
        if not enriched:
            # No new signal this hop → next PLAN yields only seen queries; stop.
            break

    ranked = sorted(best_by_id.values(), key=lambda sc: sc.score, reverse=True)
    if status != "resolved":
        status = "candidates" if any(
            sc.match.is_match(cfg.match_threshold) for sc in ranked
        ) else "no_match"

    return DiscoveryResult(
        status=status,
        brief=brief,
        candidates=ranked,
        hops=hops,
        fetches=fetches,
        elapsed_s=round(now() - start, 3),
    )
