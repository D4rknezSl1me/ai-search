"""Offline unit tests for query date-filter derivation (docs/07 §2)."""

from __future__ import annotations

from datetime import datetime, timezone

from app.filters import DateRange, derive_date_range

NOW = datetime(2026, 8, 22, tzinfo=timezone.utc)


def d(q):
    return derive_date_range(q, now=NOW)


# ---------------------------------------------------------------- explicit ---

def test_between_two_years():
    assert d("papers between 2010 and 2015") == DateRange("2010-01-01", "2015-12-31")


def test_between_normalizes_order():
    assert d("stuff between 2015 and 2010") == DateRange("2010-01-01", "2015-12-31")


def test_since_year_is_lower_bound_only():
    assert d("developments since 2019") == DateRange(date_from="2019-01-01")
    assert d("news from 2020 onward") == DateRange(date_from="2020-01-01")


def test_before_year_is_upper_bound_only():
    assert d("events before 2000") == DateRange(date_to="2000-01-01")
    assert d("prior to 1990") == DateRange(date_to="1990-01-01")


def test_in_year_is_full_year_range():
    assert d("what happened in 2021") == DateRange("2021-01-01", "2021-12-31")
    assert d("olympics of 2016") == DateRange("2016-01-01", "2016-12-31")


# ----------------------------------------------------------------- relative ---

def test_last_n_units():
    assert d("news last 3 months") == DateRange("2026-05-24", "2026-08-22")
    assert d("past 2 weeks") == DateRange("2026-08-08", "2026-08-22")
    assert d("last 1 day") == DateRange("2026-08-21", "2026-08-22")


def test_last_single_unit():
    assert d("last week") == DateRange("2026-08-15", "2026-08-22")
    assert d("in the past month") == DateRange("2026-07-23", "2026-08-22")


def test_today_and_yesterday():
    assert d("headlines today") == DateRange("2026-08-22", "2026-08-22")
    assert d("what happened yesterday") == DateRange("2026-08-21", "2026-08-21")


# ------------------------------------------------------------- precedence ---

def test_between_beats_single_year():
    # A two-sided range wins over an incidental single-year mention.
    assert d("between 2010 and 2015, not 2020") == DateRange("2010-01-01", "2015-12-31")


def test_since_beats_in_year():
    assert d("since 2019 in tech") == DateRange(date_from="2019-01-01")


# --------------------------------------------------- no false positives ---

def test_non_temporal_numbers_ignored():
    assert d("top 100 songs") is None
    assert d("the 1500 members of the club") is None   # not a 19xx/20xx year cue
    assert d("model 3 review") is None


def test_no_temporal_phrase():
    assert d("who is ada lovelace") is None
    assert d("") is None


def test_daterange_truthiness():
    assert not DateRange()
    assert DateRange(date_from="2020-01-01")
