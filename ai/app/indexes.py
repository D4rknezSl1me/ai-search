"""Idempotent creation of the Qdrant collection and OpenSearch index.

Schemas follow docs/06-DATA-MODEL.md §3–4. Both are keyed by chunk_id so they
join back to each other and to Postgres. Safe to call on every startup.
"""

from __future__ import annotations

import httpx

from .clients import http
from .config import settings

# OpenSearch mapping: `text` gets a shingle sub-field for phrase/near-phrase
# recall (docs/06 §3). Keyword/date fields drive filters and faceting.
OPENSEARCH_MAPPING = {
    "settings": {
        "index": {"number_of_shards": 1, "number_of_replicas": 0},
        "analysis": {
            "analyzer": {
                "shingle_analyzer": {
                    "type": "custom",
                    "tokenizer": "standard",
                    "filter": ["lowercase", "shingle"],
                }
            }
        },
    },
    "mappings": {
        "properties": {
            "chunk_id": {"type": "keyword"},
            "document_id": {"type": "long"},
            "url": {"type": "keyword"},
            "domain": {"type": "keyword"},
            "title": {"type": "text"},
            "text": {
                "type": "text",
                "analyzer": "standard",
                "fields": {"shingles": {"type": "text", "analyzer": "shingle_analyzer"}},
            },
            "heading_path": {"type": "text"},
            # Extracted topic tags (crawler RAKE) — searchable for recall + faceting.
            "keyphrases": {"type": "text", "fields": {"raw": {"type": "keyword"}}},
            "lang": {"type": "keyword"},
            "source_type": {"type": "keyword"},
            "authority": {"type": "float"},
            "published_at": {"type": "date"},
            "fetched_at": {"type": "date"},
            "char_start": {"type": "integer"},
            "char_end": {"type": "integer"},
        }
    },
}


async def ensure_opensearch_index() -> None:
    url = f"{settings.opensearch_url}/{settings.opensearch_index}"
    resp = await http().head(url)
    if resp.status_code == 200:
        return
    resp = await http().put(url, json=OPENSEARCH_MAPPING)
    # 400 with resource_already_exists is fine under a startup race.
    if resp.status_code >= 400 and "resource_already_exists" not in resp.text:
        resp.raise_for_status()


def qdrant_collection_body(
    *,
    dim: int | None = None,
    distance: str = "Cosine",
    quantization: bool | None = None,
    quantile: float | None = None,
    always_ram: bool | None = None,
) -> dict:
    """Build the Qdrant collection creation payload.

    When quantization is on, attach scalar **int8** quantization: it stores a
    ~4x-smaller quantized copy of each vector (optionally RAM-resident) while
    keeping the originals for rescoring — density without a real recall hit
    (docs/06). Defaults come from config; args let tests pin them.
    """
    dim = settings.embed_dim if dim is None else dim
    quantization = settings.qdrant_quantization if quantization is None else quantization
    body: dict = {"vectors": {"size": dim, "distance": distance}}
    if quantization:
        body["quantization_config"] = {
            "scalar": {
                "type": "int8",
                "quantile": settings.qdrant_quantization_quantile if quantile is None else quantile,
                "always_ram": settings.qdrant_quantization_always_ram if always_ram is None else always_ram,
            }
        }
    return body


async def ensure_qdrant_collection() -> None:
    base = f"{settings.qdrant_url}/collections/{settings.qdrant_collection}"
    resp = await http().get(base)
    if resp.status_code == 200:
        return
    body = qdrant_collection_body()
    resp = await http().put(base, json=body)
    if resp.status_code >= 400:
        resp.raise_for_status()
    # Payload indexes for fast filtered ANN (docs/06 §4).
    for field, schema in [
        ("source_type", "keyword"),
        ("lang", "keyword"),
        ("domain", "keyword"),
        ("document_id", "integer"),
        ("published_at", "datetime"),
    ]:
        try:
            await http().put(
                f"{base}/index",
                params={"wait": "true"},
                json={"field_name": field, "field_schema": schema},
            )
        except httpx.HTTPError:
            pass


async def ensure_all() -> None:
    await ensure_opensearch_index()
    await ensure_qdrant_collection()
