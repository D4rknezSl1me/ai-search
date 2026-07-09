-- 0002 — frontier claim timestamp.
--
-- Records when a URL was claimed (moved to FETCHING) so the reaper can requeue
-- only URLs that have been stuck past a timeout (orphaned by a crashed worker),
-- without disturbing ones that are legitimately still being fetched.
ALTER TABLE frontier_urls ADD COLUMN IF NOT EXISTS claimed_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS frontier_urls_state_claimed_idx
  ON frontier_urls (state, claimed_at);
