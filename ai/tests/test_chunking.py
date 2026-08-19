"""Offline unit tests for structure-aware chunking (no network)."""

from __future__ import annotations

import pytest

from app.chunking import chunk_text
from app.config import settings


@pytest.fixture
def small_chunks(monkeypatch):
    """Shrink the chunk sizes so modest test fixtures exercise the split paths."""
    monkeypatch.setattr(settings, "chunk_target_chars", 120)
    monkeypatch.setattr(settings, "chunk_overlap_chars", 20)
    monkeypatch.setattr(settings, "chunk_min_chars", 30)


SENTENCES = (
    "The quick brown fox jumps over the lazy dog. "
    "Pack my box with five dozen liquor jugs today. "
    "How vexingly quick daft zebras jump around here. "
    "The five boxing wizards jump very quickly now. "
    "Sphinx of black quartz judge my solemn vow here. "
    "Jackdaws love my big sphinx made of pale quartz. "
    "Waltz bad nymph for quick jigs said the old vex. "
    "Bright vixens jump the lazy fowl and quick dogs."
)


def _spans_cover_text(chunks, n):
    assert chunks[0].char_start == 0
    assert chunks[-1].char_end == n
    for a, b in zip(chunks, chunks[1:]):
        # Consecutive chunks are contiguous or overlap — no gap in coverage.
        assert b.char_start <= a.char_end


# ------------------------------------------------------------------- basics ---

def test_empty_and_whitespace():
    assert chunk_text("") == []
    assert chunk_text("   \n\n  ") == []


def test_short_text_single_chunk(small_chunks):
    text = "Just one short sentence here."
    chunks = chunk_text(text)
    assert len(chunks) == 1
    assert chunks[0].text == text
    assert (chunks[0].char_start, chunks[0].char_end) == (0, len(text))


def test_two_small_paragraphs_pack_into_one(small_chunks):
    text = "First short para.\n\nSecond short para."
    chunks = chunk_text(text)
    assert len(chunks) == 1
    assert "First short para." in chunks[0].text
    assert "Second short para." in chunks[0].text


# ------------------------------------------------- sentence-aware long split ---

def test_long_paragraph_breaks_on_sentence_boundaries(small_chunks):
    chunks = chunk_text(SENTENCES)
    assert len(chunks) >= 2
    _spans_cover_text(chunks, len(SENTENCES))
    # Every chunk but the last should end at a sentence terminator, not mid-word.
    for c in chunks[:-1]:
        assert c.text.rstrip()[-1] in ".!?", f"chunk did not end on a sentence: {c.text!r}"
    # No chunk grossly exceeds the target (allow a small tail-merge slack).
    for c in chunks:
        assert c.char_end - c.char_start <= settings.chunk_target_chars + settings.chunk_min_chars


def test_no_midword_cut_when_sentence_break_available(small_chunks):
    chunks = chunk_text(SENTENCES)
    # A mid-word cut would leave a chunk ending in a letter with the next chunk
    # beginning in the middle of that same word. Sentence breaks avoid this.
    for c in chunks[:-1]:
        assert not c.text.rstrip()[-1].isalnum()


# --------------------------------------------------------------- edge cases ---

def test_unbroken_token_still_splits_and_covers(small_chunks):
    # No whitespace or sentence ends anywhere → falls back to hard windowing,
    # but must still make progress, stay within target, and cover the whole text.
    text = "x" * 500
    chunks = chunk_text(text)
    assert len(chunks) >= 3
    _spans_cover_text(chunks, len(text))
    for c in chunks:
        assert c.char_end - c.char_start <= settings.chunk_target_chars + settings.chunk_min_chars


def test_offsets_map_back_to_source(small_chunks):
    chunks = chunk_text(SENTENCES)
    for c in chunks:
        # The (whitespace-collapsed) chunk text is derived from its span.
        raw = SENTENCES[c.char_start:c.char_end]
        assert c.text == " ".join(raw.split())
