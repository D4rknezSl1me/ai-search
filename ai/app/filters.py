"""Filter derivation from the query text (docs/07-RAG-SEARCH.md §2).

Infer an explicit **date range** from temporal phrases in the question ("news last
week", "papers since 2019", "in 2021", "between 2010 and 2015") and turn it into
`date_from` / `date_to` bounds retrieval can apply. This complements freshness
*weighting* (which nudges recent docs up): a hard range is the only way to honor
an explicit "in 2019" or "before 2010".

Recall-first (CLAUDE.md north star): derivation fires **only on explicit temporal
cues**, is applied **only when the caller supplied no date filter of its own**
(an explicit API filter always wins), and years are constrained to a plausible
range so "top 100" or "the 1500 members" never read as a date. Pure and
dependency-free — unit-testable offline with an injected `now`.
"""

from __future__ import annotations

import re
from dataclasses import dataclass
from datetime import datetime, timedelta, timezone

_YEAR = r"(19\d{2}|20\d{2})"           # a plausible 4-digit year only
_UNIT_DAYS = {"day": 1, "week": 7, "month": 30, "year": 365}

# Ordered most-specific-first; the first pattern that matches wins.
_BETWEEN = re.compile(rf"\bbetween\s+{_YEAR}\s+and\s+{_YEAR}\b", re.I)
_SINCE = re.compile(rf"\b(?:since|after|from)\s+{_YEAR}\b", re.I)
_BEFORE = re.compile(rf"\b(?:before|until|up\s+to|prior\s+to)\s+{_YEAR}\b", re.I)
_IN_YEAR = re.compile(rf"\b(?:in|during|of)\s+{_YEAR}\b", re.I)
_LAST_N = re.compile(r"\b(?:last|past|previous)\s+(\d{1,3})\s+(day|week|month|year)s?\b", re.I)
_LAST_ONE = re.compile(r"\b(?:last|past|previous)\s+(day|week|month|year)\b", re.I)
_TODAY = re.compile(r"\btoday\b", re.I)
_YESTERDAY = re.compile(r"\byesterday\b", re.I)


@dataclass(frozen=True)
class DateRange:
    date_from: str | None = None    # inclusive lower bound (YYYY-MM-DD) or None
    date_to: str | None = None      # inclusive upper bound (YYYY-MM-DD) or None

    def __bool__(self) -> bool:
        return self.date_from is not None or self.date_to is not None


def _d(dt: datetime) -> str:
    return dt.date().isoformat()


def derive_date_range(query: str, *, now: datetime | None = None) -> DateRange | None:
    """Infer a date range from an explicit temporal phrase, else None.

    Precedence is deliberate: an explicit two-sided range ("between … and …")
    beats a one-sided bound ("since"/"before"), which beats a single year ("in
    2021"), which beats relative windows ("last 3 months", "today").
    """
    q = query or ""
    now = now or datetime.now(timezone.utc)

    m = _BETWEEN.search(q)
    if m:
        y1, y2 = sorted((int(m.group(1)), int(m.group(2))))
        return DateRange(f"{y1}-01-01", f"{y2}-12-31")

    m = _SINCE.search(q)
    if m:
        return DateRange(date_from=f"{int(m.group(1))}-01-01")

    m = _BEFORE.search(q)
    if m:
        return DateRange(date_to=f"{int(m.group(1))}-01-01")

    m = _IN_YEAR.search(q)
    if m:
        y = int(m.group(1))
        return DateRange(f"{y}-01-01", f"{y}-12-31")

    m = _LAST_N.search(q)
    if m:
        n, unit = int(m.group(1)), m.group(2).lower()
        if n > 0:
            return DateRange(date_from=_d(now - timedelta(days=n * _UNIT_DAYS[unit])), date_to=_d(now))

    m = _LAST_ONE.search(q)
    if m:
        unit = m.group(1).lower()
        return DateRange(date_from=_d(now - timedelta(days=_UNIT_DAYS[unit])), date_to=_d(now))

    if _TODAY.search(q):
        return DateRange(_d(now), _d(now))

    if _YESTERDAY.search(q):
        y = now - timedelta(days=1)
        return DateRange(_d(y), _d(y))

    return None
