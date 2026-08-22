"""Runs the offline resolution eval over the shipped labeled cases and gates on
the metrics — so a regression in extraction/resolution/planning is caught here
without needing the live stack (docs/15 §4.4)."""

from __future__ import annotations

import sys
from pathlib import Path

# The eval module lives in ai/eval (not on the app path); add it for import.
sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "eval"))

from resolution_eval import default_cases_path, load_cases, run_eval  # noqa: E402


def test_labeled_targets_resolve():
    cases = load_cases(default_cases_path())
    assert len(cases) >= 5
    report = run_eval(cases)
    s = report["summary"]

    # Every labeled target is solvable from its corpus → the loop must find it.
    assert s["recall"] == 1.0, report
    # And the *right* candidate must win each time (precision of the resolution).
    assert s["resolution_accuracy"] == 1.0, report
    # All these cases are designed to clear the confidence floor.
    assert s["resolved_rate"] == 1.0, report
    # Bounded: the loop shouldn't burn many hops on solvable targets.
    assert s["avg_hops"] <= 3, report


def test_each_case_hits():
    report = run_eval(load_cases(default_cases_path()))
    for row in report["per_case"]:
        assert row["hit"], f"missed target for case {row['name']}: {row}"
        assert row["best_score"] >= 0.6, row
