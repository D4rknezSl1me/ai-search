"""Hybrid retrieval: lexical ∪ vector → RRF fusion → cross-encoder rerank →
context assembly (docs/07-RAG-SEARCH.md §3–6).

Recall-first (CLAUDE.md north star): cast a wide net with both BM25 and ANN,
union the candidates, then let fusion + reranking sort out precision. Every
stage degrades gracefully — a downed Qdrant yields lexical-only results rather
than an error.
"""

from __future__ import annotations

import logging
from dataclasses import dataclass, field
from typing import Any

from . import clients
from .config import settings

log = logging.getLogger("retrieval")


@dataclass
class Filters:
    date_from: str | None = None
    date_to: str | None = None
    source_types: list[str] = field(default_factory=list)
    languages: list[str] = field(default_factory=list)
    domains_include: list[str] = field(default_factory=list)
    domains_exclude: list[str] = field(default_factory=list)


@dataclass
class Candidate:
    chunk_id: str
    document_id: int
    url: str
    domain: str | None
    title: str | None
    text: str | None
    published_at: str | None
    source_type: str | None
    authority: float
    lexical_rank: int | None = None
    vector_rank: int | None = None
    fused_score: float = 0.0
    rerank_score: float | None = None

    @property
    def score(self) -> float:
        return self.rerank_score if self.rerank_score is not None else self.fused_score


# ------------------------------------------------------------- filter builders ---

def _os_filters(f: Filters) -> tuple[list[dict], list[dict]]:
    must, must_not = [], []
    if f.languages:
        must.append({"terms": {"lang": f.languages}})
    if f.source_types:
        must.append({"terms": {"source_type": f.source_types}})
    if f.domains_include:
        must.append({"terms": {"domain": f.domains_include}})
    if f.domains_exclude:
        must_not.append({"terms": {"domain": f.domains_exclude}})
    if f.date_from or f.date_to:
        rng: dict[str, str] = {}
        if f.date_from:
            rng["gte"] = f.date_from
        if f.date_to:
            rng["lte"] = f.date_to
        must.append({"range": {"published_at": rng}})
    return must, must_not


def _qdrant_filter(f: Filters) -> dict[str, Any] | None:
    must, must_not = [], []
    if f.languages:
        must.append({"key": "lang", "match": {"any": f.languages}})
    if f.source_types:
        must.append({"key": "source_type", "match": {"any": f.source_types}})
    if f.domains_include:
        must.append({"key": "domain", "match": {"any": f.domains_include}})
    if f.domains_exclude:
        must_not.append({"key": "domain", "match": {"any": f.domains_exclude}})
    if f.date_from or f.date_to:
        rng: dict[str, str] = {}
        if f.date_from:
            rng["gte"] = f.date_from
        if f.date_to:
            rng["lte"] = f.date_to
        must.append({"key": "published_at", "range": rng})
    out: dict[str, Any] = {}
    if must:
        out["must"] = must
    if must_not:
        out["must_not"] = must_not
    return out or None


# ---------------------------------------------------------------- retrievers ---

async def lexical_search(query: str, f: Filters, k: int) -> list[dict]:
    must, must_not = _os_filters(f)
    body = {
        "size": k,
        "query": {
            "bool": {
                "must": [{
                    "multi_match": {
                        "query": query,
                        "fields": ["text", "text.shingles", "title^2"],
                        "type": "best_fields",
                    }
                }],
                "filter": must,
                "must_not": must_not,
            }
        },
        "_source": ["chunk_id", "document_id", "url", "domain", "title", "text",
                    "published_at", "source_type", "authority"],
    }
    try:
        resp = await clients.http().post(
            f"{settings.opensearch_url}/{settings.opensearch_index}/_search", json=body
        )
        resp.raise_for_status()
        return resp.json()["hits"]["hits"]
    except Exception:
        log.exception("lexical search failed")
        return []


async def vector_search(query: str, f: Filters, k: int) -> list[dict]:
    try:
        vecs = await clients.embed([query], is_query=True)
        if not vecs:
            return []
        body: dict[str, Any] = {"vector": vecs[0], "limit": k, "with_payload": True}
        qf = _qdrant_filter(f)
        if qf:
            body["filter"] = qf
        resp = await clients.http().post(
            f"{settings.qdrant_url}/collections/{settings.qdrant_collection}/points/search",
            json=body,
        )
        resp.raise_for_status()
        return resp.json().get("result", [])
    except Exception:
        log.exception("vector search failed (degrading to lexical-only)")
        return []


# --------------------------------------------------------------------- fusion ---

def _rrf(lex: list[dict], vec: list[dict]) -> dict[str, Candidate]:
    k = settings.rrf_k
    cands: dict[str, Candidate] = {}

    def ensure(chunk_id: str, payload: dict) -> Candidate:
        c = cands.get(chunk_id)
        if c is None:
            c = Candidate(
                chunk_id=chunk_id,
                document_id=int(payload.get("document_id", 0)),
                url=payload.get("url", ""),
                domain=payload.get("domain"),
                title=payload.get("title"),
                text=payload.get("text"),
                published_at=payload.get("published_at"),
                source_type=payload.get("source_type"),
                authority=float(payload.get("authority", 0.5) or 0.5),
            )
            cands[chunk_id] = c
        return c

    for rank, hit in enumerate(lex):
        src = hit.get("_source", {})
        cid = src.get("chunk_id") or hit.get("_id")
        c = ensure(cid, src)
        c.lexical_rank = rank
        c.fused_score += 1.0 / (k + rank + 1)

    for rank, hit in enumerate(vec):
        payload = hit.get("payload", {})
        cid = payload.get("chunk_id")
        if not cid:
            continue
        c = ensure(cid, payload)
        c.vector_rank = rank
        c.fused_score += 1.0 / (k + rank + 1)

    return cands


async def _fill_missing_text(cands: list[Candidate]) -> None:
    """Vector-only hits carry no text (it lives in OpenSearch); fetch via mget."""
    missing = [c.chunk_id for c in cands if not c.text]
    if not missing:
        return
    try:
        resp = await clients.http().post(
            f"{settings.opensearch_url}/{settings.opensearch_index}/_mget",
            json={"ids": missing},
        )
        resp.raise_for_status()
        by_id = {d["_id"]: d.get("_source", {}) for d in resp.json()["docs"] if d.get("found")}
        for c in cands:
            if not c.text and c.chunk_id in by_id:
                c.text = by_id[c.chunk_id].get("text")
    except Exception:
        log.exception("mget for missing chunk text failed")


# -------------------------------------------------------------------- rerank ---

async def _rerank(query: str, cands: list[Candidate]) -> bool:
    texts = [c.text or "" for c in cands]
    ranked = await clients.rerank(query, texts)
    if ranked is None:
        return False
    for orig_idx, score in ranked:
        if 0 <= orig_idx < len(cands):
            cands[orig_idx].rerank_score = score
    return True


# ------------------------------------------------------------------ assembly ---

def _assemble(cands: list[Candidate], max_sources: int) -> list[Candidate]:
    """Dedupe identical text, prefer per-domain diversity, then backfill.

    Recall-first (CLAUDE.md north star): diversity avoids one domain dominating,
    but we never return fewer than the budget when relevant chunks remain — a
    sparse or single-domain corpus still fills up to max_sources.
    """
    ordered = sorted(cands, key=lambda c: c.score, reverse=True)
    seen_text: set[str] = set()

    def dedup_key(c: Candidate) -> str:
        return (c.text or "")[:200]

    # Pass 1: diversified pick, honoring the per-domain cap.
    per_domain: dict[str, int] = {}
    out: list[Candidate] = []
    for c in ordered:
        sig = dedup_key(c)
        if sig in seen_text:
            continue
        dom = c.domain or ""
        if per_domain.get(dom, 0) >= settings.max_per_domain:
            continue
        seen_text.add(sig)
        per_domain[dom] = per_domain.get(dom, 0) + 1
        out.append(c)
        if len(out) >= max_sources:
            return out

    # Pass 2: backfill remaining slots ignoring the domain cap (skip dupes).
    for c in ordered:
        if len(out) >= max_sources:
            break
        sig = dedup_key(c)
        if sig in seen_text:
            continue
        seen_text.add(sig)
        out.append(c)
    return out


@dataclass
class RetrievalResult:
    candidates: list[Candidate]
    n_candidates: int
    reranked: bool
    vector_ok: bool


async def retrieve(query: str, f: Filters, max_sources: int | None = None) -> RetrievalResult:
    max_sources = max_sources or settings.max_sources
    lex = await lexical_search(query, f, settings.retrieve_k_lexical)
    vec = await vector_search(query, f, settings.retrieve_k_vector)

    fused = _rrf(lex, vec)
    n_candidates = len(fused)
    shortlist = sorted(fused.values(), key=lambda c: c.fused_score, reverse=True)
    shortlist = shortlist[: settings.rerank_candidates]

    await _fill_missing_text(shortlist)
    reranked = await _rerank(query, shortlist)

    final = _assemble(shortlist, max_sources)
    return RetrievalResult(
        candidates=final,
        n_candidates=n_candidates,
        reranked=reranked,
        vector_ok=bool(vec),
    )
