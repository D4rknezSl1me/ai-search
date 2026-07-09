package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

// RenderItem is a URL awaiting a headless-browser re-fetch. It carries the
// escalation reasons so the browser worker (and metrics) can explain why the
// static path came up short.
type RenderItem struct {
	ID         int64    `json:"id"`
	URL        string   `json:"url"`
	Host       string   `json:"host"`
	CampaignID int64    `json:"campaign_id"`
	Reasons    []string `json:"reasons"`
	Attempts   int      `json:"attempts"`
}

// renderQueueDDL is the idempotent schema for the render queue. It mirrors
// db/migrations/0003_render_queue.sql and is executed at startup so the feature
// works on volumes that predate the migration (initdb only runs migrations on
// first init). Keep the two in sync.
const renderQueueDDL = `
CREATE TABLE IF NOT EXISTS render_queue (
  id            BIGSERIAL PRIMARY KEY,
  url           TEXT NOT NULL,
  url_hash      BYTEA NOT NULL,
  campaign_id   BIGINT REFERENCES campaigns(id),
  host          TEXT NOT NULL,
  priority      REAL NOT NULL DEFAULT 0,
  reasons       JSONB,
  state         TEXT NOT NULL DEFAULT 'PENDING',
  attempts      INT NOT NULL DEFAULT 0,
  next_attempt  TIMESTAMPTZ,
  claimed_at    TIMESTAMPTZ,
  enqueued_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  rendered_at   TIMESTAMPTZ,
  UNIQUE (url_hash, campaign_id)
);
CREATE INDEX IF NOT EXISTS render_queue_state_priority_idx ON render_queue (state, priority DESC);
CREATE INDEX IF NOT EXISTS render_queue_state_claimed_idx  ON render_queue (state, claimed_at);`

// EnsureRenderQueue creates the render queue table if it is missing. Idempotent;
// safe to call on every startup.
func (s *Store) EnsureRenderQueue(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, renderQueueDDL)
	return err
}

// EnqueueRender adds a URL to the render queue, ignoring one already queued for
// the same campaign (UNIQUE url_hash+campaign). Returns true if a new row was
// inserted. A URL already in the queue — PENDING, RENDERING, or even a prior
// RENDERED/FAILED — is left as-is; re-render policy is handled separately.
func (s *Store) EnqueueRender(ctx context.Context, campaignID int64, url string, urlHash []byte, host string, priority float64, reasons []string) (bool, error) {
	raw, err := json.Marshal(reasons)
	if err != nil {
		return false, err
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO render_queue (url, url_hash, campaign_id, host, priority, reasons, state)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb, 'PENDING')
		ON CONFLICT (url_hash, campaign_id) DO NOTHING`,
		url, urlHash, campaignID, host, priority, string(raw))
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ClaimNextRender atomically claims up to n PENDING render jobs (FOR UPDATE SKIP
// LOCKED), marking them RENDERING so concurrent browser workers never take the
// same URL. attempts is incremented on failure/reap, not on claim (mirrors the
// frontier), so claimed_at drives lease expiry via RequeueStuckRendering.
func (s *Store) ClaimNextRender(ctx context.Context, n int) ([]RenderItem, error) {
	rows, err := s.pool.Query(ctx, `
		UPDATE render_queue r SET state = 'RENDERING', claimed_at = now()
		WHERE r.id IN (
			SELECT id FROM render_queue
			WHERE state = 'PENDING'
			  AND (next_attempt IS NULL OR next_attempt <= now())
			ORDER BY priority DESC, id ASC
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING r.id, r.url, r.host, COALESCE(r.campaign_id, 0), r.reasons, r.attempts`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []RenderItem
	for rows.Next() {
		var it RenderItem
		var raw []byte
		if err := rows.Scan(&it.ID, &it.URL, &it.Host, &it.CampaignID, &raw, &it.Attempts); err != nil {
			return nil, err
		}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &it.Reasons)
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

// MarkRendered marks a render job done (the browser re-fetched and re-indexed).
func (s *Store) MarkRendered(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE render_queue SET state = 'RENDERED', rendered_at = now() WHERE id = $1`, id)
	return err
}

// MarkRenderFailed increments attempts. If retry is requested it reschedules the
// job (back to PENDING after backoff) until maxAttempts, after which it is FAILED.
// Mirrors the frontier's MarkFailed so render retries behave like fetch retries.
func (s *Store) MarkRenderFailed(ctx context.Context, id int64, retry bool, maxAttempts int, backoff time.Duration) error {
	if retry {
		_, err := s.pool.Exec(ctx, `
			UPDATE render_queue
			SET attempts = attempts + 1,
			    state = CASE WHEN attempts + 1 >= $3 THEN 'FAILED' ELSE 'PENDING' END,
			    next_attempt = CASE WHEN attempts + 1 >= $3 THEN next_attempt ELSE now() + $2 END
			WHERE id = $1`, id, backoff, maxAttempts)
		return err
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE render_queue SET state = 'FAILED', attempts = attempts + 1 WHERE id = $1`, id)
	return err
}

// RequeueStuckRendering returns jobs orphaned in RENDERING (a browser worker
// crashed mid-render) to PENDING once past olderThan, or FAILED at the cap.
// Staleness is measured from claimed_at. Returns the number requeued. Mirrors
// RequeueStuckFetching for the render lane.
func (s *Store) RequeueStuckRendering(ctx context.Context, olderThan time.Duration, maxAttempts int) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE render_queue
		SET state = CASE WHEN attempts + 1 >= $2 THEN 'FAILED' ELSE 'PENDING' END,
		    attempts = attempts + 1,
		    next_attempt = now()
		WHERE state = 'RENDERING'
		  AND claimed_at IS NOT NULL
		  AND claimed_at < now() - $1::interval`,
		olderThan.String(), maxAttempts)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// RenderQueueStats returns render-queue counts by state. If campaignID > 0 the
// counts are scoped to that campaign; otherwise they are global.
func (s *Store) RenderQueueStats(ctx context.Context, campaignID int64) (map[string]int, error) {
	var (
		rows pgx.Rows
		err  error
	)
	if campaignID > 0 {
		rows, err = s.pool.Query(ctx,
			`SELECT state, count(*) FROM render_queue WHERE campaign_id = $1 GROUP BY state`, campaignID)
	} else {
		rows, err = s.pool.Query(ctx,
			`SELECT state, count(*) FROM render_queue GROUP BY state`)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var state string
		var n int
		if err := rows.Scan(&state, &n); err != nil {
			return nil, err
		}
		out[state] = n
	}
	return out, rows.Err()
}
