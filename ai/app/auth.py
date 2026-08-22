"""API-key auth + per-key rate limiting (Phase 4 — multi-tenant readiness).

Off by default (`AUTH_ENABLED=false`), so local/dev use is unaffected. When
enabled, public `/v1/*` endpoints require a known API key (header `X-API-Key` or
`Authorization: Bearer <key>`) and are rate-limited per key with a token bucket
(smooth: allows short bursts up to the per-minute capacity, refills continuously).

The decision logic is a pure function (`decide`) over injected state + `now`, so
it's unit-testable offline with no FastAPI/network; `main.py` wraps it in a thin
middleware. Self-hosted, no external identity provider (CLAUDE.md rule 2) — keys
are operator-issued strings in config.
"""

from __future__ import annotations

import time
from dataclasses import dataclass, field


def parse_keys(csv: str) -> frozenset[str]:
    """Parse the configured comma-separated API keys into a set (blanks dropped)."""
    return frozenset(k.strip() for k in (csv or "").split(",") if k.strip())


@dataclass
class _Bucket:
    tokens: float
    updated: float


class RateLimiter:
    """Per-key token bucket. Capacity == refill-per-minute, so a fresh key may
    burst up to `per_min` requests then settles to `per_min`/minute steady-state.
    """

    def __init__(self, per_min: int):
        self.capacity = float(max(1, per_min))
        self.refill_per_sec = self.capacity / 60.0
        self._buckets: dict[str, _Bucket] = {}

    def allow(self, key: str, now: float, cost: float = 1.0) -> bool:
        b = self._buckets.get(key)
        if b is None:
            # A new key starts full, minus this request.
            self._buckets[key] = _Bucket(tokens=self.capacity - cost, updated=now)
            return True
        elapsed = max(0.0, now - b.updated)
        b.tokens = min(self.capacity, b.tokens + elapsed * self.refill_per_sec)
        b.updated = now
        if b.tokens >= cost:
            b.tokens -= cost
            return True
        return False


@dataclass
class AuthConfig:
    enabled: bool = False
    allowed_keys: frozenset[str] = field(default_factory=frozenset)


def extract_key(headers: dict[str, str]) -> str:
    """Pull the API key from `X-API-Key` or `Authorization: Bearer <key>`.

    Header lookup is case-insensitive (callers pass a lower-cased-key dict, which
    is what Starlette's Headers yields via .get)."""
    key = headers.get("x-api-key")
    if key:
        return key.strip()
    auth = headers.get("authorization") or ""
    if auth[:7].lower() == "bearer ":
        return auth[7:].strip()
    return ""


def decide(
    path: str,
    key: str,
    *,
    cfg: AuthConfig,
    limiter: RateLimiter,
    now: float | None = None,
) -> tuple[int, str] | None:
    """Authorization decision for one request.

    Returns None to allow the request through, or `(status, reason)` to reject.
    Only `/v1/*` is gated — the UI (`/`), health/metrics, and internal admin
    endpoints are exempt so the product page and ops tooling keep working.
    """
    if not cfg.enabled or not path.startswith("/v1/"):
        return None
    if not key or key not in cfg.allowed_keys:
        return (401, "missing or invalid API key")
    if not limiter.allow(key, time.monotonic() if now is None else now):
        return (429, "rate limit exceeded")
    return None
