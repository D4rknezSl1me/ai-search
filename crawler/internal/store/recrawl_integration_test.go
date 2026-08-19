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

	// Recrawl horizon of 30m re-enqueues stale FETCHED rows. This runs against the
	// shared frontier the live crawler also uses, so assert per-URL effects rather
	// than a global count (other rows may legitimately be due).
	freshID := int64(0)
	if err := st.pool.QueryRow(ctx,
		`SELECT id FROM frontier_urls WHERE url = $1`, fresh).Scan(&freshID); err != nil {
		t.Fatalf("find fresh: %v", err)
	}
	n, err := st.RequeueForRecrawl(ctx, 30*time.Minute, 1000)
	if err != nil {
		t.Fatalf("requeue: %v", err)
	}
	if n < 1 {
		t.Fatalf("requeued %d, want at least the stale URL", n)
	}

	// The stale URL left FETCHED (re-enqueued); the fresh one is untouched. Note a
	// live worker may immediately re-claim the (unresolvable) stale URL, so we only
	// assert it is no longer the stale FETCHED row — not a specific transient state.
	var staleState, freshState string
	if err := st.pool.QueryRow(ctx, `SELECT state FROM frontier_urls WHERE id=$1`, staleID).Scan(&staleState); err != nil {
		t.Fatalf("read stale: %v", err)
	}
	if err := st.pool.QueryRow(ctx, `SELECT state FROM frontier_urls WHERE id=$1`, freshID).Scan(&freshState); err != nil {
		t.Fatalf("read fresh: %v", err)
	}
	if staleState == "FETCHED" {
		t.Fatalf("stale URL still FETCHED; expected it to be re-enqueued")
	}
	if freshState != "FETCHED" {
		t.Fatalf("fresh URL state=%s, want FETCHED (not stale, must not be recrawled)", freshState)
	}
}
