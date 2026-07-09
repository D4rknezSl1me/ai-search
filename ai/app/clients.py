"""Shared clients and thin wrappers over the local model/data services.

Everything the intelligence plane talks to is self-hosted (CLAUDE.md rule 2):
TEI for embeddings and reranking, Ollama for synthesis, Qdrant + OpenSearch for
retrieval, Postgres + MinIO for source documents. All network I/O on the hot
path is async (httpx / asyncpg); the MinIO SDK is sync and used off the loop.
"""

from __future__ import annotations

import asyncio
import gzip
from functools import lru_cache
from typing import Any

import asyncpg
import httpx
from minio import Minio

from .config import settings

# bge-*-en-v1.5 wants this instruction prefixed on *queries* only (not passages);
# it measurably improves asymmetric retrieval. See the BGE model card.
QUERY_INSTRUCTION = "Represent this sentence for searching relevant passages: "

_http: httpx.AsyncClient | None = None
_pg_pool: asyncpg.Pool | None = None
_minio: Minio | None = None


def http() -> httpx.AsyncClient:
    global _http
    if _http is None:
        _http = httpx.AsyncClient(timeout=httpx.Timeout(30.0, connect=5.0))
    return _http


async def pg() -> asyncpg.Pool:
    global _pg_pool
    if _pg_pool is None:
        _pg_pool = await asyncpg.create_pool(settings.pg_dsn, min_size=1, max_size=8)
    return _pg_pool


@lru_cache(maxsize=1)
def minio() -> Minio:
    return Minio(
        settings.minio_endpoint,
        access_key=settings.minio_root_user,
        secret_key=settings.minio_root_password,
        secure=False,
    )


async def close() -> None:
    global _http, _pg_pool
    if _http is not None:
        await _http.aclose()
        _http = None
    if _pg_pool is not None:
        await _pg_pool.close()
        _pg_pool = None


# --------------------------------------------------------------------- blobs ---

def _get_text_sync(bucket: str, key: str) -> str:
    resp = minio().get_object(bucket, key)
    try:
        return gzip.decompress(resp.read()).decode("utf-8", errors="replace")
    finally:
        resp.close()
        resp.release_conn()


async def get_text(content_hash_hex: str) -> str | None:
    """Fetch a document's clean text from MinIO (written by the crawler)."""
    key = f"text/{content_hash_hex}.txt.gz"
    try:
        return await asyncio.to_thread(_get_text_sync, settings.minio_bucket, key)
    except Exception:
        return None


# ---------------------------------------------------------------- embeddings ---

async def embed(texts: list[str], *, is_query: bool = False) -> list[list[float]]:
    """Embed a batch via TEI. Queries get the BGE instruction prefix."""
    if not texts:
        return []
    if is_query:
        texts = [QUERY_INSTRUCTION + t for t in texts]
    out: list[list[float]] = []
    for i in range(0, len(texts), settings.embed_batch):
        batch = texts[i : i + settings.embed_batch]
        resp = await http().post(
            f"{settings.tei_url}/embed",
            json={"inputs": batch, "truncate": True},
        )
        resp.raise_for_status()
        out.extend(resp.json())
    return out


# ------------------------------------------------------------------ reranker ---

async def rerank(query: str, texts: list[str]) -> list[tuple[int, float]] | None:
    """Cross-encoder rerank via TEI /rerank. Returns (orig_index, score) sorted
    best-first, or None if the reranker is unavailable (caller degrades to RRF)."""
    if not texts:
        return []
    try:
        resp = await http().post(
            f"{settings.rerank_url}/rerank",
            json={"query": query, "texts": texts, "truncate": True},
            timeout=httpx.Timeout(30.0, connect=2.0),
        )
        resp.raise_for_status()
        ranked = resp.json()
        return [(item["index"], float(item["score"])) for item in ranked]
    except Exception:
        return None


# ----------------------------------------------------------------------- LLM ---

async def llm_available() -> bool:
    try:
        resp = await http().get(f"{settings.llm_url}/api/tags", timeout=httpx.Timeout(3.0))
        return resp.status_code == 200
    except Exception:
        return False


async def llm_chat_stream(messages: list[dict[str, str]], *, options: dict[str, Any] | None = None):
    """Stream assistant tokens from Ollama's /api/chat (newline-delimited JSON)."""
    payload: dict[str, Any] = {"model": settings.llm_model, "messages": messages, "stream": True}
    if options:
        payload["options"] = options
    async with http().stream(
        "POST", f"{settings.llm_url}/api/chat", json=payload,
        timeout=httpx.Timeout(300.0, connect=5.0),
    ) as resp:
        resp.raise_for_status()
        async for line in resp.aiter_lines():
            if line.strip():
                yield line
