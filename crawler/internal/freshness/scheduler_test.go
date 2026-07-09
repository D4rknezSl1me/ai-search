package freshness

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ai-search/crawler/internal/social"
	"github.com/ai-search/crawler/internal/store"
)

// fakeStore is an in-memory stand-in for the tracked-entity table. ClaimDueTracked
// returns whatever is currently due and (like the real SQL) advances next_due_at
// by one cadence as it claims, so a second claim at the same instant returns
// nothing — this is what proves the scheduler never double-schedules.
type fakeStore struct {
	mu       sync.Mutex
	entities []store.TrackedEntity
	recorded map[int64]recordedRun
}

type recordedRun struct {
	summary any
	runErr  string
}

func (f *fakeStore) ClaimDueTracked(_ context.Context, now time.Time, batch int) ([]store.TrackedEntity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var due []store.TrackedEntity
	for i := range f.entities {
		e := &f.entities[i]
		if !e.Enabled || e.NextDueAt.After(now) {
			continue
		}
		if len(due) >= batch {
			break
		}
		e.NextDueAt = now.Add(time.Duration(e.CadenceSeconds) * time.Second)
		due = append(due, *e)
	}
	return due, nil
}

func (f *fakeStore) RecordTrackedRun(_ context.Context, id int64, summary any, runErr string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.recorded == nil {
		f.recorded = map[int64]recordedRun{}
	}
	f.recorded[id] = recordedRun{summary: summary, runErr: runErr}
	return nil
}

func TestRunDueRefreshesDueEntities(t *testing.T) {
	now := time.Now()
	fs := &fakeStore{entities: []store.TrackedEntity{
		{ID: 1, Adapter: "mastodon", Seed: "#rust", CadenceSeconds: 900, MaxPages: 2, Enabled: true, NextDueAt: now.Add(-time.Minute)},
		{ID: 2, Adapter: "hackernews", Seed: "top", CadenceSeconds: 900, MaxPages: 0, Enabled: true, NextDueAt: now.Add(time.Hour)},      // not due
		{ID: 3, Adapter: "lemmy", Seed: "programming", CadenceSeconds: 900, MaxPages: 1, Enabled: false, NextDueAt: now.Add(-time.Hour)}, // disabled
	}}

	var mu sync.Mutex
	var ran []string
	var gotMaxPages int
	run := func(_ context.Context, adapter, seed string, maxPages int) (social.Result, error) {
		mu.Lock()
		defer mu.Unlock()
		ran = append(ran, adapter+"/"+seed)
		if adapter == "mastodon" {
			gotMaxPages = maxPages
		}
		return social.Result{Inserted: 3, Duplicates: 1}, nil
	}

	s := New(fs, run, time.Minute, 16)
	s.now = func() time.Time { return now }

	if n := s.runDue(context.Background()); n != 1 {
		t.Fatalf("expected 1 due entity processed, got %d", n)
	}
	if len(ran) != 1 || ran[0] != "mastodon/#rust" {
		t.Fatalf("expected only the due, enabled entity to run, got %v", ran)
	}
	if gotMaxPages != 2 {
		t.Fatalf("expected per-entity max_pages=2 threaded to the runner, got %d", gotMaxPages)
	}
	if rec, ok := fs.recorded[1]; !ok || rec.runErr != "" {
		t.Fatalf("expected a clean recorded run for id=1, got %+v (ok=%v)", rec, ok)
	}

	// A second tick at the same instant must find nothing due: the claim advanced
	// next_due_at by a full cadence, so the entity is not re-scheduled.
	if n := s.runDue(context.Background()); n != 0 {
		t.Fatalf("expected 0 due on the immediate re-tick, got %d", n)
	}
}

func TestRunDueRecordsFailureButContinues(t *testing.T) {
	now := time.Now()
	fs := &fakeStore{entities: []store.TrackedEntity{
		{ID: 1, Adapter: "mastodon", Seed: "#a", CadenceSeconds: 60, Enabled: true, NextDueAt: now.Add(-time.Second)},
		{ID: 2, Adapter: "lemmy", Seed: "b", CadenceSeconds: 60, Enabled: true, NextDueAt: now.Add(-time.Second)},
	}}

	run := func(_ context.Context, adapter, _ string, _ int) (social.Result, error) {
		if adapter == "mastodon" {
			return social.Result{}, errors.New("boom")
		}
		return social.Result{Inserted: 5}, nil
	}

	s := New(fs, run, time.Minute, 16)
	s.now = func() time.Time { return now }

	if n := s.runDue(context.Background()); n != 2 {
		t.Fatalf("expected both entities processed despite one failure, got %d", n)
	}
	if rec := fs.recorded[1]; rec.runErr != "boom" {
		t.Fatalf("expected failure recorded for id=1, got %q", rec.runErr)
	}
	if rec := fs.recorded[2]; rec.runErr != "" {
		t.Fatalf("expected clean run recorded for id=2, got %q", rec.runErr)
	}
}
