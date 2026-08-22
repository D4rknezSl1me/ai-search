"""Offline unit tests for the discovery orchestrator loop (docs/15 §2, §4).

The ACT step (search) is dependency-injected, so the loop's budget accounting,
brief-enrichment feedback, dedupe/ranking, and stop conditions run against a
fake corpus with no network/model. Async tests use asyncio.run (no
pytest-asyncio dependency), matching test_understand.py.
"""

from __future__ import annotations

import asyncio

from app.entity_brief import Budget, Relationship, TargetBrief
from app.entity_orchestrator import (
    RunConfig,
    discover_entity,
)
from app.entity_resolve import Candidate


def _run(brief, search, **kw):
    return asyncio.run(discover_entity(brief, search, **kw))


def _search_from(rules):
    """Build a fake search: `rules` is a list of (predicate, candidates)."""
    async def search(q):
        for pred, cands in rules:
            if pred(q.text):
                return list(cands)
        return []
    return search


# ------------------------------------------------------------------ resolve ---

def test_resolves_a_confident_target():
    brief = TargetBrief(surname="Rossi", given_name="Giulia",
                        known_attributes={"school": "Liceo Volta", "city": "Como"})
    target = Candidate(name="Giulia Rossi", handle="@giuliarossi",
                       attributes={"school": "Liceo Volta", "city": "Como"},
                       source_url="https://ex.com/g")
    res = _run(brief, _search_from([(lambda t: True, [target])]))
    assert res.status == "resolved"
    assert res.best is not None and res.best.candidate.handle == "@giuliarossi"
    assert res.best.score >= 0.85


# ------------------------------------------------- multi-hop + enrichment ---

def test_multi_hop_enrichment_then_resolve():
    # Hop 1 finds a partial match (surname+city, missing school) that teaches the
    # brief a given name + employer; hop 2's enriched queries find the handle.
    brief = TargetBrief(
        surname="Rossi",
        known_attributes={"school": "Liceo Volta", "city": "Como"},
    )
    partial = Candidate(name="Giulia Rossi",
                        attributes={"city": "Como", "employer": "Acme"},
                        source_url="https://ex.com/1")
    full = Candidate(name="Giulia Rossi", handle="@giuliarossi",
                     attributes={"city": "Como", "school": "Liceo Volta",
                                 "employer": "Acme"},
                     source_url="https://ex.com/2")
    search = _search_from([
        (lambda t: ("Giulia" in t) or ("Acme" in t), [full]),  # hop 2 (enriched)
        (lambda t: "Rossi" in t, [partial]),                   # hop 1
    ])
    res = _run(brief, search)
    assert res.status == "resolved"
    assert res.hops >= 2
    assert res.brief.given_name == "Giulia"        # brief was enriched
    assert res.best.candidate.handle == "@giuliarossi"


def test_matched_but_not_resolved_reports_candidates():
    # One partial match (0.6 ≤ score < 0.85), no further leads → status candidates.
    brief = TargetBrief(surname="Rossi",
                        known_attributes={"school": "Liceo Volta", "city": "Como"})
    partial = Candidate(name="Rossi", attributes={"city": "Como"},
                        source_url="https://ex.com/1")
    res = _run(brief, _search_from([(lambda t: "Como" in t, [partial])]))
    assert res.status == "candidates"
    assert res.best.match.is_match()
    assert res.best.score < 0.85


def test_no_match_when_only_wrong_people():
    brief = TargetBrief(surname="Rossi", known_attributes={"school": "Volta"})
    wrong = Candidate(name="Marco Bianchi", attributes={"school": "Manzoni"})
    res = _run(brief, _search_from([(lambda t: True, [wrong])]))
    assert res.status == "no_match"
    assert res.best.score < 0.6


# ----------------------------------------------------------------- budgets ---

def test_max_fetches_caps_search_calls():
    brief = TargetBrief(surname="Rossi",
                        known_attributes={"school": "Volta", "city": "Como",
                                          "employer": "Acme"},
                        platforms=("instagram", "tiktok", "open_web"),
                        budget=Budget(max_fetches=2))
    calls = {"n": 0}
    async def counting(q):
        calls["n"] += 1
        return []
    res = _run(brief, counting)
    assert calls["n"] == 2
    assert res.fetches == 2


def test_wall_clock_timeout_stops_before_fetching():
    brief = TargetBrief(surname="Rossi", known_attributes={"city": "Como"},
                        budget=Budget(max_wall_s=5))
    ticks = iter([0.0, 100.0, 200.0])   # 2nd read (loop guard) already exceeds
    res = _run(brief, _search_from([(lambda t: True, [Candidate(name="Rossi")])]),
               now=lambda: next(ticks))
    assert res.fetches == 0
    assert res.status == "no_match"


def test_no_new_leads_stops_early():
    # A wrong candidate never enriches the brief → queries don't change → the
    # loop stops after one hop instead of spinning to max_hops.
    brief = TargetBrief(surname="Rossi", known_attributes={"city": "Como"},
                        budget=Budget(max_hops=9))
    wrong = Candidate(name="Nobody", attributes={})
    res = _run(brief, _search_from([(lambda t: True, [wrong])]))
    assert res.hops == 1


# --------------------------------------------------------- dedupe / ranking ---

def test_repeat_observations_are_merged():
    brief = TargetBrief(surname="Rossi", known_attributes={"city": "Como"})
    c = Candidate(name="Giulia Rossi", handle="@g", attributes={"city": "Como"},
                  source_url="https://ex.com/1")
    # Returned by two different queries → one merged entry, not two.
    res = _run(brief, _search_from([(lambda t: True, [c])]))
    ids = {sc.candidate.handle for sc in res.candidates}
    assert ids == {"@g"}
    assert len(res.candidates) == 1


def test_candidates_ranked_by_score_desc():
    brief = TargetBrief(surname="Rossi",
                        known_attributes={"school": "Volta", "city": "Como"})
    strong = Candidate(name="Rossi", handle="@a",
                       attributes={"school": "Volta", "city": "Como"})
    weak = Candidate(name="Rossi", handle="@b", attributes={"city": "Como"})
    res = _run(brief, _search_from([(lambda t: True, [weak, strong])]))
    scores = [sc.score for sc in res.candidates]
    assert scores == sorted(scores, reverse=True)


# ------------------------------------------------------------- robustness ---

def test_search_exception_does_not_kill_run():
    brief = TargetBrief(surname="Rossi", given_name="Giulia",
                        known_attributes={"city": "Como"})
    target = Candidate(name="Giulia Rossi", handle="@g",
                       attributes={"city": "Como"})
    async def flaky(q):
        if "site:" in q.text:
            raise RuntimeError("boom")
        return [target]
    res = asyncio.run(discover_entity(
        brief, flaky,
        config=RunConfig(),
    ))
    # Despite some queries raising, the good ones still resolve the target.
    assert res.best is not None and res.best.candidate.handle == "@g"
