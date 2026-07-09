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

### `GET /internal/social/adapters` · `GET /internal/social/adapters/{name}`
Per-adapter social health (Phase 3; see [08-SOCIAL-MEDIA.md](08-SOCIAL-MEDIA.md) §8). The list
form returns `{summary, adapters[]}`; `summary` aggregates `{adapters, enabled, disabled,
fetches, errors, items}` and each entry carries `{adapter, enabled, fetches, errors, items,
error_rate, last_error?, last_activity?}`. The `{name}` form returns a single adapter's snapshot
(404 if unknown). `enabled=false` means the adapter auto-disabled after its error rate crossed
the threshold over the minimum sample. Credential-free adapters registered today: `mastodon`,
`hackernews`, `lemmy`.

### `POST /internal/social/ingest`
Drive one social adapter over one seed synchronously and land the resulting posts in the same
`documents` + text-blob pipeline as web pages (Phase 3). Body: `{adapter, seed, max_pages?}`
(`max_pages` defaults to 1 = a single page per discovered target; larger values follow the
adapter's pagination cursor). Returns the run summary `{adapter, seed, targets, pages, docs,
inserted, duplicates, errors}`. Errors: 400 (missing `adapter`/`seed`), 503 (registry not
configured), 502 (unknown/disabled adapter or a discover failure, with the partial `result`
echoed). Exact-dedup is by content hash (platform+post_id), so re-ingesting a post is a no-op
insert counted under `duplicates`.

### `GET /internal/render/queue[?campaign=…]`
Browser render-queue counts by state (`PENDING|RENDERING|RENDERED|FAILED`) — the static→browser
escalation lane (Phase 3; see [04-CRAWLER.md](04-CRAWLER.md) §5). Global by default; scope with
`?campaign=ID`. Returns `{campaign_id, render_queue{state: count}}`.

### `POST /internal/render/claim` · `POST /internal/render/complete`
Lease/complete API for the (separate-language) headless-browser worker pool that consumes the
render queue. `claim` body `{n?}` (default 1, max 100) marks up to `n` PENDING jobs `RENDERING`
under a `claimed_at` lease and returns `{claimed, items[]}`, each item `{id, url, host,
campaign_id, reasons[], attempts}`. `complete` body `{id, ok, retry?}` records the outcome:
`ok=true` → `RENDERED`; `ok=false` → failure, rescheduled to `PENDING` after a backoff up to the
retry cap (default `retry=true`) else `FAILED`. Jobs left un-completed past the reaper timeout
are requeued from `RENDERING` back to `PENDING` (crash recovery, mirroring the frontier).

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
