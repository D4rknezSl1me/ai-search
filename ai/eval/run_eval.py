"""Evaluation harness for the ai-search retrieval + generation pipeline.

Runs a labeled query set against the live API and reports:
  Retrieval:  recall@k, MRR, nDCG@k  (are the known-relevant docs retrieved?)
  Generation: citation accuracy, groundedness (optional LLM-as-judge)

This is how quality is *proven* rather than asserted (docs/07-RAG-SEARCH.md §10);
run it after any change to the chunker, models, fusion, or prompt to catch
regressions. All local — no paid judges (CLAUDE.md rule 2); the judge is the
same local Ollama model.

Usage (from repo root, against the running stack):
  docker run --rm --network ai-search_default -v "$PWD/ai/eval:/eval" -w /eval \
    -e AISEARCH_API=http://ai-api:8000 python:3.11-slim \
    sh -c "pip install -q httpx && python run_eval.py --judge"
"""

from __future__ import annotations

import argparse
import json
import math
import os
import sys
from pathlib import Path

import httpx

API = os.environ.get("AISEARCH_API", "http://ai-api:8000")
LLM_URL = os.environ.get("LLM_URL", "http://llm:11434")
LLM_MODEL = os.environ.get("LLM_MODEL", "llama3.1:8b")
K_VALUES = (5, 10)


def norm(url: str) -> str:
    return url.rstrip("/").lower()


def load_queries(path: Path) -> list[dict]:
    return [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines() if line.strip()]


def retrieved_urls(client: httpx.Client, query: str, k: int) -> list[str]:
    """Ranked, de-duplicated document URLs for a query (best rank per URL)."""
    resp = client.post(f"{API}/v1/retrieve", json={"query": query, "max_sources": k})
    resp.raise_for_status()
    seen: list[str] = []
    for r in resp.json()["results"]:
        u = norm(r["url"])
        if u not in seen:
            seen.append(u)
    return seen


def dcg(rels: list[int]) -> float:
    return sum(rel / math.log2(i + 2) for i, rel in enumerate(rels))


def eval_retrieval(client: httpx.Client, queries: list[dict]) -> dict:
    kmax = max(K_VALUES)
    agg = {f"recall@{k}": 0.0 for k in K_VALUES}
    agg[f"ndcg@{kmax}"] = 0.0
    agg["mrr"] = 0.0
    per_query = []

    for q in queries:
        relevant = {norm(u) for u in q["relevant_urls"]}
        ranked = retrieved_urls(client, q["query"], kmax)

        rr = 0.0
        for i, u in enumerate(ranked):
            if u in relevant:
                rr = 1.0 / (i + 1)
                break

        rels = [1 if u in relevant else 0 for u in ranked]
        ideal = sorted(rels, reverse=True)
        ndcg = (dcg(rels) / dcg(ideal)) if any(ideal) else 0.0

        row = {"query": q["query"], "mrr": round(rr, 3), f"ndcg@{kmax}": round(ndcg, 3)}
        for k in K_VALUES:
            found = sum(1 for u in ranked[:k] if u in relevant)
            recall = found / len(relevant) if relevant else 0.0
            row[f"recall@{k}"] = round(recall, 3)
            agg[f"recall@{k}"] += recall
        agg["mrr"] += rr
        agg[f"ndcg@{kmax}"] += ndcg
        per_query.append(row)

    n = len(queries)
    means = {m: round(v / n, 3) for m, v in agg.items()}
    return {"means": means, "per_query": per_query}


def eval_generation(client: httpx.Client, queries: list[dict], judge: bool) -> dict:
    cited_ok = 0
    answered = 0
    grounded_scores: list[float] = []
    per_query = []

    for q in queries:
        resp = client.post(
            f"{API}/v1/search",
            json={"query": q["query"], "options": {"stream": False, "max_sources": 6}},
            timeout=300.0,
        )
        resp.raise_for_status()
        d = resp.json()
        if d.get("mode") != "synthesize":
            per_query.append({"query": q["query"], "mode": d.get("mode"), "reason": d.get("reason")})
            continue
        answer = d.get("answer") or ""
        citations = d.get("citations", [])
        answered += 1
        # Citation accuracy: every inline [n] resolves to a returned citation.
        import re
        cited_ns = {int(m) for m in re.findall(r"\[(\d+)\]", answer)}
        valid_ns = {c["n"] for c in citations}
        acc = 1.0 if cited_ns and cited_ns <= valid_ns else (0.0 if cited_ns else 1.0)
        cited_ok += acc
        row = {"query": q["query"], "citation_ok": acc, "n_citations": len(citations),
               "confidence": d.get("confidence")}
        if judge:
            g = judge_groundedness(client, q["query"], answer, citations)
            grounded_scores.append(g)
            row["groundedness"] = g
        per_query.append(row)

    out = {
        "answered": answered,
        "citation_accuracy": round(cited_ok / answered, 3) if answered else None,
        "per_query": per_query,
    }
    if judge and grounded_scores:
        out["groundedness"] = round(sum(grounded_scores) / len(grounded_scores), 3)
    return out


def judge_groundedness(client: httpx.Client, query: str, answer: str, citations: list[dict]) -> float:
    """Local LLM-as-judge: is every claim supported by the cited sources? 0..1."""
    sources = "\n".join(f"[{c['n']}] {c.get('snippet','')}" for c in citations)
    prompt = (
        "You are grading whether an ANSWER is fully supported by its SOURCES. "
        "Reply with ONLY a number from 0 to 1 (1 = every claim is supported, "
        "0 = unsupported/hallucinated).\n\n"
        f"QUESTION: {query}\n\nSOURCES:\n{sources}\n\nANSWER:\n{answer}\n\nSCORE:"
    )
    try:
        resp = client.post(
            f"{LLM_URL}/api/generate",
            json={"model": LLM_MODEL, "prompt": prompt, "stream": False},
            timeout=300.0,
        )
        resp.raise_for_status()
        import re
        m = re.search(r"[01](?:\.\d+)?", resp.json().get("response", ""))
        return float(m.group()) if m else 0.0
    except Exception:
        return -1.0  # judge unavailable


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--judge", action="store_true", help="run LLM-as-judge groundedness")
    ap.add_argument("--no-generation", action="store_true", help="retrieval metrics only")
    ap.add_argument("--queries", default=str(Path(__file__).with_name("queries.jsonl")))
    args = ap.parse_args()

    queries = load_queries(Path(args.queries))
    print(f"Evaluating {len(queries)} queries against {API}\n")

    with httpx.Client(timeout=60.0) as client:
        retr = eval_retrieval(client, queries)
        print("== Retrieval ==")
        for row in retr["per_query"]:
            print(f"  {row}")
        print(f"  MEANS: {retr['means']}\n")

        if not args.no_generation:
            gen = eval_generation(client, queries, args.judge)
            print("== Generation ==")
            for row in gen["per_query"]:
                print(f"  {row}")
            print(f"  citation_accuracy={gen['citation_accuracy']} answered={gen['answered']}"
                  + (f" groundedness={gen.get('groundedness')}" if args.judge else ""))

    return 0


if __name__ == "__main__":
    sys.exit(main())
