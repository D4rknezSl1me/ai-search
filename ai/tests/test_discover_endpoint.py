"""Offline test for the POST /v1/discover/entity handler wiring.

Calls the handler coroutine directly with `app.main.retrieve` monkeypatched, so
the request→brief→loop→payload path is exercised without touching the network or
the startup lifespan (indexer / index bootstrap)."""

from __future__ import annotations

import asyncio
import json
from dataclasses import dataclass, field

import app.main as main
from app.schemas import (
    DiscoverConstraints,
    DiscoverEntityRequest,
    DiscoverRelationship,
    DiscoverSubject,
)


@dataclass
class _Doc:
    text: str = ""
    url: str = ""
    title: str = ""


@dataclass
class _Result:
    candidates: list = field(default_factory=list)


def _call(req):
    body = asyncio.run(main.v1_discover_entity(req)).body
    return json.loads(body)


def test_endpoint_resolves_and_shapes_payload(monkeypatch):
    async def fake_retrieve(query, filters, *, max_sources, expand):
        if "Volta" in query or "Marco Rossi" in query or "Como" in query:
            return _Result([_Doc(
                text=("Giulia Rossi del Liceo Volta di Como, sorella di Marco Rossi. "
                      "@giulia.rossi"),
                url="https://news.local/g")])
        return _Result([])

    monkeypatch.setattr(main, "retrieve", fake_retrieve)

    req = DiscoverEntityRequest(
        goal="social_handle",
        subject=DiscoverSubject(
            surname="Rossi",
            known_attributes={"school": "Liceo Volta", "city": "Como"},
            relationships=[DiscoverRelationship(type="sibling_of", of="Marco Rossi")],
        ),
    )
    payload = _call(req)

    assert payload["status"] in ("resolved", "candidates")
    assert payload["best"] is not None
    assert payload["best"]["source_url"] == "https://news.local/g"
    assert payload["candidates"], "expected ranked candidates"
    assert "signals" in payload["candidates"][0]           # evidence trail present
    assert payload["stats"]["fetches"] >= 1


def test_endpoint_rejects_empty_brief(monkeypatch):
    # No surname/given_name/handle → 400, without ever calling retrieve.
    called = {"n": 0}

    async def fake_retrieve(*a, **k):
        called["n"] += 1
        return _Result([])

    monkeypatch.setattr(main, "retrieve", fake_retrieve)
    resp = asyncio.run(main.v1_discover_entity(
        DiscoverEntityRequest(constraints=DiscoverConstraints())))
    assert resp.status_code == 400
    assert called["n"] == 0
