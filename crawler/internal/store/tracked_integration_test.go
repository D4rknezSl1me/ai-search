//go:build integration

// Integration test for the tracked-entity registry. Requires a live Postgres; run:
//
//	go test -tags integration ./internal/store/ -run TrackedEntity
//
// DSN comes from POSTGRES_DSN (e.g. postgres://user:pw@postgres:5432/aisearch?sslmode=disable).
package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestTrackedEntityLifecycle(t *testing.T) {
	ctx := context.Background()
	st, err := New(ctx, testDSN(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer st.Close()

	if err := st.EnsureTrackedEntities(ctx); err != nil {
		t.Fatalf("ensure tracked_entities: %v", err)
	}

	// Unique seeds per run so the shared table stays deterministic across reruns.
	seedA := fmt.Sprintf("#it-a-%d", time.Now().UnixNano())
	seedB := fmt.Sprintf("#it-b-%d", time.Now().UnixNano())

	// Register two entities (default next_due_at = now → both immediately due).
	teA, err := st.UpsertTrackedEntity(ctx, "mastodon", seedA, 900, 3)
	if err != nil {
		t.Fatalf("upsert A: %v", err)
	}
	if teA.CadenceSeconds != 900 || teA.MaxPages != 3 || !teA.Enabled {
		t.Fatalf("upsert A round-trip: %+v", teA)
	}
	if _, err := st.UpsertTrackedEntity(ctx, "lemmy", seedB, 900, 0); err != nil {
		t.Fatalf("upsert B: %v", err)
	}

	// Re-upsert A with a new cadence: same row (no duplicate), cadence updated.
	teA2, err := st.UpsertTrackedEntity(ctx, "mastodon", seedA, 1800, 5)
	if err != nil {
		t.Fatalf("re-upsert A: %v", err)
	}
	if teA2.ID != teA.ID {
		t.Fatalf("re-upsert created a new row: %d != %d", teA2.ID, teA.ID)
	}
	if teA2.CadenceSeconds != 1800 || teA2.MaxPages != 5 {
		t.Fatalf("re-upsert did not update cadence/cap: %+v", teA2)
	}

	// Claim due entities as of now → both A and B come back, and next_due_at is
	// advanced by one cadence so an immediate re-claim returns them no more.
	now := time.Now()
	due, err := st.ClaimDueTracked(ctx, now, 50)
	if err != nil {
		t.Fatalf("claim due: %v", err)
	}
	gotA, gotB := false, false
	for _, e := range due {
		switch e.Seed {
		case seedA:
			gotA = true
			if !e.NextDueAt.After(now.Add(1700 * time.Second)) {
				t.Fatalf("A next_due_at not advanced by cadence: %v", e.NextDueAt)
			}
		case seedB:
			gotB = true
		}
	}
	if !gotA || !gotB {
		t.Fatalf("claim did not return both due entities (A=%v B=%v)", gotA, gotB)
	}
	// Immediate re-claim at the same instant: nothing due (double-schedule guard).
	again, err := st.ClaimDueTracked(ctx, now, 50)
	if err != nil {
		t.Fatalf("re-claim: %v", err)
	}
	for _, e := range again {
		if e.Seed == seedA || e.Seed == seedB {
			t.Fatalf("entity re-claimed at same instant (should be scheduled forward): %s", e.Seed)
		}
	}

	// Record a run for A → last_ingested_at + runs++ + last_result stored.
	summary := map[string]int{"inserted": 4, "duplicates": 1}
	if err := st.RecordTrackedRun(ctx, teA.ID, summary, ""); err != nil {
		t.Fatalf("record run: %v", err)
	}
	list, err := st.ListTrackedEntities(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var found *TrackedEntity
	for i := range list {
		if list[i].ID == teA.ID {
			found = &list[i]
		}
	}
	if found == nil {
		t.Fatalf("entity A not in list")
	}
	if found.Runs != 1 || found.LastIngestedAt == nil || len(found.LastResult) == 0 {
		t.Fatalf("run not recorded: %+v", found)
	}

	// Delete both; second delete of A reports not-removed.
	if removed, _ := st.DeleteTrackedEntity(ctx, "mastodon", seedA); !removed {
		t.Fatalf("delete A: expected removed")
	}
	if removed, _ := st.DeleteTrackedEntity(ctx, "mastodon", seedA); removed {
		t.Fatalf("delete A twice: expected not removed")
	}
	if removed, _ := st.DeleteTrackedEntity(ctx, "lemmy", seedB); !removed {
		t.Fatalf("delete B: expected removed")
	}
}
