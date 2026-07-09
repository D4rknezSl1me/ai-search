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

	// HTTP control/health server.
	srv := &http.Server{
		Addr:              ":" + cfg.HealthPort,
		Handler:           api.NewServer(st, bl).Handler(),
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
