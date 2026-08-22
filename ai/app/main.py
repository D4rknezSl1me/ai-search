"""ai-search intelligence service (Phase 2 — Search / RAG API).

Chunk → embed (TEI) → index (Qdrant + OpenSearch) → hybrid retrieve → RRF fuse
→ cross-encoder rerank → grounded synthesis (local LLM, cited). Fully self-hosted
(CLAUDE.md rule 2). See docs/07-RAG-SEARCH.md and docs/13-API.md.
"""

from __future__ import annotations

import asyncio
import json
import logging
import time
from contextlib import asynccontextmanager

from pathlib import Path

import httpx
from fastapi import FastAPI
from fastapi.responses import HTMLResponse, JSONResponse, StreamingResponse

from . import auth, clients, indexer, indexes, reconcile
from .config import settings
from .retrieval import Filters, retrieve
from .schemas import (
    DiscoverEntityRequest, RetrieveRequest, RetrieveResponse, ResultItem, SearchRequest,
)
from . import synthesis
from .entity_brief import TargetBrief
from .entity_orchestrator import discover_entity
from .entity_planner import make_llm_planner
from .entity_search import make_search, make_searxng_discover

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(name)s %(levelname)s %(message)s")
log = logging.getLogger("ai-api")

_stop = asyncio.Event()
_indexer_task: asyncio.Task | None = None


@asynccontextmanager
async def lifespan(app: FastAPI):
    global _indexer_task
    try:
        await indexes.ensure_all()
        log.info("indexes ensured (qdrant collection + opensearch index)")
    except Exception:
        log.exception("index setup failed at startup (will retry lazily)")
    _indexer_task = asyncio.create_task(indexer.run_loop(_stop))
    yield
    _stop.set()
    if _indexer_task:
        await asyncio.wait_for(asyncio.shield(_indexer_task), timeout=5)
    await clients.close()


app = FastAPI(title="ai-search AI service", version="0.2.0", lifespan=lifespan)

# API-key auth + per-key rate limiting on /v1/* (off unless AUTH_ENABLED). The
# decision logic lives in app.auth (pure/testable); this is a thin adapter.
_auth_cfg = auth.AuthConfig(enabled=settings.auth_enabled, allowed_keys=auth.parse_keys(settings.api_keys))
_rate_limiter = auth.RateLimiter(settings.rate_limit_per_min)


@app.middleware("http")
async def _auth_middleware(request, call_next):
    if _auth_cfg.enabled:
        key = auth.extract_key(request.headers)
        rejection = auth.decide(request.url.path, key, cfg=_auth_cfg, limiter=_rate_limiter)
        if rejection is not None:
            status, reason = rejection
            return JSONResponse(status_code=status, content={"error": reason})
    return await call_next(request)


def _filters(f) -> Filters:
    return Filters(
        date_from=f.date_from, date_to=f.date_to,
        source_types=f.source_types, languages=f.languages,
        domains_include=f.domains_include, domains_exclude=f.domains_exclude,
    )


# ----------------------------------------------------------------------- UI ---

_UI_HTML = (Path(__file__).parent / "static" / "index.html").read_text(encoding="utf-8")


@app.get("/", response_class=HTMLResponse)
async def ui() -> str:
    """Minimal self-hosted search UI (Phase 5). Streams cited answers from /v1/search."""
    return _UI_HTML


# ------------------------------------------------------------------- health ---

@app.get("/healthz")
async def healthz() -> dict:
    return {"status": "ok", "service": "ai-api"}


async def _check(url: str) -> bool:
    try:
        resp = await clients.http().get(url, timeout=httpx.Timeout(2.0))
        return resp.status_code < 500
    except Exception:
        return False


@app.get("/readyz")
async def readyz() -> JSONResponse:
    checks = {
        "qdrant": f"{settings.qdrant_url}/readyz",
        "opensearch": f"{settings.opensearch_url}/_cluster/health",
        "tei": f"{settings.tei_url}/health",
        "reranker": f"{settings.rerank_url}/health",
        "llm": f"{settings.llm_url}/api/tags",
    }
    results = {name: ("ok" if await _check(url) else "unreachable") for name, url in checks.items()}
    required = {"opensearch"}  # lexical-only is the minimum viable service
    ready = all(results[name] == "ok" for name in required)
    return JSONResponse(
        status_code=200 if ready else 503,
        content={"status": "ready" if ready else "not_ready", "dependencies": results},
    )


@app.get("/metrics")
async def metrics() -> str:
    try:
        pool = await clients.pg()
        pending = await pool.fetchval(
            "SELECT count(*) FROM documents WHERE n_chunks = 0 AND dup_of IS NULL"
        )
        indexed = await pool.fetchval("SELECT count(*) FROM documents WHERE n_chunks > 0")
    except Exception:
        pending = indexed = -1
    return (
        "# ai-search ai-api metrics\n"
        f"aisearch_documents_indexed {indexed}\n"
        f"aisearch_documents_pending {pending}\n"
    )


# --------------------------------------------------------------- index admin ---

@app.post("/internal/reconcile")
async def reconcile_endpoint(document_id: int | None = None) -> dict:
    """Prune orphaned chunks (index ≥ n_chunks) from Qdrant + OpenSearch. Scope to
    one document with ?document_id=…, else scan a batch of indexed documents."""
    if document_id is not None:
        pool = await clients.pg()
        n = await pool.fetchval("SELECT n_chunks FROM documents WHERE id = $1", document_id)
        if n is None:
            return {"error": "unknown document", "document_id": document_id}
        return {"document_id": document_id, **await reconcile.prune_document(document_id, n)}
    return await reconcile.reconcile_all()


@app.post("/internal/reindex")
async def reindex() -> dict:
    """Drain pending documents synchronously (handy for tests/backfills)."""
    total = {"documents": 0, "chunks": 0}
    while True:
        res = await indexer.index_pending()
        total["documents"] += res["documents"]
        total["chunks"] += res["chunks"]
        if res["remaining"] == 0 or res["documents"] == 0:
            break
    return {"indexed": total, "remaining": res["remaining"]}


# ----------------------------------------------------------------- retrieve ---

@app.post("/v1/retrieve", response_model=RetrieveResponse)
async def v1_retrieve(req: RetrieveRequest) -> RetrieveResponse:
    result = await retrieve(
        req.query, _filters(req.filters), max_sources=req.max_sources,
        expand=req.expand, freshness=req.freshness,
    )
    items = [
        ResultItem(
            chunk_id=c.chunk_id, document_id=c.document_id, url=c.url, domain=c.domain,
            title=c.title, text=c.text, published_at=c.published_at,
            source_type=c.source_type, score=round(c.score, 4),
            lexical_rank=c.lexical_rank, vector_rank=c.vector_rank,
        )
        for c in result.candidates
    ]
    domains = {c.domain for c in result.candidates if c.domain}
    return RetrieveResponse(
        query=req.query,
        results=items,
        coverage={"candidates": result.n_candidates, "used": len(items), "domains": len(domains)},
        reranked=result.reranked,
        degraded={"vector": not result.vector_ok},
        expansions=result.plan.expansions,
        intent=result.intent,
        freshness=result.freshness,
    )


# -------------------------------------------------------------------- search ---

@app.post("/v1/search")
async def v1_search(req: SearchRequest):
    start = time.monotonic()
    result = await retrieve(
        req.query, _filters(req.filters),
        max_sources=req.options.max_sources, expand=req.options.expand,
        freshness=req.options.freshness,
    )
    cands = result.candidates

    llm_ready = req.options.synthesize and await clients.llm_available()

    # Degradation: no synthesis requested, or no LLM, or no sources → retrieve-only.
    if not req.options.synthesize or not llm_ready or not cands:
        payload = _retrieve_only_payload(req, result, start, llm_ready)
        if req.options.stream:
            return StreamingResponse(_sse_once(payload), media_type="text/event-stream")
        return JSONResponse(payload)

    if req.options.stream:
        return StreamingResponse(_search_sse(req, result, start), media_type="text/event-stream")
    return JSONResponse(await _search_full(req, result, start))


def _domains(cands) -> int:
    return len({c.domain for c in cands if c.domain})


def _retrieve_only_payload(req, result, start, llm_ready) -> dict:
    cands = result.candidates
    reason = "no_sources" if not cands else ("synthesis_disabled" if not req.options.synthesize else "llm_unavailable")
    return {
        "answer": None,
        "mode": "retrieve",
        "reason": reason,
        "citations": [],
        "results": [
            {"n": i + 1, "url": c.url, "title": c.title, "domain": c.domain,
             "snippet": (c.text or "")[:280], "score": round(c.score, 4),
             "published_at": c.published_at, "source_type": c.source_type}
            for i, c in enumerate(cands)
        ],
        "confidence": 0.0,
        "coverage": {"candidates": result.n_candidates, "used": len(cands), "domains": _domains(cands)},
        "expansions": result.plan.expansions,
        "intent": result.intent,
        "freshness": result.freshness,
        "latency_ms": int((time.monotonic() - start) * 1000),
        "degraded": {"vector": not result.vector_ok, "reranker": not result.reranked, "llm": not llm_ready},
    }


def _citation_dict(c) -> dict:
    return {"n": c.n, "url": c.url, "title": c.title, "published_at": c.published_at,
            "snippet": c.snippet, "score": c.score, "source_type": c.source_type}


async def _search_full(req, result, start) -> dict:
    cands = result.candidates
    answer = "".join([tok async for tok in synthesis.stream_tokens(req.query, cands)])
    citations, used = synthesis.build_citations(answer, cands)
    return {
        "answer": answer,
        "mode": "synthesize",
        "citations": [_citation_dict(c) for c in citations],
        "confidence": synthesis.confidence(cands, used, result.reranked),
        "coverage": {"candidates": result.n_candidates, "used": len(cands), "domains": _domains(cands)},
        "expansions": result.plan.expansions,
        "intent": result.intent,
        "freshness": result.freshness,
        "latency_ms": int((time.monotonic() - start) * 1000),
        "degraded": {"vector": not result.vector_ok, "reranker": not result.reranked, "llm": False},
    }


def _sse(event: str, data) -> str:
    return f"event: {event}\ndata: {json.dumps(data)}\n\n"


async def _sse_once(payload: dict):
    # Emit a retrieve-only result over the SSE channel for a uniform client path.
    for item in payload["results"]:
        yield _sse("source", item)
    yield _sse("meta", {k: payload[k] for k in ("mode", "reason", "coverage", "confidence", "degraded", "latency_ms")})
    yield _sse("done", {"ok": True})


async def _search_sse(req, result, start):
    cands = result.candidates
    # Surface the sources up front so the UI can render them while tokens stream.
    for i, c in enumerate(cands, start=1):
        yield _sse("source", {"n": i, "url": c.url, "title": c.title, "domain": c.domain,
                              "snippet": (c.text or "")[:280], "score": round(c.score, 4)})
    parts: list[str] = []
    try:
        async for tok in synthesis.stream_tokens(req.query, cands):
            parts.append(tok)
            yield _sse("token", {"t": tok})
    except Exception:
        log.exception("synthesis stream failed")
        yield _sse("error", {"detail": "synthesis_failed"})
    answer = "".join(parts)
    citations, used = synthesis.build_citations(answer, cands)
    yield _sse("citations", [_citation_dict(c) for c in citations])
    yield _sse("meta", {
        "mode": "synthesize",
        "confidence": synthesis.confidence(cands, used, result.reranked),
        "coverage": {"candidates": result.n_candidates, "used": len(cands), "domains": _domains(cands)},
        "expansions": result.plan.expansions,
        "intent": result.intent,
        "freshness": result.freshness,
        "latency_ms": int((time.monotonic() - start) * 1000),
        "degraded": {"vector": not result.vector_ok, "reranker": not result.reranked, "llm": False},
    })
    yield _sse("done", {"ok": True})


# --------------------------------------------------- targeted entity discovery ---

def _discovery_payload(result, max_results: int) -> dict:
    cands = result.candidates[:max_results]
    return {
        "status": result.status,
        "goal": result.brief.goal.value,
        "subject": {
            "surname": result.brief.surname,
            "given_name": result.brief.given_name,
            "known_attributes": result.brief.known_attributes,
        },
        "candidates": [
            {
                "name": sc.candidate.name,
                "handle": sc.candidate.handle,
                "platform": sc.candidate.platform,
                "score": sc.score,
                "signals": sc.match.signals,          # per-signal evidence trail
                "attributes": sc.candidate.attributes,
                "co_mentions": sc.candidate.co_mentions,
                "source_url": sc.candidate.source_url,
            }
            for sc in cands
        ],
        "best": (
            {
                "name": result.best.candidate.name,
                "handle": result.best.candidate.handle,
                "score": result.best.score,
                "source_url": result.best.candidate.source_url,
            }
            if result.best else None
        ),
        "stats": {"hops": result.hops, "fetches": result.fetches, "elapsed_s": result.elapsed_s},
    }


@app.post("/v1/discover/entity")
async def v1_discover_entity(req: DiscoverEntityRequest) -> JSONResponse:
    """Targeted, multi-hop lookup for an ultra-specific entity (docs/15).

    Runs the plan→act→observe→refine loop: attribute-anchored queries → hybrid
    retrieval over the indexed corpus → candidate extraction → resolution scoring
    → brief enrichment, bounded by the brief's budget. Returns ranked candidates
    each with the per-signal evidence for the match.
    """
    brief = TargetBrief.from_dict(req.model_dump())
    if not (brief.surname or brief.given_name or brief.seed_handles):
        return JSONResponse(
            status_code=400,
            content={"error": "brief needs at least a surname, given_name, or seed handle"},
        )

    async def retrieve_fn(query: str):
        # expand=False: the agent already fans out into many attribute-anchored
        # queries, so per-query LLM expansion would multiply calls for no recall.
        result = await retrieve(query, Filters(), max_sources=req.max_candidates, expand=False)
        return result.candidates

    # Sources: always the indexed corpus; add live SearXNG discovery (pages not yet
    # indexed — the core recall lever) when requested and the instance is reachable.
    sources = [retrieve_fn]
    if req.discover and await _searxng_available():
        sources.append(make_searxng_discover(
            settings.searxng_url,
            lambda url, **kw: clients.http().get(url, **kw),
            max_urls=settings.discover_searxng_max_urls,
        ))

    # PLAN: use the local LLM as the reasoner when it's up, else the deterministic
    # query generation (make_llm_planner degrades on its own too — recall-first).
    llm = _planner_llm if await clients.llm_available() else None
    plan = make_llm_planner(llm)

    search = make_search(brief, *sources)
    result = await discover_entity(brief, search, plan=plan)
    return JSONResponse(_discovery_payload(result, req.max_results))


async def _searxng_available() -> bool:
    try:
        resp = await clients.http().get(
            f"{settings.searxng_url}/healthz", timeout=httpx.Timeout(2.0))
        return resp.status_code < 500
    except Exception:
        return False


async def _planner_llm(messages: list[dict[str, str]], temperature: float) -> str:
    """Adapter handed to the LLM planner so entity_planner stays client-free."""
    return await clients.llm_complete(messages, options={"temperature": temperature})


# ------------------------------------------------------------------ coverage ---

@app.get("/v1/coverage")
async def v1_coverage() -> dict:
    """What's indexed (docs/chunks + top domains). Degrades to `available:false`
    when Postgres is unreachable, so the UI can render a status instead of 500."""
    try:
        pool = await clients.pg()
        total_docs = await pool.fetchval("SELECT count(*) FROM documents")
        indexed = await pool.fetchval("SELECT count(*) FROM documents WHERE n_chunks > 0")
        total_chunks = await pool.fetchval("SELECT COALESCE(sum(n_chunks),0) FROM documents WHERE n_chunks > 0")
        by_domain = await pool.fetch(
            "SELECT s.host AS domain, count(*) AS docs FROM documents d "
            "LEFT JOIN sources s ON s.id = d.source_id GROUP BY s.host ORDER BY docs DESC LIMIT 25"
        )
    except Exception:
        log.exception("coverage query failed (datastores down?)")
        return {"available": False, "documents": 0, "documents_indexed": 0, "chunks": 0, "by_domain": []}
    return {
        "available": True,
        "documents": total_docs,
        "documents_indexed": indexed,
        "chunks": int(total_chunks),
        "by_domain": [{"domain": r["domain"], "documents": r["docs"]} for r in by_domain],
    }
