"""Offline unit tests for query understanding (no LLM, no network).

The LLM is dependency-injected, so planning is exercised with fakes. Async tests
use asyncio.run to avoid a pytest-asyncio dependency.
"""

from __future__ import annotations

import asyncio

from app.understand import (
    QueryPlan,
    clean_expansions,
    normalize_query,
    parse_expansions,
    plan_query,
)


# ------------------------------------------------------------------ normalize ---

def test_normalize_collapses_whitespace():
    assert normalize_query("  who   is\tAda\nLovelace  ") == "who is Ada Lovelace"


def test_normalize_empty():
    assert normalize_query("") == ""
    assert normalize_query("   ") == ""


# --------------------------------------------------------------------- parse ---

def test_parse_json_array():
    assert parse_expansions('["ada lovelace", "first programmer"]') == [
        "ada lovelace",
        "first programmer",
    ]


def test_parse_json_array_wrapped_in_prose():
    raw = 'Sure! Here are some queries:\n["who was ada lovelace", "analytical engine"]\nHope that helps.'
    assert parse_expansions(raw) == ["who was ada lovelace", "analytical engine"]


def test_parse_json_array_of_objects():
    raw = '[{"query": "ada lovelace biography"}, {"q": "babbage collaborator"}]'
    assert parse_expansions(raw) == ["ada lovelace biography", "babbage collaborator"]


def test_parse_line_fallback_when_not_json():
    raw = "who was ada lovelace\nanalytical engine inventor"
    assert parse_expansions(raw) == ["who was ada lovelace", "analytical engine inventor"]


def test_parse_strips_numbering_bullets_and_quotes():
    raw = '1. "ada lovelace"\n- analytical engine\n2) babbage'
    assert parse_expansions(raw) == ["ada lovelace", "analytical engine", "babbage"]


def test_parse_empty():
    assert parse_expansions("") == []
    assert parse_expansions("   ") == []


# --------------------------------------------------------------------- clean ---

def test_clean_drops_original_case_insensitively():
    got = clean_expansions(["Ada Lovelace", "first programmer"], "ada lovelace", 5)
    assert got == ["first programmer"]


def test_clean_dedupes_preserving_order():
    got = clean_expansions(["a query", "A QUERY", "b query"], "orig", 5)
    assert got == ["a query", "b query"]


def test_clean_caps_count():
    got = clean_expansions(["q1", "q2", "q3", "q4"], "orig", 2)
    assert got == ["q1", "q2"]


def test_clean_drops_empty_and_too_long():
    long_q = "x " * 150  # > 200 chars after normalize
    got = clean_expansions(["", "   ", long_q, "keep me"], "orig", 5)
    assert got == ["keep me"]


def test_clean_zero_budget():
    assert clean_expansions(["a", "b"], "orig", 0) == []


# ------------------------------------------------------------------ QueryPlan ---

def test_queryplan_queries_and_flags():
    p = QueryPlan("original", ["e1", "e2"])
    assert p.queries == ["original", "e1", "e2"]
    assert p.expanded is True
    assert QueryPlan("only").expanded is False


# ----------------------------------------------------------------- plan_query ---

def _fake_llm(reply: str):
    async def _llm(messages, temperature):
        return reply
    return _llm


def test_plan_query_expands():
    plan = asyncio.run(
        plan_query(
            "  who is ada lovelace  ",
            expand=True,
            max_expansions=3,
            llm=_fake_llm('["ada lovelace biography", "first computer programmer"]'),
        )
    )
    assert plan.original == "who is ada lovelace"
    assert plan.expansions == ["ada lovelace biography", "first computer programmer"]


def test_plan_query_drops_original_echoed_by_model():
    plan = asyncio.run(
        plan_query(
            "ada lovelace",
            expand=True,
            max_expansions=3,
            llm=_fake_llm('["ada lovelace", "analytical engine"]'),
        )
    )
    assert plan.expansions == ["analytical engine"]


def test_plan_query_llm_error_falls_back_to_original():
    async def _boom(messages, temperature):
        raise RuntimeError("model down")

    plan = asyncio.run(
        plan_query("ada lovelace", expand=True, max_expansions=3, llm=_boom)
    )
    assert plan.original == "ada lovelace"
    assert plan.expansions == []


def test_plan_query_expand_disabled():
    plan = asyncio.run(
        plan_query(
            "ada lovelace",
            expand=False,
            max_expansions=3,
            llm=_fake_llm('["should", "not", "be", "used"]'),
        )
    )
    assert plan.expansions == []


def test_plan_query_no_llm():
    plan = asyncio.run(
        plan_query("ada lovelace", expand=True, max_expansions=3, llm=None)
    )
    assert plan.expansions == []


def test_plan_query_empty_query():
    plan = asyncio.run(
        plan_query("   ", expand=True, max_expansions=3, llm=_fake_llm('["x"]'))
    )
    assert plan.original == ""
    assert plan.expansions == []
