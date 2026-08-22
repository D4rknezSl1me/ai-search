"""Per-key API usage metering (Phase 4 — multi-tenant readiness).

Counts `/v1/*` requests by API key and outcome (ok / unauthorized / rate_limited)
and renders them in Prometheus text format for the existing `/metrics` endpoint —
so an operator can see traffic per tenant and spot abuse, alongside the auth +
rate-limiting from `app.auth`.

Two cardinality safeguards keep the metric bounded and secret-free:
- keys are **hashed** to a short stable label (`k_<8 hex>`) — the raw key never
  lands in metrics;
- any request without a *recognized* key is bucketed as `anonymous`, so random
  bad keys can't explode label cardinality.

Pure/stdlib, unit-testable offline.
"""

from __future__ import annotations

import hashlib
from collections import defaultdict

_OUTCOMES = ("ok", "unauthorized", "rate_limited")


def mask_key(key: str) -> str:
    """A stable, non-reversible label for a key (empty → 'anonymous')."""
    if not key:
        return "anonymous"
    return "k_" + hashlib.sha256(key.encode("utf-8")).hexdigest()[:8]


class UsageMeter:
    """In-process counters of API requests by (key-label, outcome)."""

    def __init__(self) -> None:
        self._counts: dict[tuple[str, str], int] = defaultdict(int)

    def record(self, key_label: str, outcome: str) -> None:
        self._counts[(key_label, outcome)] += 1

    def snapshot(self) -> dict[tuple[str, str], int]:
        return dict(self._counts)

    def total(self) -> int:
        return sum(self._counts.values())

    def render(self) -> str:
        """Prometheus exposition text for the recorded counters (sorted, stable)."""
        lines = [
            "# HELP aisearch_api_requests_total API requests by key and outcome.",
            "# TYPE aisearch_api_requests_total counter",
        ]
        for (label, outcome), n in sorted(self._counts.items()):
            lines.append(
                f'aisearch_api_requests_total{{key="{label}",outcome="{outcome}"}} {n}'
            )
        return "\n".join(lines)
