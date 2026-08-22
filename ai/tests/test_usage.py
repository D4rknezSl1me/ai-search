"""Offline unit tests for per-key usage metering (app.usage)."""

from __future__ import annotations

from app.usage import UsageMeter, mask_key


def test_mask_key_is_stable_and_masks():
    a = mask_key("secret")
    assert a == mask_key("secret")          # stable
    assert a.startswith("k_") and "secret" not in a
    assert mask_key("other") != a           # distinct keys distinct labels
    assert mask_key("") == "anonymous"


def test_meter_records_and_totals():
    m = UsageMeter()
    m.record("k_abc", "ok")
    m.record("k_abc", "ok")
    m.record("k_abc", "rate_limited")
    m.record("anonymous", "unauthorized")
    assert m.total() == 4
    assert m.snapshot()[("k_abc", "ok")] == 2
    assert m.snapshot()[("k_abc", "rate_limited")] == 1


def test_render_prometheus_format():
    m = UsageMeter()
    m.record("anonymous", "ok")
    m.record("k_abc", "unauthorized")
    out = m.render()
    assert "# TYPE aisearch_api_requests_total counter" in out
    assert 'aisearch_api_requests_total{key="anonymous",outcome="ok"} 1' in out
    assert 'aisearch_api_requests_total{key="k_abc",outcome="unauthorized"} 1' in out
    # Deterministic ordering (sorted) → lines stable across renders.
    assert m.render() == out


def test_render_empty_is_header_only():
    out = UsageMeter().render()
    assert "aisearch_api_requests_total{" not in out
    assert out.startswith("# HELP")
