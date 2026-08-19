"""Offline unit tests for freshness-aware ranking (no network)."""

from __future__ import annotations

from datetime import datetime, timedelta, timezone

from app.freshness import apply_freshness, blend_weight, recency_weight
from app.retrieval import Candidate

NOW = datetime(2026, 8, 19, tzinfo=timezone.utc)
HL = 180.0
UNDATED = 0.5


def iso(days_ago: float) -> str:
    return (NOW - timedelta(days=days_ago)).isoformat()


# ------------------------------------------------------------- recency_weight ---

def test_recency_fresh_is_near_one():
    assert recency_weight(iso(0), NOW, HL, UNDATED) == 1.0


def test_recency_halves_at_half_life():
    assert abs(recency_weight(iso(HL), NOW, HL, UNDATED) - 0.5) < 1e-9


def test_recency_quarter_at_two_half_lives():
    assert abs(recency_weight(iso(2 * HL), NOW, HL, UNDATED) - 0.25) < 1e-9


def test_recency_future_clamped_to_one():
    assert recency_weight(iso(-30), NOW, HL, UNDATED) == 1.0


def test_recency_undated_returns_neutral():
    assert recency_weight(None, NOW, HL, UNDATED) == UNDATED
    assert recency_weight("not-a-date", NOW, HL, UNDATED) == UNDATED


def test_recency_accepts_z_suffix_and_bare_date():
    assert recency_weight("2026-08-19T00:00:00Z", NOW, HL, UNDATED) == 1.0
    assert abs(recency_weight("2026-02-20", NOW, HL, UNDATED) - 0.5) < 1e-3  # ~180d


# --------------------------------------------------------------- blend_weight ---

def test_blend_weight_modes():
    assert blend_weight("any", 0.15, 0.45) == 0.0
    assert blend_weight("auto", 0.15, 0.45) == 0.15
    assert blend_weight("fresh", 0.15, 0.45) == 0.45
    assert blend_weight("garbage", 0.15, 0.45) == 0.0


# ------------------------------------------------------------- apply_freshness ---

def _cand(cid: str, score: float, published_at: str | None) -> Candidate:
    c = Candidate(
        chunk_id=cid, document_id=1, url="", domain=None, title=None, text=None,
        published_at=published_at, source_type=None, authority=0.5,
    )
    c.rerank_score = score  # relevance score used by .score
    return c


def _kw(**over):
    base = dict(half_life_days=HL, undated_weight=UNDATED, auto_weight=0.15,
               fresh_weight=0.45, now=NOW)
    base.update(over)
    return base


# Anchors establish a realistic relevance span (a real shortlist has ~50 docs);
# min-max is linear, so without a spread two candidates always separate to 0/1.
def _anchors() -> list[Candidate]:
    return [_cand("hi", 1.0, iso(0)), _cand("lo", 0.0, iso(0))]


def test_any_mode_is_noop():
    cands = [_cand("a", 0.9, iso(0)), _cand("b", 0.1, iso(1000))]
    applied = apply_freshness(cands, "any", **_kw())
    assert applied is False
    assert all(c.final_score is None for c in cands)


def test_fresh_flips_near_tie_but_auto_keeps_strong():
    # Near-equal relevance atop a wide span: "fresh" promotes the newer doc,
    # while the gentle "auto" nudge leaves the (barely) stronger stale doc on top.
    old_strong = _cand("old", 1.0, iso(4 * HL))   # recency ~0.06
    new_weak = _cand("new", 0.55, iso(0))         # recency 1.0
    anchors = _anchors()

    apply_freshness([*anchors, old_strong, new_weak], "auto", **_kw())
    assert old_strong.final_score > new_weak.final_score

    apply_freshness([*anchors, old_strong, new_weak], "fresh", **_kw())
    assert new_weak.final_score > old_strong.final_score


def test_undated_uses_neutral_not_zero():
    # Equally-relevant dated-old vs undated. Undated recency 0.5 beats the old
    # doc's ~0.06, so a missing date must not bury it below a stale match.
    dated_old = _cand("old", 0.5, iso(4 * HL))
    undated = _cand("und", 0.5, None)
    apply_freshness([*_anchors(), dated_old, undated], "fresh", **_kw())
    assert undated.final_score > dated_old.final_score


def test_single_candidate_gets_full_norm():
    only = _cand("solo", 0.3, iso(0))
    apply_freshness([only], "fresh", **_kw())
    # span==0 → norm defaults to 1.0; recency 1.0 → final 1.0
    assert abs(only.final_score - 1.0) < 1e-9


def test_empty_returns_false():
    assert apply_freshness([], "fresh", **_kw()) is False
