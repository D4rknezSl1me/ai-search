"""Offline unit tests for API-key auth + per-key rate limiting (app.auth)."""

from __future__ import annotations

from app.auth import (
    AuthConfig,
    RateLimiter,
    decide,
    extract_key,
    parse_keys,
)


# ------------------------------------------------------------------- keys -----

def test_parse_keys():
    assert parse_keys("a, b ,, c") == frozenset({"a", "b", "c"})
    assert parse_keys("") == frozenset()
    assert parse_keys(None) == frozenset()


def test_extract_key_header_variants():
    assert extract_key({"x-api-key": " k1 "}) == "k1"
    assert extract_key({"authorization": "Bearer k2"}) == "k2"
    assert extract_key({"authorization": "bearer k3"}) == "k3"    # case-insensitive
    assert extract_key({"authorization": "Basic zzz"}) == ""      # not bearer
    assert extract_key({}) == ""


# ---------------------------------------------------------------- limiter -----

def test_rate_limiter_allows_burst_then_blocks():
    rl = RateLimiter(per_min=3)   # capacity 3
    assert rl.allow("k", now=0.0)   # 3 -> 2
    assert rl.allow("k", now=0.0)   # 2 -> 1
    assert rl.allow("k", now=0.0)   # 1 -> 0
    assert not rl.allow("k", now=0.0)   # empty


def test_rate_limiter_refills_over_time():
    rl = RateLimiter(per_min=60)   # 1 token/sec
    for _ in range(60):
        rl.allow("k", now=0.0)
    assert not rl.allow("k", now=0.0)      # bucket empty
    assert rl.allow("k", now=1.0)          # ~1 token refilled after 1s
    assert not rl.allow("k", now=1.0)


def test_rate_limiter_is_per_key():
    rl = RateLimiter(per_min=1)
    assert rl.allow("a", now=0.0)
    assert not rl.allow("a", now=0.0)
    assert rl.allow("b", now=0.0)          # separate bucket


# ----------------------------------------------------------------- decide -----

def _cfg(enabled=True, keys=("secret",)):
    return AuthConfig(enabled=enabled, allowed_keys=frozenset(keys))


def test_disabled_allows_everything():
    rl = RateLimiter(1)
    assert decide("/v1/search", "", cfg=_cfg(enabled=False), limiter=rl) is None


def test_non_v1_paths_are_exempt():
    rl = RateLimiter(1)
    cfg = _cfg()
    assert decide("/", "", cfg=cfg, limiter=rl) is None
    assert decide("/healthz", "", cfg=cfg, limiter=rl) is None
    assert decide("/internal/reconcile", "", cfg=cfg, limiter=rl) is None


def test_missing_or_bad_key_401():
    rl = RateLimiter(10)
    assert decide("/v1/search", "", cfg=_cfg(), limiter=rl, now=0.0) == (401, "missing or invalid API key")
    assert decide("/v1/search", "nope", cfg=_cfg(), limiter=rl, now=0.0)[0] == 401


def test_valid_key_passes_until_rate_limited():
    rl = RateLimiter(per_min=2)
    cfg = _cfg()
    assert decide("/v1/search", "secret", cfg=cfg, limiter=rl, now=0.0) is None
    assert decide("/v1/search", "secret", cfg=cfg, limiter=rl, now=0.0) is None
    assert decide("/v1/search", "secret", cfg=cfg, limiter=rl, now=0.0) == (429, "rate limit exceeded")
