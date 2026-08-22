"""Offline unit tests for the LLM query planner (docs/15 §4.1, §6).

The LLM is dependency-injected (fake), so parsing/guardrail/merge/degradation all
run without a model. Async tests use asyncio.run (no pytest-asyncio)."""

from __future__ import annotations

import asyncio

from app.entity_brief import Relationship, TargetBrief
from app.entity_planner import (
    make_llm_planner,
    parse_planner_queries,
    sanitize,
)


def _brief(**kw) -> TargetBrief:
    return TargetBrief(**kw)


def _fake_llm(reply: str):
    async def llm(messages, temperature):
        return reply
    return llm


def _plan(brief, reply=None, **kw):
    llm = _fake_llm(reply) if reply is not None else None
    return asyncio.run(make_llm_planner(llm, **kw)(brief))


# ------------------------------------------------------------------- parsing ---

def test_parse_objects_and_strings():
    pairs = parse_planner_queries('[{"q": "a", "platform": "instagram"}, "b"]')
    assert pairs == [("a", "instagram"), ("b", "open_web")]


def test_parse_tolerates_prose_wrapping():
    pairs = parse_planner_queries('Sure!\n[{"q":"x"}]\nHope that helps')
    assert pairs == [("x", "open_web")]


def test_parse_garbage_returns_empty():
    assert parse_planner_queries("no json here") == []
    assert parse_planner_queries("") == []


# ---------------------------------------------------------------- guardrail ---

def test_sanitize_drops_bare_name_when_discriminators_exist():
    b = _brief(surname="Rossi", known_attributes={"city": "Como"})
    kept = sanitize([("Rossi", "open_web"),                 # no discriminator → drop
                     ('"Rossi" Como', "open_web")], b)      # has city → keep
    texts = [q.text for q in kept]
    assert texts == ['"Rossi" Como']


def test_sanitize_allows_site_scope_as_discriminator():
    b = _brief(surname="Rossi", known_attributes={"city": "Como"})
    kept = sanitize([("site:instagram.com Rossi", "instagram")], b)
    assert kept and kept[0].platform == "instagram"
    assert kept[0].specificity >= 1


def test_sanitize_no_discriminators_allows_name():
    b = _brief(surname="Smith")              # nothing to anchor on
    kept = sanitize([("Smith", "open_web")], b)
    assert [q.text for q in kept] == ["Smith"]


def test_sanitize_dedupes_and_drops_overlong():
    b = _brief(surname="Rossi", known_attributes={"city": "Como"})
    kept = sanitize([("Rossi Como", "open_web"), ("rossi como", "open_web"),
                     ("Rossi Como " + "x" * 300, "open_web")], b)
    assert len(kept) == 1


# --------------------------------------------------------- planner behavior ---

def test_no_llm_returns_deterministic():
    b = _brief(surname="Rossi", known_attributes={"city": "Como"})
    got = _plan(b)                                   # llm=None
    assert got == _plan(b)                            # stable
    assert all("Como" in q.text for q in got)        # deterministic backbone


def test_llm_queries_are_unioned_with_deterministic():
    b = _brief(surname="Rossi", known_attributes={"city": "Como"})
    reply = '[{"q": "\\"Rossi\\" Como calcio squad roster", "platform": "open_web"}]'
    got = _plan(b, reply=reply)
    texts = [q.text for q in got]
    assert any("roster" in t for t in texts)         # LLM idea present
    assert any(t == "Rossi Como" or "Como" in t for t in texts)  # backbone still there


def test_llm_error_degrades_to_deterministic():
    b = _brief(surname="Rossi", known_attributes={"city": "Como"})
    async def boom(messages, temperature):
        raise RuntimeError("model down")
    got = asyncio.run(make_llm_planner(boom)(b))
    assert got == _plan(b)                            # exactly the deterministic set


def test_llm_bare_name_idea_is_filtered_out():
    b = _brief(surname="Rossi", known_attributes={"city": "Como"})
    got = _plan(b, reply='[{"q": "Rossi"}]')          # violates guardrail
    assert all(q.text != "Rossi" for q in got)


def test_relationship_counts_as_discriminator():
    b = _brief(surname="Rossi",
               relationships=[Relationship("sibling_of", "Marco Rossi")])
    got = _plan(b, reply='[{"q": "\\"Marco Rossi\\" sorella instagram"}]')
    assert any("sorella" in q.text for q in got)


def test_results_capped_by_max_queries():
    b = _brief(surname="Rossi",
               known_attributes={"city": "Como", "school": "Volta", "employer": "Acme"})
    reply = '[{"q":"Rossi Como a"},{"q":"Rossi Volta b"},{"q":"Rossi Acme c"}]'
    got = _plan(b, reply=reply, max_queries=4)
    assert len(got) == 4
