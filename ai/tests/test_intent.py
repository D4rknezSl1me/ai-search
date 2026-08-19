"""Offline unit tests for query intent classification + freshness resolution."""

from __future__ import annotations

from datetime import datetime, timezone

import pytest

from app.intent import Intent, classify, resolve_freshness

NOW = datetime(2026, 8, 19, tzinfo=timezone.utc)


def c(q: str) -> Intent:
    return classify(q, now=NOW)


# ------------------------------------------------------------------ news/fresh ---

@pytest.mark.parametrize("q", [
    "latest developments in fusion energy",
    "recent news about the election",
    "what happened today in gaza",
    "breaking updates on the earthquake",
    "openai news this week",
    "trending topics right now",
])
def test_news_fresh_intent(q):
    assert c(q) is Intent.NEWS_FRESH


def test_current_or_next_year_reads_as_fresh():
    assert c("best laptops 2026") is Intent.NEWS_FRESH
    assert c("world cup 2027 schedule") is Intent.NEWS_FRESH


def test_old_year_is_not_fresh():
    assert c("world cup 2018 results") is not Intent.NEWS_FRESH


# -------------------------------------------------------------------- other ---

def test_navigational_intent():
    assert c("stripe official website") is Intent.NAVIGATIONAL
    assert c("github.com login") is Intent.NAVIGATIONAL
    assert c("visit https://example.com") is Intent.NAVIGATIONAL


def test_broad_research_intent():
    assert c("everything about ada lovelace") is Intent.BROAD_RESEARCH
    assert c("comprehensive overview of the roman empire") is Intent.BROAD_RESEARCH
    assert c("find all information on tesla model 3 recalls") is Intent.BROAD_RESEARCH


def test_entity_lookup_intent():
    assert c("Ada Lovelace") is Intent.ENTITY_LOOKUP
    assert c("who is Grace Hopper") is Intent.ENTITY_LOOKUP


def test_factual_default():
    assert c("how does photosynthesis work") is Intent.FACTUAL
    assert c("why is the sky blue") is Intent.FACTUAL


def test_empty_query_is_factual():
    assert c("") is Intent.FACTUAL
    assert c("   ") is Intent.FACTUAL


# ------------------------------------------------- precedence between signals ---

def test_navigational_beats_news():
    # A URL/site signal takes precedence over a recency word.
    assert c("latest posts on example.com") is Intent.NAVIGATIONAL


def test_news_beats_broad():
    # Recency has the concrete freshness hook, so it wins over breadth phrasing.
    assert c("everything about the latest ai news") is Intent.NEWS_FRESH


# ------------------------------------------------------------- resolve_freshness ---

def test_resolve_auto_upgrades_only_for_news():
    assert resolve_freshness("auto", Intent.NEWS_FRESH) == "fresh"
    assert resolve_freshness("auto", Intent.FACTUAL) == "auto"
    assert resolve_freshness("auto", Intent.BROAD_RESEARCH) == "auto"


def test_resolve_respects_explicit_user_choice():
    # Explicit fresh/any always wins over the inferred value.
    assert resolve_freshness("any", Intent.NEWS_FRESH) == "any"
    assert resolve_freshness("fresh", Intent.FACTUAL) == "fresh"
