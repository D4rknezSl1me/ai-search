"""Resolution eval for agentic entity discovery (docs/15 §4.4; docs/00 §5).

Retrieval/generation quality is measured by `run_eval.py` against the live stack.
This complements it with the metric Phase 6 actually cares about: given a solvable
**target brief**, does the loop resolve the *right* person — not just retrieve
relevant docs? It runs the real discovery loop (`discover_entity` + the
retrieval-backed adapter) over small **labeled synthetic corpora**, so it needs no
GPU/index/network and runs in CI:

  resolution_accuracy  — top candidate is the labeled target (precision of the win)
  recall               — the target appears anywhere in the returned candidates
  resolved_rate        — status == "resolved" (cleared the confidence floor)
  avg_hops / fetches   — how much budget the loop spent

The corpus-backed `retrieve` simulates lexical retrieval (token overlap) so the
loop's PLAN→ACT→OBSERVE→REFINE is exercised end-to-end. Cases live in
`resolution_cases.jsonl`; each has a brief, a corpus (target + distractors +
noise), and the expected target document URL.
"""

from __future__ import annotations

import argparse
import asyncio
import json
import sys
from dataclasses import dataclass
from pathlib import Path

# Allow running as a standalone script (python ai/eval/resolution_eval.py): put
# the ai/ package root on the path so `app...` imports resolve like under pytest.
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from app.entity_brief import TargetBrief  # noqa: E402
from app.entity_orchestrator import discover_entity
from app.entity_resolve import _norm, _tokens
from app.entity_search import make_retrieval_search


@dataclass
class _Doc:
    text: str = ""
    url: str = ""
    title: str = ""


def _norm_url(url: str) -> str:
    return (url or "").rstrip("/").lower()


def make_corpus_retrieve(corpus: list[dict], *, k: int = 10):
    """A fake retrieval over a fixed corpus: rank docs by query/doc token overlap."""
    docs = [_Doc(text=d.get("text", ""), url=d.get("url", ""), title=d.get("title", ""))
            for d in corpus]

    async def retrieve(query: str):
        q = {t for t in _tokens(query) if len(t) > 2}
        scored: list[tuple[int, _Doc]] = []
        for d in docs:
            overlap = len(q & set(_tokens(f"{d.title} {d.text}")))
            if overlap:
                scored.append((overlap, d))
        scored.sort(key=lambda x: -x[0])
        return [d for _, d in scored[:k]]

    return retrieve


async def _run_case(case: dict) -> dict:
    brief = TargetBrief.from_dict(case["brief"])
    retrieve = make_corpus_retrieve(case["corpus"])
    search = make_retrieval_search(brief, retrieve)
    res = await discover_entity(brief, search)

    expected = _norm_url(case["expect"]["source_url"])
    best_url = _norm_url(res.best.candidate.source_url) if res.best else ""
    urls = {_norm_url(sc.candidate.source_url) for sc in res.candidates}
    return {
        "name": case.get("name", ""),
        "status": res.status,
        "hit": best_url == expected,
        "recall": expected in urls,
        "hops": res.hops,
        "fetches": res.fetches,
        "best_score": round(res.best.score, 3) if res.best else 0.0,
        "best_url": best_url,
    }


async def _run_eval_async(cases: list[dict]) -> dict:
    rows = [await _run_case(c) for c in cases]
    n = len(cases) or 1
    summary = {
        "cases": len(cases),
        "resolution_accuracy": round(sum(r["hit"] for r in rows) / n, 3),
        "recall": round(sum(r["recall"] for r in rows) / n, 3),
        "resolved_rate": round(sum(r["status"] == "resolved" for r in rows) / n, 3),
        "avg_hops": round(sum(r["hops"] for r in rows) / n, 2),
        "avg_fetches": round(sum(r["fetches"] for r in rows) / n, 2),
    }
    return {"summary": summary, "per_case": rows}


def run_eval(cases: list[dict]) -> dict:
    """Synchronous entry point (used by tests and the CLI)."""
    return asyncio.run(_run_eval_async(cases))


def load_cases(path: Path) -> list[dict]:
    return [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines()
            if line.strip()]


def default_cases_path() -> Path:
    return Path(__file__).with_name("resolution_cases.jsonl")


def main() -> int:
    ap = argparse.ArgumentParser(description="Offline resolution eval for entity discovery")
    ap.add_argument("--cases", default=str(default_cases_path()))
    args = ap.parse_args()

    cases = load_cases(Path(args.cases))
    report = run_eval(cases)
    print(f"Resolution eval - {len(cases)} labeled targets\n")
    for r in report["per_case"]:
        mark = "[hit] " if r["hit"] else "[MISS]"
        print(f"  {mark} {r['name']:<28} status={r['status']:<10} "
              f"score={r['best_score']:<5} hops={r['hops']} fetches={r['fetches']}")
    print(f"\n  SUMMARY: {report['summary']}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
