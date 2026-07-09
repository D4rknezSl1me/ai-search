"""ai-search intelligence service.

Phase 0: skeleton FastAPI app. Exposes health/readiness and verifies HTTP
connectivity to Qdrant, OpenSearch and (optionally) TEI. The retrieval and RAG
pipeline arrives in Phase 2 (see docs/07-RAG-SEARCH.md, docs/12-ROADMAP.md).
"""

import httpx
from fastapi import FastAPI
from fastapi.responses import JSONResponse

from .config import settings

app = FastAPI(title="ai-search AI service", version="0.0.0")


async def _check(client: httpx.AsyncClient, url: str) -> bool:
    try:
        resp = await client.get(url, timeout=2.0)
        return resp.status_code < 500
    except Exception:
        return False


@app.get("/healthz")
async def healthz() -> dict:
    """Liveness: the process is up."""
    return {"status": "ok", "service": "ai-api"}


@app.get("/readyz")
async def readyz() -> JSONResponse:
    """Readiness: required backing services are reachable.

    TEI is optional in Phase 0 (only present under the 'gpu' compose profile),
    so it does not gate readiness.
    """
    checks = {
        "qdrant": f"{settings.qdrant_url}/readyz",
        "opensearch": f"{settings.opensearch_url}/_cluster/health",
        "tei": f"{settings.tei_url}/health",
    }
    required = {"qdrant", "opensearch"}

    results: dict[str, str] = {}
    async with httpx.AsyncClient() as client:
        for name, url in checks.items():
            ok = await _check(client, url)
            results[name] = "ok" if ok else "unreachable"

    ready = all(results[name] == "ok" for name in required)
    status_code = 200 if ready else 503
    return JSONResponse(
        status_code=status_code,
        content={
            "status": "ready" if ready else "not_ready",
            "dependencies": results,
            "note": "tei is optional in Phase 0 (gpu profile)",
        },
    )


@app.get("/metrics")
async def metrics() -> str:
    # Placeholder so Prometheus scrape config doesn't error; real metrics in Phase 2.
    return "# ai-search ai-api metrics placeholder\n"
