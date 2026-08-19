"""Freshness-aware ranking (docs/07-RAG-SEARCH.md §2–3).

Blend a recency signal into the retrieval ordering so that, for time-sensitive
questions, newer documents surface ahead of equally-relevant stale ones. The
`freshness` request option (auto | fresh | any) sets how hard recency pushes:

    any   → no recency effect (pure relevance)
    auto  → a gentle nudge (default)
    fresh → recency weighs heavily

Recall-first (CLAUDE.md north star): freshness only *reorders* the shortlist, it
never drops candidates, and **undated documents are held at a neutral weight**
rather than penalized to the bottom — a missing date must not bury a strong
match. Pure and side-effect-light (sets `Candidate.final_score`), so it's fully
unit-testable offline.
"""

from __future__ import annotations

from datetime import datetime, timezone
from typing import Iterable

_SECONDS_PER_DAY = 86400.0


def _parse_dt(value: str | None) -> datetime | None:
    """Parse an ISO date/datetime into a tz-aware UTC datetime, or None."""
    if not value:
        return None
    s = value.strip()
    if not s:
        return None
    # Tolerate a trailing Z (fromisoformat rejects it before 3.11 semantics vary).
    if s.endswith(("Z", "z")):
        s = s[:-1] + "+00:00"
    try:
        dt = datetime.fromisoformat(s)
    except ValueError:
        # Fall back to a bare date (YYYY-MM-DD) prefix.
        try:
            dt = datetime.fromisoformat(s[:10])
        except ValueError:
            return None
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=timezone.utc)
    return dt.astimezone(timezone.utc)


def recency_weight(
    published_at: str | None,
    now: datetime,
    half_life_days: float,
    undated_weight: float,
) -> float:
    """A [0,1] recency score: 1.0 for brand-new, halving every `half_life_days`.

    Undated (or unparseable) documents return `undated_weight` — a neutral middle
    so they neither win nor lose purely for lacking a timestamp.
    """
    dt = _parse_dt(published_at)
    if dt is None:
        return undated_weight
    age_days = (now - dt).total_seconds() / _SECONDS_PER_DAY
    if age_days <= 0:  # published now or (clock skew) in the future
        return 1.0
    if half_life_days <= 0:
        return 1.0
    return 0.5 ** (age_days / half_life_days)


def blend_weight(mode: str, auto_weight: float, fresh_weight: float) -> float:
    """How much recency counts for, given the request's freshness mode."""
    if mode == "fresh":
        return fresh_weight
    if mode == "auto":
        return auto_weight
    return 0.0  # "any" or anything unrecognized → relevance only


def apply_freshness(
    cands: Iterable,
    mode: str,
    *,
    half_life_days: float,
    undated_weight: float,
    auto_weight: float,
    fresh_weight: float,
    now: datetime | None = None,
) -> bool:
    """Set `final_score` on each candidate blending normalized relevance + recency.

    Returns True if freshness was applied (mode weighs recency and there are
    candidates), False otherwise — in which case `final_score` is left as-is and
    downstream ordering falls back to the plain relevance `score`.
    """
    cands = list(cands)
    w = blend_weight(mode, auto_weight, fresh_weight)
    if w <= 0 or not cands:
        return False

    now = now or datetime.now(timezone.utc)
    scores = [c.score for c in cands]
    lo, hi = min(scores), max(scores)
    span = hi - lo
    for c in cands:
        # Min-max normalize within the shortlist so blending is scale-independent
        # (rerank logits and RRF sums live on very different scales).
        norm = (c.score - lo) / span if span > 1e-12 else 1.0
        rec = recency_weight(c.published_at, now, half_life_days, undated_weight)
        c.final_score = (1.0 - w) * norm + w * rec
    return True
