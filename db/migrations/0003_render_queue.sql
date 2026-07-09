-- 0003 — browser render queue (Phase 3).
--
-- Durable hand-off between the static crawler's escalation gate (internal/render
-- decides a page's real content is locked behind JavaScript) and the headless
-- browser worker pool that re-fetches those URLs. The gate previously only
-- stamped `documents.meta.needs_render`; that is an observability flag, not a
-- work queue — it has no claim/lease/retry semantics. This table is the queue:
-- a frontier-shaped state machine (PENDING → RENDERING → RENDERED | FAILED) the
-- (separate-language, per docs/02) Playwright service consumes over the control
-- API. See docs/04-CRAWLER.md §5 and docs/06-DATA-MODEL.md.
--
-- The crawler also ensures this table at startup (store.EnsureRenderQueue) so the
-- feature works on pre-existing volumes where initdb-only migrations never ran.
BEGIN;

CREATE TABLE IF NOT EXISTS render_queue (
  id            BIGSERIAL PRIMARY KEY,
  url           TEXT NOT NULL,
  url_hash      BYTEA NOT NULL,
  campaign_id   BIGINT REFERENCES campaigns(id),
  host          TEXT NOT NULL,
  priority      REAL NOT NULL DEFAULT 0,
  reasons       JSONB,                         -- escalation reasons from internal/render
  state         TEXT NOT NULL DEFAULT 'PENDING', -- PENDING|RENDERING|RENDERED|FAILED
  attempts      INT NOT NULL DEFAULT 0,
  next_attempt  TIMESTAMPTZ,
  claimed_at    TIMESTAMPTZ,                    -- set on claim; drives the stuck-render reaper
  enqueued_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  rendered_at   TIMESTAMPTZ,
  UNIQUE (url_hash, campaign_id)
);
CREATE INDEX IF NOT EXISTS render_queue_state_priority_idx ON render_queue (state, priority DESC);
CREATE INDEX IF NOT EXISTS render_queue_state_claimed_idx  ON render_queue (state, claimed_at);

COMMIT;
