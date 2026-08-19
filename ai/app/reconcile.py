"""Index reconciliation (docs/12 Phase 4; backlog).

A document is indexed as chunks `{id}:0 … {id}:n-1`. If it is ever re-indexed to
*fewer* chunks (content shrank, chunking changed), the surplus high-index chunks
would orphan in Qdrant/OpenSearch and keep surfacing in results. This module
prunes any chunk whose index is ≥ the document's current `n_chunks`, and reports
drift between Postgres' `n_chunks` and what the stores actually hold.

Idempotent and safe: it only ever deletes chunks that are *not* in the valid set
for their document, so re-running converges and never removes live chunks.
"""

from __future__ import annotations

import logging

from . import clients
from .config import settings

log = logging.getLogger("reconcile")


def valid_chunk_ids(document_id: int, n_chunks: int) -> list[str]:
    """The chunk_ids a document should currently have in the indexes."""
    if n_chunks <= 0:
        return []
    return [f"{document_id}:{i}" for i in range(n_chunks)]


async def _opensearch_orphans_deleted(document_id: int, valid: list[str]) -> int:
    """Delete this document's OpenSearch chunks that are not in the valid set."""
    body = {
        "query": {
            "bool": {
                "must": [{"term": {"document_id": document_id}}],
                "must_not": [{"terms": {"chunk_id": valid}}] if valid else [],
            }
        }
    }
    resp = await clients.http().post(
        f"{settings.opensearch_url}/{settings.opensearch_index}/_delete_by_query",
        params={"refresh": "true", "conflicts": "proceed"}, json=body,
    )
    resp.raise_for_status()
    return int(resp.json().get("deleted", 0))


async def _qdrant_orphans_deleted(document_id: int, valid: list[str]) -> int:
    """Delete this document's Qdrant points that are not in the valid set."""
    must_not = [{"key": "chunk_id", "match": {"any": valid}}] if valid else []
    flt = {"must": [{"key": "document_id", "match": {"value": document_id}}], "must_not": must_not}
    # Count first (Qdrant delete doesn't report how many matched).
    cnt = await clients.http().post(
        f"{settings.qdrant_url}/collections/{settings.qdrant_collection}/points/count",
        json={"filter": flt, "exact": True},
    )
    cnt.raise_for_status()
    n = int(cnt.json().get("result", {}).get("count", 0))
    if n:
        resp = await clients.http().post(
            f"{settings.qdrant_url}/collections/{settings.qdrant_collection}/points/delete",
            params={"wait": "true"}, json={"filter": flt},
        )
        resp.raise_for_status()
    return n


async def prune_document(document_id: int, n_chunks: int) -> dict[str, int]:
    """Prune orphaned chunks for one document from both indexes."""
    valid = valid_chunk_ids(document_id, n_chunks)
    os_deleted = await _opensearch_orphans_deleted(document_id, valid)
    q_deleted = await _qdrant_orphans_deleted(document_id, valid)
    if os_deleted or q_deleted:
        log.info("reconcile doc %s: pruned os=%d qdrant=%d", document_id, os_deleted, q_deleted)
    return {"opensearch": os_deleted, "qdrant": q_deleted}


async def reconcile_all(limit: int | None = None) -> dict[str, int]:
    """Prune orphans across indexed documents; returns aggregate counts."""
    pool = await clients.pg()
    rows = await pool.fetch(
        "SELECT id, n_chunks FROM documents WHERE n_chunks > 0 ORDER BY id LIMIT $1",
        limit or settings.reconcile_batch,
    )
    docs = os_pruned = q_pruned = 0
    for row in rows:
        try:
            res = await prune_document(row["id"], row["n_chunks"])
            os_pruned += res["opensearch"]
            q_pruned += res["qdrant"]
            docs += 1
        except Exception:
            log.exception("reconcile document %s failed", row["id"])
    return {"documents": docs, "opensearch_pruned": os_pruned, "qdrant_pruned": q_pruned}
