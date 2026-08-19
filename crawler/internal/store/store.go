// Package store is the Postgres data-access layer for the crawler.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool *pgxpool.Pool
}

// New opens a connection pool, retrying until Postgres is reachable.
func New(ctx context.Context, dsn string) (*Store, error) {
	var pool *pgxpool.Pool
	var err error
	for i := 0; i < 30; i++ {
		pool, err = pgxpool.New(ctx, dsn)
		if err == nil {
			if err = pool.Ping(ctx); err == nil {
				return &Store{pool: pool}, nil
			}
			pool.Close()
		}
		time.Sleep(2 * time.Second)
	}
	return nil, err
}

func (s *Store) Close() { s.pool.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// ---------------------------------------------------------------- campaigns ---

type Campaign struct {
	ID     int64
	Name   string
	Config CampaignConfig
}

type CampaignConfig struct {
	Seeds         []string `json:"seeds"`
	MaxDepth      int      `json:"max_depth"`
	MaxPages      int      `json:"max_pages"`
	AllowExternal bool     `json:"allow_external"`
	MinDelayMs    int      `json:"min_delay_ms"`
	// RenderJS is the browser-escalation policy: never | auto | always.
	// Empty is treated as auto. See internal/render.
	RenderJS string `json:"render_js"`
}

// UpsertCampaign creates or updates a campaign by name and returns its id.
// The config is marshalled to JSON and cast to jsonb in SQL to avoid pgx
// parameter-type ambiguity for struct values.
func (s *Store) UpsertCampaign(ctx context.Context, name string, cfg any) (int64, error) {
	data, err := json.Marshal(cfg)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.pool.QueryRow(ctx, `
		INSERT INTO campaigns (name, config)
		VALUES ($1, $2::jsonb)
		ON CONFLICT (name) DO UPDATE SET config = EXCLUDED.config
		RETURNING id`, name, string(data)).Scan(&id)
	return id, err
}

func (s *Store) GetCampaign(ctx context.Context, id int64) (*Campaign, error) {
	c := &Campaign{ID: id}
	var raw []byte
	if err := s.pool.QueryRow(ctx,
		`SELECT name, config FROM campaigns WHERE id = $1`, id).Scan(&c.Name, &raw); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &c.Config); err != nil {
		return nil, err
	}
	return c, nil
}

// ------------------------------------------------------------------ sources ---

// EnsureSource upserts a host and returns its source id.
func (s *Store) EnsureSource(ctx context.Context, host string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO sources (host) VALUES ($1)
		ON CONFLICT (host) DO UPDATE SET host = EXCLUDED.host
		RETURNING id`, host).Scan(&id)
	return id, err
}

// ----------------------------------------------------------------- frontier ---

type FrontierItem struct {
	ID           int64
	URL          string
	Host         string
	Depth        int
	CampaignID   int64
	Attempts     int    // prior failed attempts (drives exponential backoff)
	ETag         string // prior validator for conditional GET ("" if none)
	LastModified string // prior Last-Modified for conditional GET ("" if none)
}

// AddURL inserts a frontier URL, ignoring duplicates (same url_hash+campaign).
// Returns true if a new row was inserted.
func (s *Store) AddURL(ctx context.Context, campaignID int64, canonURL string, urlHash []byte, host string, depth int, priority float64) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO frontier_urls (url, url_hash, campaign_id, host, depth, priority, state)
		VALUES ($1, $2, $3, $4, $5, $6, 'PENDING')
		ON CONFLICT (url_hash, campaign_id) DO NOTHING`,
		canonURL, urlHash, campaignID, host, depth, priority)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ClaimNext atomically claims up to n PENDING/eligible URLs (FOR UPDATE SKIP
// LOCKED) and marks them FETCHING, so multiple workers never take the same URL.
func (s *Store) ClaimNext(ctx context.Context, n int) ([]FrontierItem, error) {
	rows, err := s.pool.Query(ctx, `
		UPDATE frontier_urls f SET state = 'FETCHING', claimed_at = now()
		WHERE f.id IN (
			SELECT id FROM frontier_urls
			WHERE state = 'PENDING'
			  AND (next_attempt IS NULL OR next_attempt <= now())
			ORDER BY priority DESC, id ASC
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING f.id, f.url, f.host, f.depth, f.campaign_id, f.attempts,
		          COALESCE(f.etag, ''), COALESCE(f.last_modified, '')`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []FrontierItem
	for rows.Next() {
		var it FrontierItem
		if err := rows.Scan(&it.ID, &it.URL, &it.Host, &it.Depth, &it.CampaignID,
			&it.Attempts, &it.ETag, &it.LastModified); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

// SetValidators records the HTTP validators (ETag / Last-Modified) returned for a
// URL, so a later recrawl can issue a conditional GET and skip re-processing an
// unchanged page. Empty strings clear the stored value.
func (s *Store) SetValidators(ctx context.Context, id int64, etag, lastModified string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE frontier_urls SET etag = NULLIF($2, ''), last_modified = NULLIF($3, '') WHERE id = $1`,
		id, etag, lastModified)
	return err
}

func (s *Store) MarkFetched(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE frontier_urls SET state = 'FETCHED', last_fetched_at = now() WHERE id = $1`, id)
	return err
}

// EnsureRecrawlColumns adds the recrawl bookkeeping column idempotently, so the
// feature works on volumes that predate the 0005 migration (initdb only applies
// migrations on first init).
func (s *Store) EnsureRecrawlColumns(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `
		ALTER TABLE frontier_urls ADD COLUMN IF NOT EXISTS last_fetched_at timestamptz;
		ALTER TABLE frontier_urls ADD COLUMN IF NOT EXISTS etag text;
		ALTER TABLE frontier_urls ADD COLUMN IF NOT EXISTS last_modified text;`)
	return err
}

// RequeueForRecrawl re-enqueues FETCHED URLs last fetched longer than olderThan
// ago back to PENDING (oldest first, up to limit), giving them a fresh retry
// budget so re-crawls keep content current. Returns the number re-enqueued.
func (s *Store) RequeueForRecrawl(ctx context.Context, olderThan time.Duration, limit int) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE frontier_urls
		SET state = 'PENDING', attempts = 0, next_attempt = now(), claimed_at = NULL
		WHERE id IN (
			SELECT id FROM frontier_urls
			WHERE state = 'FETCHED'
			  AND last_fetched_at IS NOT NULL
			  AND last_fetched_at < now() - $1::interval
			ORDER BY last_fetched_at ASC
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)`,
		olderThan.String(), limit)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func (s *Store) MarkSkipped(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE frontier_urls SET state = 'SKIPPED' WHERE id = $1`, id)
	return err
}

// MarkFailed increments attempts. If retry is requested it reschedules the URL,
// but only until maxAttempts is reached, after which it is permanently FAILED.
func (s *Store) MarkFailed(ctx context.Context, id int64, retry bool, maxAttempts int, backoff time.Duration) error {
	if retry {
		_, err := s.pool.Exec(ctx, `
			UPDATE frontier_urls
			SET attempts = attempts + 1,
			    state = CASE WHEN attempts + 1 >= $3 THEN 'FAILED' ELSE 'PENDING' END,
			    next_attempt = CASE WHEN attempts + 1 >= $3 THEN next_attempt ELSE now() + $2 END
			WHERE id = $1`, id, backoff, maxAttempts)
		return err
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE frontier_urls SET state = 'FAILED', attempts = attempts + 1 WHERE id = $1`, id)
	return err
}

// RequeueStuckFetching returns URLs orphaned in FETCHING (e.g. the crawler died
// mid-fetch) back to PENDING so they get retried, but only while under the retry
// cap; those already at the cap are marked FAILED. Returns the number requeued.
// Staleness is measured from claimed_at (set by ClaimNext), so URLs that are
// legitimately mid-fetch are not disturbed.
func (s *Store) RequeueStuckFetching(ctx context.Context, olderThan time.Duration, maxAttempts int) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE frontier_urls
		SET state = CASE WHEN attempts + 1 >= $2 THEN 'FAILED' ELSE 'PENDING' END,
		    attempts = attempts + 1,
		    next_attempt = now()
		WHERE state = 'FETCHING'
		  AND claimed_at IS NOT NULL
		  AND claimed_at < now() - $1::interval`,
		olderThan.String(), maxAttempts)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// FetchedCount returns how many URLs in a campaign have been fetched (for max_pages).
func (s *Store) FetchedCount(ctx context.Context, campaignID int64) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM frontier_urls WHERE campaign_id = $1 AND state = 'FETCHED'`,
		campaignID).Scan(&n)
	return n, err
}

// FrontierStats returns counts by state for a campaign.
func (s *Store) FrontierStats(ctx context.Context, campaignID int64) (map[string]int, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT state, count(*) FROM frontier_urls WHERE campaign_id = $1 GROUP BY state`, campaignID)
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

// ---------------------------------------------------------------- documents ---

type Document struct {
	ID          int64
	URL         string
	FinalURL    string
	SourceID    int64
	HTTPStatus  int
	ContentType string
	ContentHash []byte
	Simhash     int64
	Title       string
	Author      string
	Lang        string
	FetchedAt   time.Time
	BlobKey     string
	Meta        map[string]any
}

// InsertDocument stores a document, skipping exact duplicates (content_hash).
// Returns (id, inserted). inserted=false means it was a duplicate.
func (s *Store) InsertDocument(ctx context.Context, d *Document) (int64, bool, error) {
	meta, err := json.Marshal(d.Meta)
	if err != nil {
		return 0, false, err
	}
	var id int64
	err = s.pool.QueryRow(ctx, `
		INSERT INTO documents
			(url, final_url, source_id, http_status, content_type, content_hash,
			 simhash, title, author, lang, fetched_at, blob_key, meta)
		VALUES ($1,$2,NULLIF($3,0),$4,$5,$6,$7,$8,$9,$10,$11,$12,$13::jsonb)
		ON CONFLICT (content_hash) DO NOTHING
		RETURNING id`,
		d.URL, d.FinalURL, d.SourceID, d.HTTPStatus, d.ContentType, d.ContentHash,
		d.Simhash, d.Title, d.Author, d.Lang, d.FetchedAt, d.BlobKey, string(meta)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil // duplicate
	}
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

// GetDocumentMeta returns a document row for the control API.
func (s *Store) GetDocumentMeta(ctx context.Context, id int64) (map[string]any, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, url, final_url, http_status, content_type, title, author, lang,
		       fetched_at, blob_key, n_chunks
		FROM documents WHERE id = $1`, id)
	var (
		did              int64
		url, final, ct   string
		title, auth, lng string
		status, nchunks  int
		blob             string
		fetched          time.Time
	)
	if err := row.Scan(&did, &url, &final, &status, &ct, &title, &auth, &lng, &fetched, &blob, &nchunks); err != nil {
		return nil, err
	}
	return map[string]any{
		"id": did, "url": url, "final_url": final, "http_status": status,
		"content_type": ct, "title": title, "author": auth, "lang": lng,
		"fetched_at": fetched, "blob_key": blob, "n_chunks": nchunks,
	}, nil
}

// TotalDocuments returns the overall document count (for coverage/stats).
func (s *Store) TotalDocuments(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM documents`).Scan(&n)
	return n, err
}
