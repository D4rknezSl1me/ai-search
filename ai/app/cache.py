"""A tiny in-process TTL + LRU cache (docs/07-RAG-SEARCH.md, caching).

Used to memoize retrieval results for identical queries over a short window.
On the owner's single-GPU box every cache hit avoids a burst of embed + ANN +
lexical + cross-encoder (and, for search, LLM) work, so repeated or dashboard
queries stay cheap. Deliberately conservative for a recall-first product: the
TTL is short and the caller bypasses the cache whenever ranking is
freshness-driven, so freshly-crawled content is never hidden behind a stale hit.

Pure stdlib with an injectable clock, so expiry/eviction are unit-testable
without sleeping.
"""

from __future__ import annotations

import time
from collections import OrderedDict
from typing import Any, Callable


class TTLCache:
    """Fixed-size cache where entries expire after `ttl` seconds (LRU eviction)."""

    def __init__(self, maxsize: int, ttl: float, clock: Callable[[], float] = time.monotonic) -> None:
        self.maxsize = max(1, maxsize)
        self.ttl = ttl
        self._clock = clock
        self._data: "OrderedDict[str, tuple[float, Any]]" = OrderedDict()

    def get(self, key: str) -> Any | None:
        item = self._data.get(key)
        if item is None:
            return None
        expires_at, value = item
        if self._clock() >= expires_at:
            # Lazily drop the expired entry.
            self._data.pop(key, None)
            return None
        self._data.move_to_end(key)  # mark as most-recently-used
        return value

    def set(self, key: str, value: Any) -> None:
        if self.ttl <= 0:
            return
        self._data.pop(key, None)
        self._data[key] = (self._clock() + self.ttl, value)
        self._data.move_to_end(key)
        self._purge_expired()
        while len(self._data) > self.maxsize:
            self._data.popitem(last=False)  # evict least-recently-used

    def _purge_expired(self) -> None:
        now = self._clock()
        expired = [k for k, (exp, _) in self._data.items() if now >= exp]
        for k in expired:
            self._data.pop(k, None)

    def clear(self) -> None:
        self._data.clear()

    def __len__(self) -> int:
        return len(self._data)
