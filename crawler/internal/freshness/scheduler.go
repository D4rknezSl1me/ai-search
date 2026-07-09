// Package freshness runs the recrawl cadence for tracked social entities
// (Phase 3, docs/08 §7). Social content is time-sensitive, so a registered
// (adapter, seed) — a hashtag, profile, or instance worth watching — is
// re-ingested on its own cadence rather than only when an operator triggers it.
//
// The scheduler is a small ticker loop over the store's tracked_entities table:
// every tick it claims the entities whose cadence has elapsed (the store advances
// their next_due_at as it claims, so a slow run never double-schedules) and
// re-runs the same social ingester used by POST /internal/social/ingest. Re-run
// posts dedupe by content hash, so keeping an entity fresh is idempotent at the
// document level — only genuinely new posts land.
package freshness

import (
	"context"
	"log"
	"time"

	"github.com/ai-search/crawler/internal/metrics"
	"github.com/ai-search/crawler/internal/social"
	"github.com/ai-search/crawler/internal/store"
)

// Store is the slice of the Postgres layer the scheduler needs. Kept narrow so
// the loop can be unit-tested with a fake (no live database).
type Store interface {
	ClaimDueTracked(ctx context.Context, now time.Time, batch int) ([]store.TrackedEntity, error)
	RecordTrackedRun(ctx context.Context, id int64, summary any, runErr string) error
}

// RunFunc ingests one tracked entity. main wires this to a social.Ingester built
// with the entity's page cap; the scheduler stays free of adapter/sink wiring so
// it is trivially testable.
type RunFunc func(ctx context.Context, adapter, seed string, maxPages int) (social.Result, error)

// Scheduler re-ingests due tracked entities on a fixed tick.
type Scheduler struct {
	store Store
	run   RunFunc
	tick  time.Duration
	batch int
	now   func() time.Time // injectable clock for tests
}

// New builds a scheduler. tick is how often the loop wakes to look for due
// entities; batch bounds how many are claimed per tick (a fairness/backpressure
// cap). Non-positive tick/batch fall back to sane defaults.
func New(st Store, run RunFunc, tick time.Duration, batch int) *Scheduler {
	if tick <= 0 {
		tick = 30 * time.Second
	}
	if batch <= 0 {
		batch = 16
	}
	return &Scheduler{store: st, run: run, tick: tick, batch: batch, now: time.Now}
}

// Run drives the loop until ctx is cancelled. It ticks immediately once so a
// freshly registered entity (next_due_at = now) is picked up promptly rather than
// waiting a full tick.
func (s *Scheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(s.tick)
	defer ticker.Stop()
	s.runDue(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runDue(ctx)
		}
	}
}

// runDue claims and re-ingests every entity due at the current instant, returning
// how many it processed. Each run's outcome is recorded (result summary + error)
// so the registry surfaces freshness lag and breakage — "fail loud, don't
// silently under-collect" (docs/08 §8). One entity's failure never stops the rest.
func (s *Scheduler) runDue(ctx context.Context) int {
	due, err := s.store.ClaimDueTracked(ctx, s.now(), s.batch)
	if err != nil {
		log.Printf("freshness: claim due tracked entities: %v", err)
		return 0
	}
	for _, e := range due {
		if ctx.Err() != nil {
			return len(due)
		}
		res, runErr := s.run(ctx, e.Adapter, e.Seed, e.MaxPages)
		errStr := ""
		outcome := "ok"
		if runErr != nil {
			errStr = runErr.Error()
			outcome = "error"
			log.Printf("freshness: ingest %s/%q failed: %v", e.Adapter, e.Seed, runErr)
		} else {
			log.Printf("freshness: refreshed %s/%q — %d inserted, %d duplicate, %d errors",
				e.Adapter, e.Seed, res.Inserted, res.Duplicates, res.Errors)
		}
		metrics.FreshnessRuns.WithLabelValues(e.Adapter, outcome).Inc()
		if err := s.store.RecordTrackedRun(ctx, e.ID, res, errStr); err != nil {
			log.Printf("freshness: record run for id=%d: %v", e.ID, err)
		}
	}
	return len(due)
}
