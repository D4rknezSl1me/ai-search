package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

// TrackedEntity is a social seed the crawler keeps fresh on a cadence (docs/08
// §7). One row per (adapter, seed); next_due_at is the self-advancing timer the
// freshness scheduler claims from. Optional timestamps are pointers so an
// un-run entity marshals as null rather than the zero time.
type TrackedEntity struct {
	ID             int64           `json:"id"`
	Adapter        string          `json:"adapter"`
	Seed           string          `json:"seed"`
	CadenceSeconds int             `json:"cadence_seconds"`
	MaxPages       int             `json:"max_pages"`
	Enabled        bool            `json:"enabled"`
	NextDueAt      time.Time       `json:"next_due_at"`
	LastIngestedAt *time.Time      `json:"last_ingested_at,omitempty"`
	LastResult     json.RawMessage `json:"last_result,omitempty"`
	LastError      string          `json:"last_error,omitempty"`
	Runs           int             `json:"runs"`
	CreatedAt      time.Time       `json:"created_at"`
}

// trackedEntitiesDDL is the idempotent schema for the tracked-entity registry. It
// mirrors db/migrations/0004_tracked_entities.sql and is executed at startup so
// the feature works on volumes that predate the migration (initdb only runs
// migrations on first init). Keep the two in sync.
const trackedEntitiesDDL = `
CREATE TABLE IF NOT EXISTS tracked_entities (
  id               BIGSERIAL PRIMARY KEY,
  adapter          TEXT NOT NULL,
  seed             TEXT NOT NULL,
  cadence_seconds  INT NOT NULL,
  max_pages        INT NOT NULL DEFAULT 0,
  enabled          BOOLEAN NOT NULL DEFAULT TRUE,
  next_due_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_ingested_at TIMESTAMPTZ,
  last_result      JSONB,
  last_error       TEXT,
  runs             INT NOT NULL DEFAULT 0,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (adapter, seed)
);
CREATE INDEX IF NOT EXISTS tracked_entities_due_idx ON tracked_entities (enabled, next_due_at);`

// EnsureTrackedEntities creates the tracked-entity table if missing. Idempotent;
// safe to call on every startup.
func (s *Store) EnsureTrackedEntities(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, trackedEntitiesDDL)
	return err
}

// UpsertTrackedEntity registers (or updates) a social seed to keep fresh. On a
// repeat (adapter, seed) it updates the cadence/cap, re-enables it, and resets
// next_due_at to now so an operator re-registering forces a prompt refresh. The
// stored row is returned.
func (s *Store) UpsertTrackedEntity(ctx context.Context, adapter, seed string, cadenceSeconds, maxPages int) (TrackedEntity, error) {
	row := s.pool.QueryRow(ctx, `
		INSERT INTO tracked_entities (adapter, seed, cadence_seconds, max_pages, enabled, next_due_at)
		VALUES ($1, $2, $3, $4, TRUE, now())
		ON CONFLICT (adapter, seed) DO UPDATE SET
			cadence_seconds = EXCLUDED.cadence_seconds,
			max_pages       = EXCLUDED.max_pages,
			enabled         = TRUE,
			next_due_at     = now()
		RETURNING id, adapter, seed, cadence_seconds, max_pages, enabled,
		          next_due_at, last_ingested_at, last_result, last_error, runs, created_at`,
		adapter, seed, cadenceSeconds, maxPages)
	return scanTracked(row)
}

// ListTrackedEntities returns every registered entity, soonest-due first.
func (s *Store) ListTrackedEntities(ctx context.Context) ([]TrackedEntity, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, adapter, seed, cadence_seconds, max_pages, enabled,
		       next_due_at, last_ingested_at, last_result, last_error, runs, created_at
		FROM tracked_entities
		ORDER BY next_due_at ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TrackedEntity
	for rows.Next() {
		te, err := scanTracked(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, te)
	}
	return out, rows.Err()
}

// DeleteTrackedEntity removes an entity from the registry. Returns true if a row
// was deleted.
func (s *Store) DeleteTrackedEntity(ctx context.Context, adapter, seed string) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM tracked_entities WHERE adapter = $1 AND seed = $2`, adapter, seed)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ClaimDueTracked atomically claims up to batch entities whose cadence has
// elapsed (enabled AND next_due_at <= now), advancing next_due_at by one cadence
// as it claims so a concurrent tick — or a slow run that outlasts the next tick —
// never double-schedules the same entity (FOR UPDATE SKIP LOCKED, mirroring the
// frontier claim). The caller runs each returned entity and then records the
// outcome via RecordTrackedRun. now is passed in so tests are deterministic.
func (s *Store) ClaimDueTracked(ctx context.Context, now time.Time, batch int) ([]TrackedEntity, error) {
	rows, err := s.pool.Query(ctx, `
		UPDATE tracked_entities t
		SET next_due_at = $1 + make_interval(secs => t.cadence_seconds)
		WHERE t.id IN (
			SELECT id FROM tracked_entities
			WHERE enabled AND next_due_at <= $1
			ORDER BY next_due_at ASC
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		RETURNING t.id, t.adapter, t.seed, t.cadence_seconds, t.max_pages, t.enabled,
		          t.next_due_at, t.last_ingested_at, t.last_result, t.last_error, t.runs, t.created_at`,
		now, batch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TrackedEntity
	for rows.Next() {
		te, err := scanTracked(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, te)
	}
	return out, rows.Err()
}

// RecordTrackedRun stores the outcome of one run: it stamps last_ingested_at,
// increments the completed-run count, and records the run summary and error (an
// empty runErr clears the previous error). next_due_at was already advanced at
// claim time, so this never touches the schedule.
func (s *Store) RecordTrackedRun(ctx context.Context, id int64, summary any, runErr string) error {
	raw, err := json.Marshal(summary)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		UPDATE tracked_entities
		SET last_ingested_at = now(),
		    runs = runs + 1,
		    last_result = $2::jsonb,
		    last_error = NULLIF($3, '')
		WHERE id = $1`, id, string(raw), runErr)
	return err
}

// scanTracked reads one tracked_entities row in the canonical column order shared
// by every query above.
func scanTracked(row pgx.Row) (TrackedEntity, error) {
	var (
		te     TrackedEntity
		result []byte
		errStr *string
	)
	if err := row.Scan(&te.ID, &te.Adapter, &te.Seed, &te.CadenceSeconds, &te.MaxPages,
		&te.Enabled, &te.NextDueAt, &te.LastIngestedAt, &result, &errStr, &te.Runs, &te.CreatedAt); err != nil {
		return TrackedEntity{}, err
	}
	if len(result) > 0 {
		te.LastResult = json.RawMessage(result)
	}
	if errStr != nil {
		te.LastError = *errStr
	}
	return te, nil
}
