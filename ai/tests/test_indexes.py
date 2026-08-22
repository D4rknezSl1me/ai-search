"""Offline unit tests for the Qdrant collection payload builder (app.indexes)."""

from __future__ import annotations

from app.indexes import qdrant_collection_body


def test_body_without_quantization():
    b = qdrant_collection_body(dim=1024, quantization=False)
    assert b["vectors"] == {"size": 1024, "distance": "Cosine"}
    assert "quantization_config" not in b


def test_body_with_quantization():
    b = qdrant_collection_body(dim=768, quantization=True, quantile=0.95, always_ram=False)
    assert b["vectors"]["size"] == 768
    sq = b["quantization_config"]["scalar"]
    assert sq == {"type": "int8", "quantile": 0.95, "always_ram": False}


def test_distance_is_configurable():
    b = qdrant_collection_body(dim=4, distance="Dot", quantization=False)
    assert b["vectors"]["distance"] == "Dot"


def test_defaults_track_config():
    # With no explicit flags, defaults come from settings (quantization off by default).
    b = qdrant_collection_body()
    assert "quantization_config" not in b
    assert b["vectors"]["size"] >= 1   # embed_dim from config
