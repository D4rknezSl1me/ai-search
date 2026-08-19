-- Phase 4: recrawl/freshness scheduling.
-- Track when each frontier URL was last fetched so the recrawl scheduler can
-- re-enqueue stale URLs. Idempotent so it is safe on pre-existing volumes.
ALTER TABLE frontier_urls ADD COLUMN IF NOT EXISTS last_fetched_at timestamptz;

-- Recrawl selection scans FETCHED rows ordered by staleness.
CREATE INDEX IF NOT EXISTS idx_frontier_recrawl
    ON frontier_urls (last_fetched_at)
    WHERE state = 'FETCHED';
