"""Structure-aware text chunking (docs/05-EXTRACTION.md §7).

The crawler stores clean text (readability output) as a flat blob, so we chunk
on paragraph boundaries, packing paragraphs up to a target size with a small
overlap for context continuity. Char offsets are retained so a chunk can be
traced back to its position in the source document.
"""

from __future__ import annotations

import re
from dataclasses import dataclass

from .config import settings

_PARA = re.compile(r"\n\s*\n+")
_WS = re.compile(r"[ \t]+")


@dataclass
class Chunk:
    index: int
    text: str
    char_start: int
    char_end: int


def _clean(p: str) -> str:
    return _WS.sub(" ", p.strip())


def chunk_text(text: str) -> list[Chunk]:
    """Split into overlapping, roughly target-sized chunks on paragraph breaks.

    A very long paragraph is hard-split so no chunk grossly exceeds the target.
    Offsets index into the original (untrimmed) text.
    """
    target = settings.chunk_target_chars
    overlap = settings.chunk_overlap_chars
    min_chars = settings.chunk_min_chars

    if not text or not text.strip():
        return []

    # Paragraph spans with their offsets in the original text.
    spans: list[tuple[int, int]] = []
    pos = 0
    for part in _PARA.split(text):
        start = text.find(part, pos) if part else pos
        if start < 0:
            start = pos
        end = start + len(part)
        pos = end
        if part.strip():
            spans.append((start, end))

    # Hard-split any paragraph longer than the target into windowed pieces.
    pieces: list[tuple[int, int]] = []
    for s, e in spans:
        if e - s <= target:
            pieces.append((s, e))
            continue
        i = s
        while i < e:
            j = min(i + target, e)
            pieces.append((i, j))
            i = j - overlap if j - overlap > i else j

    # Pack pieces into chunks up to the target, carrying overlap between them.
    chunks: list[Chunk] = []
    cur_start: int | None = None
    cur_end = 0
    idx = 0
    for s, e in pieces:
        if cur_start is None:
            cur_start, cur_end = s, e
            continue
        if e - cur_start <= target:
            cur_end = e
        else:
            chunks.append(_mk(idx, text, cur_start, cur_end))
            idx += 1
            # Start the next chunk a little before the previous end for overlap.
            back = max(cur_start, cur_end - overlap)
            cur_start, cur_end = min(back, s), e
    if cur_start is not None and cur_end - cur_start >= min(min_chars, 1):
        # Keep a final short chunk only if it's the sole chunk; otherwise merge
        # tiny tails into the previous chunk to avoid orphan crumbs.
        if chunks and (cur_end - cur_start) < min_chars:
            prev = chunks[-1]
            chunks[-1] = _mk(prev.index, text, prev.char_start, cur_end)
        else:
            chunks.append(_mk(idx, text, cur_start, cur_end))

    return chunks


def _mk(index: int, text: str, start: int, end: int) -> Chunk:
    return Chunk(index=index, text=_clean(text[start:end]), char_start=start, char_end=end)
