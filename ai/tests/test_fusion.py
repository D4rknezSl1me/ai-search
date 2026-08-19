"""Offline unit tests for multi-query RRF fusion (no network).

`_rrf_runs` is pure — it fuses already-fetched lexical/vector hit lists — so it's
tested directly with hand-built hit dicts shaped like OpenSearch / Qdrant results.
"""

from __future__ import annotations

from app.config import settings
from app.retrieval import _rrf_runs


def lex_hit(chunk_id: str, **payload) -> dict:
    return {"_id": chunk_id, "_source": {"chunk_id": chunk_id, **payload}}


def vec_hit(chunk_id: str, **payload) -> dict:
    return {"id": chunk_id, "payload": {"chunk_id": chunk_id, **payload}}


def rrf(rank: int) -> float:
    return 1.0 / (settings.rrf_k + rank + 1)


def test_single_run_records_ranks_and_score():
    lex = [lex_hit("a", url="http://a"), lex_hit("b", url="http://b")]
    vec = [vec_hit("b"), vec_hit("c")]
    cands = _rrf_runs([(lex, vec)])

    assert set(cands) == {"a", "b", "c"}
    assert cands["a"].lexical_rank == 0 and cands["a"].vector_rank is None
    assert cands["b"].lexical_rank == 1 and cands["b"].vector_rank == 0
    # b appears in both lists → its fused score sums both contributions.
    assert abs(cands["b"].fused_score - (rrf(1) + rrf(0))) < 1e-12
    assert cands["b"].url == "http://b"  # payload carried from lexical hit


def test_multi_run_boosts_a_chunk_shared_across_queries():
    # "x" is retrieved (top lexical) by BOTH expanded queries; "y" only by one.
    run1 = ([lex_hit("x"), lex_hit("y")], [])
    run2 = ([lex_hit("x")], [])
    cands = _rrf_runs([run1, run2])

    # x accrues score from both runs; y from one → x ranks strictly higher.
    assert cands["x"].fused_score == rrf(0) + rrf(0)
    assert cands["y"].fused_score == rrf(1)
    assert cands["x"].fused_score > cands["y"].fused_score


def test_best_rank_is_kept_across_runs():
    # "z" is rank 2 in the first run but rank 0 in the second → keep the best (0).
    run1 = ([lex_hit("p"), lex_hit("q"), lex_hit("z")], [])
    run2 = ([lex_hit("z")], [])
    cands = _rrf_runs([run1, run2])
    assert cands["z"].lexical_rank == 0
    # Score still sums both appearances (rank 2 + rank 0).
    assert abs(cands["z"].fused_score - (rrf(2) + rrf(0))) < 1e-12


def test_vector_only_hit_uses_payload():
    cands = _rrf_runs([([], [vec_hit("v", url="http://v", domain="v.com")])])
    assert cands["v"].url == "http://v"
    assert cands["v"].domain == "v.com"
    assert cands["v"].vector_rank == 0


def test_hits_missing_chunk_id_are_skipped():
    lex = [{"_source": {"url": "no-id"}}]  # no chunk_id, no _id
    vec = [{"payload": {"url": "no-id"}}]
    assert _rrf_runs([(lex, vec)]) == {}


def test_empty_runs():
    assert _rrf_runs([]) == {}
    assert _rrf_runs([([], [])]) == {}
