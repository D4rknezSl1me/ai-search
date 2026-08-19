"""Offline unit tests for indexer helpers (no network)."""

from __future__ import annotations

from app.indexer import _keyphrases


def test_keyphrases_from_json_string():
    # asyncpg returns jsonb as a text string.
    assert _keyphrases('["analytical engine", "charles babbage"]') == [
        "analytical engine",
        "charles babbage",
    ]


def test_keyphrases_from_list():
    assert _keyphrases(["a", "b"]) == ["a", "b"]


def test_keyphrases_none_and_junk():
    assert _keyphrases(None) == []
    assert _keyphrases("not json") == []
    assert _keyphrases("{}") == []          # object, not a list
    assert _keyphrases(42) == []


def test_keyphrases_coerces_non_strings():
    assert _keyphrases("[1, 2]") == ["1", "2"]
