"""Near-duplicate detection for query-time result assembly (docs/07 §6).

The final source budget is small (max_sources), so every slot spent on a chunk
that merely re-states one already chosen is a wasted slot — redundant citations
and less distinct information reaching synthesis. Exact-prefix matching (the old
approach) misses the common cases: the same passage re-crawled with different
whitespace/case, or a chunk that overlaps another by ~95%.

This collapses those by comparing word-shingle sets with Jaccard similarity.
Recall-first (CLAUDE.md north star): the threshold is deliberately high (only
near-identical passages collapse), short snippets fall back to exact match so
distinct-but-brief facts are never dropped, and assembly only ever *replaces* a
duplicate with the next candidate — it never returns fewer results.

Pure and self-contained (stdlib only) → fully unit-testable offline.
"""

from __future__ import annotations

import re

_WS = re.compile(r"\s+")
_WORD = re.compile(r"\w+", re.UNICODE)


def normalize(text: str | None) -> str:
    """Lowercase + collapse whitespace — cheap canonical form for exact match."""
    return _WS.sub(" ", (text or "").strip().lower())


def shingles(text: str, k: int) -> frozenset[str]:
    """Word k-shingles of the normalized text.

    Texts shorter than k words fall back to a single whole-string shingle, so
    brief snippets only match on (near-)exact content rather than on incidental
    shared words — avoids over-collapsing distinct short facts.
    """
    tokens = _WORD.findall(normalize(text))
    if not tokens:
        return frozenset()
    if len(tokens) < k:
        return frozenset({" ".join(tokens)})
    return frozenset(" ".join(tokens[i : i + k]) for i in range(len(tokens) - k + 1))


def jaccard(a: frozenset[str], b: frozenset[str]) -> float:
    if not a and not b:
        return 1.0
    if not a or not b:
        return 0.0
    inter = len(a & b)
    if inter == 0:
        return 0.0
    return inter / len(a | b)


class DedupIndex:
    """Accumulates kept texts and reports whether a new one duplicates any.

    Query (`is_duplicate`) and commit (`add`) are separate so a caller can test a
    candidate, decide for other reasons not to keep it (e.g. a domain cap), and
    reconsider it later without it having polluted the index.
    """

    def __init__(self, k: int = 5, threshold: float = 0.8) -> None:
        self.k = k
        self.threshold = threshold
        self._exact: set[str] = set()
        self._shingles: list[frozenset[str]] = []

    def is_duplicate(self, text: str | None) -> bool:
        norm = normalize(text)
        if norm in self._exact:
            return True
        sh = shingles(text or "", self.k)
        if not sh:
            return False
        for prev in self._shingles:
            if jaccard(sh, prev) >= self.threshold:
                return True
        return False

    def add(self, text: str | None) -> None:
        self._exact.add(normalize(text))
        sh = shingles(text or "", self.k)
        if sh:
            self._shingles.append(sh)
