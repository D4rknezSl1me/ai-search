//go:build integration

// Integration test for recrawl re-enqueuing. Requires a live Postgres; run:
//
//	go test -tags integration ./internal/store/ -run Recrawl
//
// DSN comes from POSTGRES_DSN.
package store

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"
	"time"
)

func hashOf(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}

func TestRecrawlRequeue(t *testing.T) {
	ctx := context.Background()
	st, err := New(ctx, testDSN(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer st.Close()

	if err := st.EnsureRecrawlColumns(ctx); err != nil {
		t.Fatalf("ensure recrawl columns: %v", err)
	}

	// A throwaway campaign + two URLs so we don't disturb real data.
	campaign, err := st.UpsertCampaign(ctx, fmt.Sprintf("recrawl-it-%d", time.Now().UnixNano()), map[string]any{})
	if err != nil {
		t.Fatalf("campaign: %v", err)
	}
	stale := fmt.Sprintf("https://recrawl.test/stale-%d", time.Now().UnixNano())
	fresh := fmt.Sprintf("https://recrawl.test/fresh-%d", time.Now().UnixNano())
	for _, u := range []string{stale, fresh} {
		if _, err := st.AddURL(ctx, campaign, u, hashOf(u), "recrawl.test", 0, 1.0); err != nil {
			t.Fatalf("add %s: %v", u, err)
		}
	}

	// Claim + mark both fetched (MarkFetched stamps last_fetched_at = now()).
	items, err := st.ClaimNext(ctx, 10)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	var staleID int64
	for _, it := range items {
		if err := st.MarkFetched(ctx, it.ID); err != nil {
			t.Fatalf("mark fetched: %v", err)
		}
		if it.URL == stale {
			staleID = it.ID
		}
	}
	if staleID == 0 {
		t.Fatal("stale URL was not claimed/fetched")
	}

	// Backdate the stale one an hour into the past.
	if _, err := st.pool.Exec(ctx,
		`UPDATE frontier_urls SET last_fetched_at = now() - interval '1 hour' WHERE id = $1`, staleID); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	// Recrawl horizon of 30m: only the backdated URL is due.
	n, err := st.RequeueForRecrawl(ctx, 30*time.Minute, 100)
	if err != nil {
		t.Fatalf("requeue: %v", err)
	}
	if n != 1 {
		t.Fatalf("requeued %d, want exactly 1 (only the stale URL)", n)
	}

	// The stale URL is PENDING again with a reset attempt budget; fresh stays FETCHED.
	var state string
	var attempts int
	if err := st.pool.QueryRow(ctx,
		`SELECT state, attempts FROM frontier_urls WHERE id = $1`, staleID).Scan(&state, &attempts); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if state != "PENDING" || attempts != 0 {
		t.Fatalf("stale URL state=%s attempts=%d, want PENDING/0", state, attempts)
	}

	// A second immediate recrawl finds nothing new (the stale one is no longer FETCHED).
	if n2, err := st.RequeueForRecrawl(ctx, 30*time.Minute, 100); err != nil || n2 != 0 {
		t.Fatalf("second requeue n=%d err=%v, want 0/nil", n2, err)
	}
}
