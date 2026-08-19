"""Indexer: turn stored documents into retrievable chunks.

Poll-based (robust, backfill-friendly, no queue dependency): find documents that
Postgres marks un-indexed, pull their clean text from MinIO, chunk → embed (TEI)
→ upsert into Qdrant (vectors) and OpenSearch (text), then record the chunk count
on the document. Idempotent per chunk_id, so re-runs converge.

documents.n_chunks convention: 0 = not indexed yet, >0 = indexed with N chunks,
-1 = processed but no indexable text (sentinel to avoid re-polling empties).
"""

from __future__ import annotations

import asyncio
import json
import logging
import uuid

from . import clients
from .chunking import chunk_text
from .config import settings

log = logging.getLogger("indexer")

_CHUNK_NS = uuid.UUID("6f9619ff-8b86-d011-b42d-00cf4fc964ff")  # stable namespace


def _point_id(chunk_id: str) -> str:
    return str(uuid.uuid5(_CHUNK_NS, chunk_id))


def _keyphrases(raw) -> list[str]:
    """Normalize the `meta->'keyphrases'` jsonb (asyncpg returns it as text)."""
    if raw is None:
        return []
    if isinstance(raw, str):
        try:
            raw = json.loads(raw)
        except (ValueError, TypeError):
            return []
    return [str(x) for x in raw] if isinstance(raw, list) else []


_UNINDEXED_SQL = """
    SELECT d.id,
           encode(d.content_hash, 'hex') AS chash,
           d.url, d.title, d.lang, d.published_at, d.fetched_at,
           d.meta -> 'keyphrases' AS keyphrases,
           s.host AS domain, s.type AS source_type,
           COALESCE(s.authority, 0.5) AS authority
    FROM documents d
    LEFT JOIN sources s ON s.id = d.source_id
    WHERE d.n_chunks = 0 AND d.dup_of IS NULL
    ORDER BY d.id
    LIMIT $1
"""


async def _index_one(pool, row) -> int:
    text = await clients.get_text(row["chash"])
    if not text or not text.strip():
        await pool.execute("UPDATE documents SET n_chunks = -1 WHERE id = $1", row["id"])
        return 0

    chunks = chunk_text(text)
    if not chunks:
        await pool.execute("UPDATE documents SET n_chunks = -1 WHERE id = $1", row["id"])
        return 0

    vectors = await clients.embed([c.text for c in chunks], is_query=False)

    published = row["published_at"].isoformat() if row["published_at"] else None
    fetched = row["fetched_at"].isoformat() if row["fetched_at"] else None
    base_payload = {
        "document_id": row["id"],
        "url": row["url"],
        "domain": row["domain"],
        "title": row["title"],
        "lang": row["lang"] or None,
        "source_type": row["source_type"] or None,
        "authority": float(row["authority"]),
        "published_at": published,
        "fetched_at": fetched,
        "keyphrases": _keyphrases(row["keyphrases"]),
    }

    q_points = []
    os_lines: list[str] = []
    for c, vec in zip(chunks, vectors):
        chunk_id = f"{row['id']}:{c.index}"
        payload = {
            **base_payload,
            "chunk_id": chunk_id,
            "char_start": c.char_start,
            "char_end": c.char_end,
        }
        q_points.append({"id": _point_id(chunk_id), "vector": vec, "payload": payload})
        os_lines.append(json.dumps({"index": {"_id": chunk_id}}))
        os_lines.append(json.dumps({**payload, "text": c.text}))

    await _qdrant_upsert(q_points)
    await _opensearch_bulk(os_lines)

    await pool.execute("UPDATE documents SET n_chunks = $2 WHERE id = $1", row["id"], len(chunks))
    return len(chunks)


async def _qdrant_upsert(points: list[dict]) -> None:
    if not points:
        return
    url = f"{settings.qdrant_url}/collections/{settings.qdrant_collection}/points"
    resp = await clients.http().put(url, params={"wait": "true"}, json={"points": points})
    resp.raise_for_status()


async def _opensearch_bulk(lines: list[str]) -> None:
    if not lines:
        return
    body = "\n".join(lines) + "\n"
    resp = await clients.http().post(
        f"{settings.opensearch_url}/{settings.opensearch_index}/_bulk",
        content=body,
        headers={"Content-Type": "application/x-ndjson"},
    )
    resp.raise_for_status()
    if resp.json().get("errors"):
        log.warning("opensearch bulk reported item errors")


async def index_pending(limit: int | None = None) -> dict[str, int]:
    """Index one batch of pending documents. Returns counts (docs, chunks)."""
    pool = await clients.pg()
    rows = await pool.fetch(_UNINDEXED_SQL, limit or settings.indexer_batch)
    docs = chunks = 0
    for row in rows:
        try:
            n = await _index_one(pool, row)
            docs += 1
            chunks += n
        except Exception:
            log.exception("indexing document %s failed", row["id"])
    return {"documents": docs, "chunks": chunks, "remaining": await _pending_count(pool)}


async def _pending_count(pool) -> int:
    return await pool.fetchval(
        "SELECT count(*) FROM documents WHERE n_chunks = 0 AND dup_of IS NULL"
    )


async def run_loop(stop: asyncio.Event) -> None:
    """Background loop: drain pending docs, then idle-poll for new ones."""
    while not stop.is_set():
        try:
            result = await index_pending()
            if result["documents"]:
                log.info(
                    "indexed %d doc(s), %d chunk(s); %d remaining",
                    result["documents"], result["chunks"], result["remaining"],
                )
                if result["remaining"]:
                    continue  # keep draining without the idle wait
        except Exception:
            log.exception("indexer loop error")
        try:
            await asyncio.wait_for(stop.wait(), timeout=settings.indexer_poll_seconds)
        except asyncio.TimeoutError:
            pass
