"""Offline unit tests for the TTL/LRU cache and the retrieval cache key."""

from __future__ import annotations

from app.cache import TTLCache
from app.retrieval import Filters, _cache_key


class FakeClock:
    def __init__(self) -> None:
        self.t = 1000.0

    def __call__(self) -> float:
        return self.t

    def advance(self, dt: float) -> None:
        self.t += dt


# ------------------------------------------------------------------ TTLCache ---

def test_get_miss_then_hit():
    c = TTLCache(maxsize=8, ttl=60, clock=FakeClock())
    assert c.get("k") is None
    c.set("k", 42)
    assert c.get("k") == 42


def test_entry_expires_after_ttl():
    clock = FakeClock()
    c = TTLCache(maxsize=8, ttl=60, clock=clock)
    c.set("k", "v")
    clock.advance(59)
    assert c.get("k") == "v"      # still fresh
    clock.advance(1)              # now at ttl boundary
    assert c.get("k") is None     # expired
    assert len(c) == 0            # and dropped


def test_lru_eviction_beyond_maxsize():
    c = TTLCache(maxsize=2, ttl=60, clock=FakeClock())
    c.set("a", 1)
    c.set("b", 2)
    c.get("a")           # touch a → b becomes least-recently-used
    c.set("c", 3)        # evicts b
    assert c.get("a") == 1
    assert c.get("c") == 3
    assert c.get("b") is None


def test_set_refreshes_expiry_and_value():
    clock = FakeClock()
    c = TTLCache(maxsize=8, ttl=10, clock=clock)
    c.set("k", "old")
    clock.advance(9)
    c.set("k", "new")            # re-set resets the TTL window
    clock.advance(9)
    assert c.get("k") == "new"   # would have expired under the original set


def test_zero_ttl_disables_storage():
    c = TTLCache(maxsize=8, ttl=0, clock=FakeClock())
    c.set("k", "v")
    assert c.get("k") is None
    assert len(c) == 0


def test_expired_entries_purged_on_set():
    clock = FakeClock()
    c = TTLCache(maxsize=8, ttl=10, clock=clock)
    c.set("a", 1)
    clock.advance(11)            # a is now stale
    c.set("b", 2)               # set() purges expired entries
    assert len(c) == 1
    assert c.get("a") is None
    assert c.get("b") == 2


# ----------------------------------------------------------------- cache key ---

def test_cache_key_stable_for_equivalent_inputs():
    f1 = Filters(languages=["en", "de"], domains_include=["a.com"])
    f2 = Filters(languages=["de", "en"], domains_include=["a.com"])  # order differs
    k1 = _cache_key("Hello World", f1, 12, True, "auto")
    k2 = _cache_key("  hello world ", f2, 12, True, "auto")          # case/space differ
    assert k1 == k2  # normalized query + order-insensitive filters


def test_cache_key_varies_with_each_param():
    f = Filters()
    base = _cache_key("q", f, 12, True, "auto")
    assert _cache_key("q2", f, 12, True, "auto") != base
    assert _cache_key("q", f, 10, True, "auto") != base
    assert _cache_key("q", f, 12, False, "auto") != base
    assert _cache_key("q", f, 12, True, "any") != base
    assert _cache_key("q", Filters(languages=["en"]), 12, True, "auto") != base
