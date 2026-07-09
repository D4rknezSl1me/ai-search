-- ai-search initial schema. See docs/06-DATA-MODEL.md.
-- Applied automatically by Postgres on first init (docker-entrypoint-initdb.d).
-- Ordering matters: referenced tables are defined before their referrers.

BEGIN;

-- ---------------------------------------------------------------- sources ---
CREATE TABLE IF NOT EXISTS sources (
  id            BIGSERIAL PRIMARY KEY,
  host          TEXT UNIQUE NOT NULL,
  type          TEXT,                       -- news|blog|forum|social|gov|academic|other
  authority     REAL NOT NULL DEFAULT 0.5,  -- 0..1
  politeness    JSONB,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- -------------------------------------------------------------- campaigns ---
CREATE TABLE IF NOT EXISTS campaigns (
  id            BIGSERIAL PRIMARY KEY,
  name          TEXT UNIQUE NOT NULL,
  config        JSONB NOT NULL,
  status        TEXT NOT NULL DEFAULT 'active',
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- -------------------------------------------------------------- documents ---
CREATE TABLE IF NOT EXISTS documents (
  id            BIGSERIAL PRIMARY KEY,
  url           TEXT NOT NULL,
  canonical_url TEXT,
  final_url     TEXT,
  source_id     BIGINT REFERENCES sources(id),
  http_status   INT,
  content_type  TEXT,
  content_hash  BYTEA NOT NULL,
  simhash       BIGINT,
  dup_of        BIGINT REFERENCES documents(id),
  title         TEXT,
  author        TEXT,
  lang          TEXT,
  published_at  TIMESTAMPTZ,
  fetched_at    TIMESTAMPTZ NOT NULL,
  blob_key      TEXT,
  n_chunks      INT NOT NULL DEFAULT 0,
  stage_version INT NOT NULL DEFAULT 1,
  meta          JSONB
);
CREATE UNIQUE INDEX IF NOT EXISTS documents_content_hash_idx ON documents (content_hash);
CREATE INDEX IF NOT EXISTS documents_published_at_idx ON documents (published_at);
CREATE INDEX IF NOT EXISTS documents_source_id_idx ON documents (source_id);

-- ----------------------------------------------------------- frontier_urls ---
CREATE TABLE IF NOT EXISTS frontier_urls (
  id            BIGSERIAL PRIMARY KEY,
  url           TEXT NOT NULL,
  url_hash      BYTEA NOT NULL,
  campaign_id   BIGINT REFERENCES campaigns(id),
  host          TEXT NOT NULL,
  depth         INT NOT NULL DEFAULT 0,
  priority      REAL NOT NULL DEFAULT 0,
  state         TEXT NOT NULL DEFAULT 'PENDING',
  attempts      INT NOT NULL DEFAULT 0,
  next_attempt  TIMESTAMPTZ,
  discovered_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (url_hash, campaign_id)
);
CREATE INDEX IF NOT EXISTS frontier_state_priority_idx ON frontier_urls (state, priority DESC);
CREATE INDEX IF NOT EXISTS frontier_host_next_idx ON frontier_urls (host, next_attempt);

-- ------------------------------------------------------------------- jobs ---
CREATE TABLE IF NOT EXISTS jobs (
  id            BIGSERIAL PRIMARY KEY,
  kind          TEXT,                       -- fetch|extract|embed|index
  ref_id        BIGINT,
  state         TEXT,
  error         TEXT,
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS jobs_kind_state_idx ON jobs (kind, state);

COMMIT;
