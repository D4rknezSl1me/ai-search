-- 0004 — tracked entities: freshness cadence for social monitoring (Phase 3).
--
-- Social content is time-sensitive (docs/08 §7): a hashtag, profile, or instance
-- worth watching must be re-ingested on a short, per-entity cadence rather than
-- only on a one-shot operator trigger. This table is the durable registry of
-- what to keep fresh — one row per (adapter, seed) — and the schedule that drives
-- it: `next_due_at` is a self-advancing timer the freshness scheduler
-- (internal/freshness) claims from every tick, re-running the same social
-- ingester used by POST /internal/social/ingest. Re-ingested posts dedupe by
-- content hash, so re-running is idempotent at the document level.
--
-- The crawler also ensures this table at startup (store.EnsureTrackedEntities) so
-- the feature works on pre-existing volumes where initdb-only migrations never ran.
BEGIN;

CREATE TABLE IF NOT EXISTS tracked_entities (
  id               BIGSERIAL PRIMARY KEY,
  adapter          TEXT NOT NULL,                  -- social adapter name (mastodon|hackernews|lemmy|…)
  seed             TEXT NOT NULL,                  -- the seed handed to the adapter (hashtag/profile/instance)
  cadence_seconds  INT NOT NULL,                   -- how often to re-ingest this entity
  max_pages        INT NOT NULL DEFAULT 0,         -- pagination cap per run (0 → adapter default)
  enabled          BOOLEAN NOT NULL DEFAULT TRUE,  -- false suspends scheduling without deleting history
  next_due_at      TIMESTAMPTZ NOT NULL DEFAULT now(), -- self-advancing timer; a run is due when <= now()
  last_ingested_at TIMESTAMPTZ,                    -- when the last run completed
  last_result      JSONB,                          -- last run summary (social.Result)
  last_error       TEXT,                           -- last run error, if any (fail-loud visibility)
  runs             INT NOT NULL DEFAULT 0,         -- completed run count
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (adapter, seed)
);
CREATE INDEX IF NOT EXISTS tracked_entities_due_idx ON tracked_entities (enabled, next_due_at);

COMMIT;
