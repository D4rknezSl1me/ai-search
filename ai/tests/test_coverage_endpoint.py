"""Offline test for GET /v1/coverage — graceful degradation + shape.

Calls the handler directly with `clients.pg` monkeypatched (no real Postgres),
so both the datastore-down path and the happy path are covered without network.
"""

from __future__ import annotations

import asyncio

import app.clients as clients
import app.main as main


def test_coverage_degrades_when_pg_unreachable(monkeypatch):
    async def boom():
        raise RuntimeError("no postgres")
    monkeypatch.setattr(clients, "pg", boom)

    out = asyncio.run(main.v1_coverage())
    assert out["available"] is False
    assert out["documents"] == 0 and out["chunks"] == 0 and out["by_domain"] == []


def test_coverage_happy_path(monkeypatch):
    class _Pool:
        async def fetchval(self, q):
            if "count(*) FROM documents WHERE n_chunks > 0" in q:
                return 7
            if "count(*) FROM documents" in q:
                return 10
            if "sum(n_chunks)" in q:
                return 42
            return 0
        async def fetch(self, q):
            return [{"domain": "example.com", "docs": 5},
                    {"domain": "blog.org", "docs": 2}]

    async def pool():
        return _Pool()
    monkeypatch.setattr(clients, "pg", pool)

    out = asyncio.run(main.v1_coverage())
    assert out["available"] is True
    assert out["documents"] == 10 and out["documents_indexed"] == 7 and out["chunks"] == 42
    assert out["by_domain"][0] == {"domain": "example.com", "documents": 5}
