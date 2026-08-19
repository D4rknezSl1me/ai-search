"""Offline unit tests for reconciliation helpers (no network)."""

from __future__ import annotations

from app.reconcile import valid_chunk_ids


def test_valid_chunk_ids():
    assert valid_chunk_ids(7, 3) == ["7:0", "7:1", "7:2"]


def test_valid_chunk_ids_empty_for_nonpositive():
    assert valid_chunk_ids(7, 0) == []
    assert valid_chunk_ids(7, -1) == []
