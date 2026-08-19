"""Offline unit tests for near-duplicate detection + assembly dedup."""

from __future__ import annotations

from app.dedup import DedupIndex, jaccard, normalize, shingles
from app.retrieval import Candidate, _assemble


# ------------------------------------------------------------------ normalize ---

def test_normalize_lowercases_and_collapses_ws():
    assert normalize("  The   QUICK\tBrown\nFox ") == "the quick brown fox"


def test_normalize_none_and_empty():
    assert normalize(None) == ""
    assert normalize("   ") == ""


# ------------------------------------------------------------------- shingles ---

def test_shingles_word_kgrams():
    s = shingles("the quick brown fox jumps", 3)
    assert "the quick brown" in s and "brown fox jumps" in s
    assert len(s) == 3


def test_shingles_short_text_is_single_whole_string():
    # Fewer than k words → one whole-string shingle (exact-match only).
    assert shingles("brown fox", 5) == frozenset({"brown fox"})


def test_shingles_empty():
    assert shingles("", 5) == frozenset()


# -------------------------------------------------------------------- jaccard ---

def test_jaccard_identical_and_disjoint():
    a = frozenset({"x", "y", "z"})
    assert jaccard(a, a) == 1.0
    assert jaccard(a, frozenset({"p", "q"})) == 0.0
    assert jaccard(frozenset(), frozenset()) == 1.0
    assert jaccard(a, frozenset()) == 0.0


# ----------------------------------------------------------------- DedupIndex ---

# A realistic chunk-length passage (~130 words). Query chunks are long, so a
# few-word edit barely moves the shingle Jaccard — the regime the 0.8 threshold
# is tuned for. A too-short fixture would make one word change swing the score.
LOREM = (
    "the analytical engine was a proposed mechanical general purpose computer "
    "designed by charles babbage it was first described in eighteen thirty seven "
    "as the successor to babbage difference engine which had been a design for a "
    "simpler mechanical calculator the analytical engine incorporated an arithmetic "
    "logic unit control flow in the form of conditional branching and loops and "
    "integrated memory making it the first design for a general purpose computer "
    "that could be described in modern terms as turing complete in other words the "
    "logical structure of the analytical engine was essentially the same as that "
    "which has dominated computer design in the electronic era babbage was never "
    "able to complete construction of any of his machines due to conflicts with his "
    "chief engineer and inadequate funding but his son later built a portion of the "
    "engine that was able to perform basic calculations and print results"
)


def test_exact_and_whitespace_case_variants_are_duplicates():
    idx = DedupIndex()
    idx.add(LOREM)
    assert idx.is_duplicate(LOREM) is True
    # Same content, re-crawled with mangled case + whitespace → still a duplicate.
    variant = ("  " + LOREM.upper().replace(" ", "   \t")).replace("engine", "Engine")
    assert idx.is_duplicate(variant) is True


def test_near_duplicate_with_one_word_changed():
    idx = DedupIndex(threshold=0.8)
    idx.add(LOREM)
    near = LOREM.replace("charles babbage", "charles p babbage")
    assert idx.is_duplicate(near) is True


def test_distinct_text_is_not_duplicate():
    idx = DedupIndex()
    idx.add(LOREM)
    assert idx.is_duplicate(
        "ada lovelace wrote the first algorithm intended to be processed by a machine "
        "and is often regarded as the first computer programmer in history"
    ) is False


def test_is_duplicate_does_not_mutate_index():
    idx = DedupIndex()
    idx.add(LOREM)
    other = "a completely different sentence about mountains and rivers and forests today"
    assert idx.is_duplicate(other) is False
    # Not committed → still not a duplicate on a second check.
    assert idx.is_duplicate(other) is False


def test_empty_text_never_flagged_after_exact():
    idx = DedupIndex()
    assert idx.is_duplicate("") is False
    idx.add("")
    # Empty normalizes into the exact set, so a second empty is a duplicate.
    assert idx.is_duplicate("") is True


# ------------------------------------------------------- assembly integration ---

def _cand(cid: str, score: float, text: str, domain: str) -> Candidate:
    c = Candidate(chunk_id=cid, document_id=int(cid), url=f"http://{domain}/{cid}",
                  domain=domain, title=None, text=text, published_at=None,
                  source_type=None, authority=0.5)
    c.rerank_score = score
    return c


def test_assemble_drops_near_duplicate_and_backfills_distinct():
    # top result and a near-dup of it (different domain), plus a distinct doc.
    top = _cand("1", 0.9, LOREM, "a.com")
    dup = _cand("2", 0.8, LOREM.replace("charles babbage", "charles babbage esq"), "b.com")
    distinct = _cand("3", 0.7, "ada lovelace first programmer analytical engine notes "
                                "translated menabrea memoir with extensive original addendum", "c.com")
    out = _assemble([top, dup, distinct], max_sources=2)
    ids = [c.chunk_id for c in out]
    assert ids == ["1", "3"]  # near-dup "2" skipped, distinct "3" backfilled


def test_assemble_keeps_distinct_texts():
    a = _cand("1", 0.9, "first distinct passage about steam engines and railways history", "a.com")
    b = _cand("2", 0.8, "second unrelated passage regarding oceanography and marine biology", "b.com")
    out = _assemble([a, b], max_sources=5)
    assert {c.chunk_id for c in out} == {"1", "2"}
