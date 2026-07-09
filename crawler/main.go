// Command crawler is the ai-search ingestion service (Phase 1 — Crawler MVP).
//
// It seeds a campaign, then runs a fetch → extract → store → discover loop over
// the open web, persisting documents to Postgres/MinIO. Control and health are
// exposed over HTTP. See docs/04-CRAWLER.md and docs/12-ROADMAP.md.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ai-search/crawler/internal/api"
	"github.com/ai-search/crawler/internal/blob"
	"github.com/ai-search/crawler/internal/config"
	"github.com/ai-search/crawler/internal/crawl"
	"github.com/ai-search/crawler/internal/fetch"
	"github.com/ai-search/crawler/internal/social"
	"github.com/ai-search/crawler/internal/store"
)

func main() {
	cfg := config.Load()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Connect datastores (each retries until reachable).
	log.Printf("connecting to Postgres at %s:%s ...", cfg.PGHost, cfg.PGPort)
	st, err := store.New(ctx, cfg.PGConnString())
	if err != nil {
		log.Fatalf("postgres: %v", err)
	}
	defer st.Close()

	// Ensure the browser render queue exists (idempotent; covers volumes that
	// predate the 0003 migration, which initdb only applies on first init).
	if err := st.EnsureRenderQueue(ctx); err != nil {
		log.Fatalf("ensure render_queue: %v", err)
	}

	log.Printf("connecting to MinIO at %s ...", cfg.MinIOEndpoint())
	bl, err := blob.New(ctx, cfg.MinIOEndpoint(), cfg.MinIOUser, cfg.MinIOPassword, cfg.MinIOBucket)
	if err != nil {
		log.Fatalf("minio: %v", err)
	}

	// Crawl engine.
	fetcher := fetch.New(time.Duration(cfg.FetchTimeout)*time.Second, cfg.MaxBodyBytes, cfg.UserAgent)
	limiter := crawl.NewHostLimiter(time.Duration(cfg.MinDelayMs) * time.Millisecond)
	scheduler := crawl.NewScheduler(st, bl, fetcher, limiter, cfg.Workers)

	go scheduler.Run(ctx)
	log.Printf("crawl scheduler started with %d workers (min host delay %dms)", cfg.Workers, cfg.MinDelayMs)

	// Reaper: requeue URLs orphaned in FETCHING (worker died mid-fetch).
	go runReaper(ctx, st, time.Duration(cfg.ReapAfterS)*time.Second)

	// Social adapter registry (credential-free platforms). Exposed for health
	// monitoring via the control API; login-walled adapters are added once the
	// owner provides credentials (ralph/QUESTIONS.md).
	socialReg := social.DefaultRegistry(cfg.UserAgent, time.Duration(cfg.FetchTimeout)*time.Second, 0)
	log.Printf("social adapters registered: %v", socialReg.Names())

	// HTTP control/health server.
	srv := &http.Server{
		Addr:              ":" + cfg.HealthPort,
		Handler:           api.NewServer(st, bl, socialReg).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		log.Printf("control API listening on :%s", cfg.HealthPort)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http server: %v", err)
		}
	}()

	// Graceful shutdown.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Printf("shutting down ...")
	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = srv.Shutdown(shutdownCtx)
}

// runReaper periodically requeues URLs stuck in FETCHING beyond reapAfter. The
// check interval is a fraction of the timeout so orphans are caught promptly.
func runReaper(ctx context.Context, st *store.Store, reapAfter time.Duration) {
	if reapAfter <= 0 {
		return
	}
	interval := reapAfter / 2
	if interval < 30*time.Second {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := st.RequeueStuckFetching(ctx, reapAfter, crawl.MaxAttempts)
			if err != nil {
				log.Printf("reaper error: %v", err)
			} else if n > 0 {
				log.Printf("reaper requeued %d stuck FETCHING url(s)", n)
			}
			// Same treatment for the render lane: browser workers that crashed
			// mid-render leave jobs orphaned in RENDERING.
			rn, err := st.RequeueStuckRendering(ctx, reapAfter, crawl.MaxAttempts)
			if err != nil {
				log.Printf("render reaper error: %v", err)
			} else if rn > 0 {
				log.Printf("reaper requeued %d stuck RENDERING job(s)", rn)
			}
		}
	}
}
