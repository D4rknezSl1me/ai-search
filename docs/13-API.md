# 13 — API Reference

Contracts for the external Search API and internal control APIs. Schemas are the source of truth
for implementation (Pydantic in Python, structs in Go). Illustrative; finalized in Phase 2.

## 1. Search API (public, Python/FastAPI)

### `POST /v1/search`
Natural-language query → grounded, cited answer.

Request:
```json
{
  "query": "string",
  "filters": {
    "date_from": "2025-01-01",
    "date_to": "2025-07-01",
    "source_types": ["news", "social"],
    "languages": ["en"],
    "domains_include": [], "domains_exclude": []
  },
  "options": {
    "stream": true,
    "max_sources": 12,
    "freshness": "auto",          // auto | fresh | any
    "synthesize": true            // false → retrieve-only
  }
}
```

Response (non-streamed):
```json
{
  "answer": "…text with inline [1][2] citations…",
  "citations": [
    { "n": 1, "url": "…", "title": "…", "published_at": "…", "snippet": "…", "score": 0.87, "source_type": "news" }
  ],
  "confidence": 0.72,
  "conflicts": [ { "claim": "…", "sources": [2, 5] } ],
  "coverage": { "candidates": 187, "used": 11, "domains": 6 },
  "latency_ms": 2410,
  "usage": { "llm_tokens_in": 4300, "llm_tokens_out": 380, "gpu_ms": 1900 }
}
```
Streaming: `text/event-stream`; events `token`, then `citations`, `meta`, `done`.

### `POST /v1/retrieve`
Documents only, no synthesis (same request minus synthesis; returns ranked `results[]` with
chunk text, url, scores). For clients that want raw retrieval.

### `GET /v1/coverage`
What's indexed: counts by domain/source-type/date, last-crawl times, total docs/chunks.

### `GET /healthz` / `GET /readyz`
Liveness / readiness (checks Qdrant, OpenSearch, TEI, LLM reachability).

## 2. Crawl control API (internal, Go)

### `POST /internal/campaigns`
Create a crawl campaign (scope, seeds, weights, politeness, recrawl) — see
[04-CRAWLER.md](04-CRAWLER.md) §8.

### `POST /internal/seeds`
Add seed URLs / sitemaps to a campaign.

### `GET /internal/frontier?campaign=…`
Frontier stats by state; host backpressure view.

### `GET /internal/jobs/{id}` · `GET /internal/documents/{id}`
Job/pipeline status; document metadata + provenance.

### `POST /internal/recrawl`
Trigger recrawl for a campaign/host/URL set.

### `GET /metrics`
Prometheus exposition (all services).

## 3. Internal events (NATS subjects)

| Subject | Payload | Producer → Consumer |
|---------|---------|---------------------|
| `fetch.raw` | `{doc_ref, blob_key, http_meta}` | fetcher → extractor |
| `doc.ready` | `{document_id, content_hash, lang}` | extractor → embedder |
| `index.upsert` | `{chunk_ids[]}` | embedder → indexers |
| `dlq.*` | failed message + reason | any → dead-letter |

All events carry `trace_id` for cross-plane tracing and are processed idempotently.

## 4. Conventions

- Versioned paths (`/v1`). JSON everywhere; SSE for streaming.
- Errors: RFC-7807-style `{type, title, status, detail, trace_id}`.
- Auth (P4): API key via `Authorization: Bearer …`; per-key rate limits & quotas.
- Pagination: cursor-based for list endpoints.
- Idempotency keys on write endpoints where retries are possible.
