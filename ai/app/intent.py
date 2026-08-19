"""Query intent classification (docs/07-RAG-SEARCH.md §2).

A lightweight, rule-based classifier (no model, no network) that labels a query
so downstream stages can adapt. Today it drives one concrete behavior: a
**news/recency** intent upgrades `freshness="auto"` to `"fresh"`, so "latest
developments in X" gets recency weighting without the user having to ask. The
other labels are surfaced for observability and future hooks (filter derivation,
per-intent k/rerank tuning) — kept intentionally simple and dependency-free.

Recall-first (CLAUDE.md north star): intent only *nudges* ranking (never filters
or drops results), and an explicit user `freshness` always wins over the inferred
one — classification can only help, never silently exclude content.
"""

from __future__ import annotations

import re
from datetime import datetime, timezone
from enum import Enum


class Intent(str, Enum):
    NEWS_FRESH = "news_fresh"          # wants the latest / time-sensitive
    BROAD_RESEARCH = "broad_research"  # "everything about X" — max breadth
    ENTITY_LOOKUP = "entity_lookup"    # a person/org/thing by name
    NAVIGATIONAL = "navigational"      # a specific site/page
    FACTUAL = "factual"                # default: a specific answer


_URL = re.compile(r"https?://|www\.|\b[\w-]+\.(com|org|net|io|gov|edu)\b", re.I)
_NAV = re.compile(
    r"\b(official\s+(site|website|page)|home\s?page|log\s?in|sign\s?in|download|"
    r"documentation|docs)\b",
    re.I,
)
_NEWS = re.compile(
    r"\b(latest|newest|recent(ly)?|breaking|today|tonight|yesterday|current(ly)?|"
    r"news|updates?|developments?|so\s+far|right\s+now|as\s+of|this\s+(week|month|year)|"
    r"past\s+(day|week|month|year)|in\s+the\s+news|trend(ing|s)?)\b",
    re.I,
)
_BROAD = re.compile(
    r"\b(everything|every\s+(thing|detail)|all\s+(about|information|details|known)|"
    r"comprehensive|exhaustive|complete\s+(list|history|picture)|overview|deep\s+dive|"
    r"find\s+all|list\s+all|dig\s+up)\b",
    re.I,
)
_ENTITY_PREFIX = re.compile(r"^\s*(who|whose|whom)\b", re.I)
_WORD = re.compile(r"\w+", re.UNICODE)


def _mentions_recent_year(query: str, now: datetime) -> bool:
    """A current/next-year mention ('in 2026') reads as a recency signal."""
    years = {now.year, now.year + 1}
    return any(str(y) in query for y in years)


def _looks_like_entity(query: str) -> bool:
    """Short query dominated by Capitalized tokens → likely a name/entity."""
    tokens = query.split()
    if not (1 <= len(tokens) <= 4):
        return False
    caps = sum(1 for t in tokens if t[:1].isupper())
    return caps >= max(1, len(tokens) - 1)


def classify(query: str, *, now: datetime | None = None) -> Intent:
    """Best-effort single-label intent. Precedence is deliberate: a navigational
    target wins first, then recency (it has the concrete freshness hook), then
    breadth, then a named-entity lookup, else a plain factual question.
    """
    q = (query or "").strip()
    if not q:
        return Intent.FACTUAL
    now = now or datetime.now(timezone.utc)

    if _URL.search(q) or _NAV.search(q):
        return Intent.NAVIGATIONAL
    if _NEWS.search(q) or _mentions_recent_year(q, now):
        return Intent.NEWS_FRESH
    if _BROAD.search(q):
        return Intent.BROAD_RESEARCH
    if _ENTITY_PREFIX.search(q) or _looks_like_entity(q):
        return Intent.ENTITY_LOOKUP
    return Intent.FACTUAL


def resolve_freshness(requested: str, intent: Intent) -> str:
    """Map an intent onto a freshness mode, honoring an explicit user choice.

    Only the default ("auto") is inferred: a news/recency intent upgrades it to
    "fresh". An explicit "fresh"/"any" from the caller is always respected.
    """
    if requested != "auto":
        return requested
    return "fresh" if intent is Intent.NEWS_FRESH else "auto"
