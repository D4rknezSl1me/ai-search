// Package crawl orchestrates the fetch → extract → store → discover loop.
package crawl

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/ai-search/crawler/internal/blob"
	"github.com/ai-search/crawler/internal/extract"
	"github.com/ai-search/crawler/internal/fetch"
	"github.com/ai-search/crawler/internal/metrics"
	"github.com/ai-search/crawler/internal/render"
	"github.com/ai-search/crawler/internal/store"
	"github.com/ai-search/crawler/internal/urlx"
)

// MaxAttempts caps how many times a URL is retried before being marked FAILED.
const MaxAttempts = 3

type Scheduler struct {
	store   *store.Store
	blob    *blob.Store
	fetcher *fetch.Fetcher
	limiter *HostLimiter
	workers int

	campaignMu sync.Mutex
	campaigns  map[int64]store.CampaignConfig
}

func NewScheduler(st *store.Store, bl *blob.Store, f *fetch.Fetcher, limiter *HostLimiter, workers int) *Scheduler {
	return &Scheduler{
		store:     st,
		blob:      bl,
		fetcher:   f,
		limiter:   limiter,
		workers:   workers,
		campaigns: make(map[int64]store.CampaignConfig),
	}
}

// Run starts the dispatcher and worker pool until ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context) {
	jobs := make(chan store.FrontierItem, s.workers*2)

	var wg sync.WaitGroup
	for i := 0; i < s.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range jobs {
				s.process(ctx, item)
			}
		}()
	}

	// Dispatcher: claim eligible URLs and feed the workers.
	for {
		select {
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return
		default:
		}

		items, err := s.store.ClaimNext(ctx, s.workers)
		if err != nil {
			log.Printf("claim error: %v", err)
			time.Sleep(time.Second)
			continue
		}
		if len(items) == 0 {
			time.Sleep(500 * time.Millisecond) // idle backoff
			continue
		}
		for _, it := range items {
			jobs <- it
		}
	}
}

func (s *Scheduler) campaignConfig(ctx context.Context, id int64) store.CampaignConfig {
	s.campaignMu.Lock()
	defer s.campaignMu.Unlock()
	if cfg, ok := s.campaigns[id]; ok {
		return cfg
	}
	c, err := s.store.GetCampaign(ctx, id)
	if err != nil {
		return store.CampaignConfig{MaxDepth: 2, MaxPages: 1000}
	}
	s.campaigns[id] = c.Config
	return c.Config
}

func (s *Scheduler) process(ctx context.Context, item store.FrontierItem) {
	cfg := s.campaignConfig(ctx, item.CampaignID)

	// Respect max_pages (best-effort; counted from fetched URLs).
	if cfg.MaxPages > 0 {
		if n, err := s.store.FetchedCount(ctx, item.CampaignID); err == nil && n >= cfg.MaxPages {
			_ = s.store.MarkSkipped(ctx, item.ID)
			return
		}
	}

	// Politeness: wait our per-host slot.
	if wait := s.limiter.Reserve(item.Host); wait > 0 {
		select {
		case <-ctx.Done():
			_ = s.store.MarkFailed(ctx, item.ID, true, MaxAttempts, time.Second)
			return
		case <-time.After(wait):
		}
	}

	start := time.Now()
	res, err := s.fetcher.Get(ctx, item.URL)
	metrics.FetchDuration.Observe(time.Since(start).Seconds())
	if err != nil {
		metrics.FetchTotal.WithLabelValues("error").Inc()
		s.retryOrFail(ctx, item, "fetch error")
		return
	}
	metrics.FetchStatus.WithLabelValues(metrics.StatusClass(res.Status)).Inc()

	// Non-2xx handling: retry 5xx, fail 4xx.
	if res.Status >= 400 {
		metrics.FetchTotal.WithLabelValues("error").Inc()
		retry := res.Status >= 500
		_ = s.store.MarkFailed(ctx, item.ID, retry, MaxAttempts, backoff(item))
		return
	}

	if !fetch.IsHTML(res.ContentType) {
		metrics.FetchTotal.WithLabelValues("non_html").Inc()
		_ = s.store.MarkFetched(ctx, item.ID) // recorded as visited; parsing added later
		return
	}
	metrics.FetchTotal.WithLabelValues("ok").Inc()

	doc, err := extract.FromHTML(res.FinalURL, res.Body)
	if err != nil {
		s.retryOrFail(ctx, item, "extract error")
		return
	}

	// Store raw content (gzip) keyed by content hash.
	blobKey, err := s.blob.PutRaw(ctx, doc.ContentHash, res.Body)
	if err != nil {
		log.Printf("blob put error: %v", err)
	}

	// Persist the clean extracted text (idempotent, keyed by content hash) so
	// the intelligence plane can chunk/embed it. Written for every extracted
	// document — including re-crawls of already-known content — and NOT gated by
	// the insert-dedup below, so text is available even for pre-existing rows.
	if doc.Text != "" {
		if _, err := s.blob.PutText(ctx, doc.ContentHash, doc.Text); err != nil {
			log.Printf("blob put text error: %v", err)
		}
	}

	// Escalation gate: decide whether this page's real content is locked behind
	// JavaScript and should be re-fetched by the browser worker. Recorded in
	// metrics + document meta now; the Playwright pool consumes it next.
	decision := render.NeedsRender(render.ParseMode(cfg.RenderJS), res.Body, doc.Text, len(doc.Links))
	if decision.Needs {
		for _, r := range decision.Reasons {
			metrics.RenderEscalations.WithLabelValues(r).Inc()
		}
	}

	sourceID, _ := s.store.EnsureSource(ctx, item.Host)

	meta := map[string]any{
		"excerpt":   doc.Excerpt,
		"site_name": doc.SiteName,
		"text_len":  len(doc.Text),
		"truncated": res.Truncated,
	}
	if decision.Needs {
		meta["needs_render"] = true
		meta["render_reasons"] = decision.Reasons
	}

	_, inserted, err := s.store.InsertDocument(ctx, &store.Document{
		URL:         item.URL,
		FinalURL:    res.FinalURL,
		SourceID:    sourceID,
		HTTPStatus:  res.Status,
		ContentType: res.ContentType,
		ContentHash: doc.ContentHash,
		Simhash:     int64(doc.Simhash),
		Title:       doc.Title,
		Author:      doc.Author,
		Lang:        doc.Lang,
		FetchedAt:   time.Now().UTC(),
		BlobKey:     blobKey,
		Meta:        meta,
	})
	if err != nil {
		log.Printf("insert document error: %v", err)
	} else if inserted {
		metrics.DocsIndexed.Inc()
	} else {
		metrics.DocsDuplicate.Inc()
	}

	s.discover(ctx, item, cfg, doc.Links)
	_ = s.store.MarkFetched(ctx, item.ID)
}

// discover enqueues in-scope outlinks at depth+1.
func (s *Scheduler) discover(ctx context.Context, item store.FrontierItem, cfg store.CampaignConfig, links []string) {
	if cfg.MaxDepth > 0 && item.Depth >= cfg.MaxDepth {
		return
	}
	priority := 1.0 / float64(item.Depth+2)
	for _, link := range links {
		host := urlx.Host(link)
		if host == "" {
			continue
		}
		if !cfg.AllowExternal && !urlx.SameRegisteredDomain(host, item.Host) {
			continue
		}
		hash := urlx.Hash(link)
		added, err := s.store.AddURL(ctx, item.CampaignID, link, hash, host, item.Depth+1, priority)
		if err == nil && added {
			metrics.LinksDiscovered.Inc()
		}
	}
}

func (s *Scheduler) retryOrFail(ctx context.Context, item store.FrontierItem, reason string) {
	_ = s.store.MarkFailed(ctx, item.ID, true, MaxAttempts, backoff(item))
}

func backoff(item store.FrontierItem) time.Duration {
	return 30 * time.Second
}
