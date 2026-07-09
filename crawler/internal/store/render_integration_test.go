//go:build integration

// Integration test for the render queue. Requires a live Postgres; run with:
//
//	go test -tags integration ./internal/store/ -run RenderQueue
//
// DSN comes from POSTGRES_DSN (e.g. postgres://user:pw@postgres:5432/aisearch?sslmode=disable).
package store

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"testing"
	"time"
)

func testDSN(t *testing.T) string {
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_DSN not set; skipping integration test")
	}
	return dsn
}

func TestRenderQueueLifecycle(t *testing.T) {
	ctx := context.Background()
	st, err := New(ctx, testDSN(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer st.Close()

	if err := st.EnsureRenderQueue(ctx); err != nil {
		t.Fatalf("ensure render queue: %v", err)
	}

	// Isolate this run under its own campaign so counts are deterministic.
	campaign := fmt.Sprintf("render-it-%d", time.Now().UnixNano())
	campaignID, err := st.UpsertCampaign(ctx, campaign, CampaignConfig{MaxDepth: 1, RenderJS: "always"})
	if err != nil {
		t.Fatalf("upsert campaign: %v", err)
	}

	hash := func(u string) []byte { h := sha256.Sum256([]byte(u)); return h[:] }
	urlA := "https://example.com/spa-a"
	urlB := "https://example.com/spa-b"

	// Enqueue two URLs; the second twice to prove dedup.
	added, err := st.EnqueueRender(ctx, campaignID, urlA, hash(urlA), "example.com", 1.0, []string{"spa_root_marker"})
	if err != nil || !added {
		t.Fatalf("enqueue A: added=%v err=%v", added, err)
	}
	if added, _ := st.EnqueueRender(ctx, campaignID, urlB, hash(urlB), "example.com", 0.5, []string{"noscript_prompt"}); !added {
		t.Fatalf("enqueue B: expected added")
	}
	if added, _ := st.EnqueueRender(ctx, campaignID, urlB, hash(urlB), "example.com", 0.5, []string{"noscript_prompt"}); added {
		t.Fatalf("enqueue B twice: expected dedup (added=false)")
	}

	stats, err := st.RenderQueueStats(ctx, campaignID)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats["PENDING"] != 2 {
		t.Fatalf("want 2 PENDING, got %v", stats)
	}

	// Claim: higher-priority urlA comes first.
	items, err := st.ClaimNextRender(ctx, 1)
	if err != nil || len(items) != 1 {
		t.Fatalf("claim: items=%d err=%v", len(items), err)
	}
	if items[0].URL != urlA {
		t.Fatalf("priority order: want %s first, got %s", urlA, items[0].URL)
	}
	if len(items[0].Reasons) != 1 || items[0].Reasons[0] != "spa_root_marker" {
		t.Fatalf("reasons not round-tripped: %v", items[0].Reasons)
	}
	stats, _ = st.RenderQueueStats(ctx, campaignID)
	if stats["RENDERING"] != 1 || stats["PENDING"] != 1 {
		t.Fatalf("after claim want 1 RENDERING/1 PENDING, got %v", stats)
	}

	// Complete the claimed job successfully.
	if err := st.MarkRendered(ctx, items[0].ID); err != nil {
		t.Fatalf("mark rendered: %v", err)
	}

	// Claim + fail (no retry) the second job → FAILED.
	items2, err := st.ClaimNextRender(ctx, 5)
	if err != nil || len(items2) != 1 {
		t.Fatalf("claim 2: items=%d err=%v", len(items2), err)
	}
	if err := st.MarkRenderFailed(ctx, items2[0].ID, false, MaxRenderAttemptsForTest, time.Minute); err != nil {
		t.Fatalf("mark failed: %v", err)
	}

	stats, _ = st.RenderQueueStats(ctx, campaignID)
	if stats["RENDERED"] != 1 || stats["FAILED"] != 1 {
		t.Fatalf("final want 1 RENDERED/1 FAILED, got %v", stats)
	}
	if stats["PENDING"] != 0 || stats["RENDERING"] != 0 {
		t.Fatalf("final want queue drained, got %v", stats)
	}
}

// MaxRenderAttemptsForTest mirrors crawl.MaxAttempts without importing the crawl
// package (which would pull the whole scheduler into the store test binary).
const MaxRenderAttemptsForTest = 3
