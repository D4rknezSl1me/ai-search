# 06 — Data Model & Storage

## 1. Stores at a glance

| Store | Holds | Access pattern |
|-------|-------|----------------|
| PostgreSQL | frontier, sources, documents metadata, dedupe hashes, jobs, campaigns | transactional, relational |
| Redis | hot frontier, seen-URL bloom filter, per-host counters, caches | ephemeral, fast |
| MinIO | raw fetched bytes, archived rendered DOM/screenshots | blob, write-once |
| Qdrant | chunk embeddings + payload | ANN vector search |
| OpenSearch | chunk text + document metadata | BM25 / filters / facets |

## 2. PostgreSQL schema (core tables)

```sql
-- Sources / domains registry
CREATE TABLE sources (
  id            BIGSERIAL PRIMARY KEY,
  host          TEXT UNIQUE NOT NULL,
  type          TEXT,                 -- news|blog|forum|social|gov|academic|other
  authority     REAL DEFAULT 0.5,     -- 0..1, drives priority & rank boost
  politeness    JSONB,                -- per-host concurrency/delay overrides
  created_at    TIMESTAMPTZ DEFAULT now()
);

-- Crawl frontier
CREATE TABLE frontier_urls (
  id            BIGSERIAL PRIMARY KEY,
  url           TEXT NOT NULL,
  url_hash      BYTEA NOT NULL,       -- hash of canonical URL (dedupe)
  campaign_id   BIGINT REFERENCES campaigns(id),
  host          TEXT NOT NULL,
  depth         INT DEFAULT 0,
  priority      REAL DEFAULT 0,
  state         TEXT DEFAULT 'PENDING', -- PENDING|SCHEDULED|FETCHING|FETCHED|FAILED|SKIPPED|RETRY
  attempts      INT DEFAULT 0,
  next_attempt  TIMESTAMPTZ,
  discovered_at TIMESTAMPTZ DEFAULT now(),
  UNIQUE (url_hash, campaign_id)
);
CREATE INDEX ON frontier_urls (state, priority DESC);
CREATE INDEX ON frontier_urls (host, next_attempt);

-- Fetched documents (metadata; text lives in OpenSearch, raw in MinIO)
CREATE TABLE documents (
  id            BIGSERIAL PRIMARY KEY,
  url           TEXT NOT NULL,
  canonical_url TEXT,
  final_url     TEXT,
  source_id     BIGINT REFERENCES sources(id),
  http_status   INT,
  content_type  TEXT,
  content_hash  BYTEA NOT NULL,       -- exact-dup detection
  simhash       BIGINT,               -- near-dup detection
  dup_of        BIGINT REFERENCES documents(id), -- NULL if canonical
  title         TEXT,
  author        TEXT,
  lang          TEXT,
  published_at  TIMESTAMPTZ,
  fetched_at    TIMESTAMPTZ NOT NULL,
  blob_key      TEXT,                 -- MinIO object key of raw content
  n_chunks      INT DEFAULT 0,
  stage_version INT DEFAULT 1,        -- reprocessing marker
  meta          JSONB
);
CREATE UNIQUE INDEX ON documents (content_hash);
CREATE INDEX ON documents (published_at);
CREATE INDEX ON documents (source_id);

-- Crawl campaigns
CREATE TABLE campaigns (
  id            BIGSERIAL PRIMARY KEY,
  name          TEXT UNIQUE NOT NULL,
  config        JSONB NOT NULL,       -- scope, weights, politeness, recrawl
  status        TEXT DEFAULT 'active',
  created_at    TIMESTAMPTZ DEFAULT now()
);

-- Pipeline / job tracking (optional; queue is source of truth in flight)
CREATE TABLE jobs (
  id            BIGSERIAL PRIMARY KEY,
  kind          TEXT,                 -- fetch|extract|embed|index
  ref_id        BIGINT,
  state         TEXT,
  error         TEXT,
  updated_at    TIMESTAMPTZ DEFAULT now()
);
```

## 3. OpenSearch mapping (chunk index)

```json
{
  "mappings": {
    "properties": {
      "chunk_id":      { "type": "keyword" },
      "document_id":   { "type": "long" },
      "url":           { "type": "keyword" },
      "title":         { "type": "text" },
      "text":          { "type": "text", "analyzer": "standard" },
      "heading_path":  { "type": "text" },
      "lang":          { "type": "keyword" },
      "source_type":   { "type": "keyword" },
      "authority":     { "type": "float" },
      "published_at":  { "type": "date" },
      "fetched_at":    { "type": "date" },
      "char_start":    { "type": "integer" },
      "char_end":      { "type": "integer" }
    }
  }
}
```
Per-language analyzers configured for major languages; `text` also indexed with a shingle
sub-field for phrase recall.

## 4. Qdrant collection (chunk vectors)

- Collection `chunks`, vector size = embedding dim (e.g., 1024), distance = Cosine.
- Payload mirrors the retrieval-relevant metadata for filtered ANN search:
  `document_id, url, source_type, authority, lang, published_at, chunk_id`.
- HNSW index; payload indexes on `source_type`, `lang`, `published_at` for fast filtering.
- `chunk_id` is the join key back to OpenSearch and Postgres.

## 5. MinIO layout

```
raw/{yyyy}/{mm}/{dd}/{content_hash}.gz        # compressed raw bytes
dom/{content_hash}.html.gz                    # rendered DOM (browser fetches)
shots/{content_hash}.webp                     # optional screenshots (audit)
```
Raw content is compressed; retention policy configurable (e.g., keep raw N days, keep extracted
text forever). This is central to storage budgeting.

## 6. Storage budgeting (the hard ceiling on a home server)

You **cannot** store the raw web. Budget deliberately:

| Data | Approx size / 1M docs | Kept |
|------|----------------------|------|
| Raw HTML (gz) | ~50–150 GB | short retention (days) then drop |
| Clean text | ~5–15 GB | long-term |
| Embeddings (1024-d f32) | ~1M × 4 KB × (chunks/doc≈4) ≈ 16 GB | long-term (Qdrant) |
| OpenSearch index | ~10–30 GB | long-term |
| Postgres metadata | ~1–3 GB | long-term |

**Tactics:** drop raw after extraction, aggressive dedupe, quantize vectors (Qdrant scalar/binary
quantization) to cut vector RAM/disk ~4–32×, compress cold data, and tier old data to cheap disk.
Monitor disk as a first-class metric; enforce per-campaign storage caps.

## 7. Consistency & joins

- `chunk_id` and `document_id` are the universal join keys across all three indexes.
- Writes: Postgres (metadata) → MinIO (raw) → event → embed → Qdrant + OpenSearch. On partial
  failure, the event is retried; indexes are upserted idempotently by `chunk_id`.
- A periodic reconciler checks Postgres `documents.n_chunks` vs actual index counts and repairs.

## 8. Backups

- Postgres: WAL + nightly base backup.
- Qdrant: snapshot API.
- OpenSearch: snapshot repository (to MinIO).
- MinIO: versioning + optional offsite sync.
