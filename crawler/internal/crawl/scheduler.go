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
	res, err := s.fetcher.GetConditional(ctx, item.URL, item.ETag, item.LastModified)
	metrics.FetchDuration.Observe(time.Since(start).Seconds())
	if err != nil {
		metrics.FetchTotal.WithLabelValues("error").Inc()
		s.retryOrFail(ctx, item, "fetch error")
		return
	}
	metrics.FetchStatus.WithLabelValues(metrics.StatusClass(res.Status)).Inc()

	// Conditional GET: an unchanged page (304) needs no re-extraction/indexing.
	// Just refresh its fetch time so the recrawl horizon restarts.
	if res.NotModified {
		metrics.FetchTotal.WithLabelValues("not_modified").Inc()
		metrics.ConditionalNotModified.Inc()
		_ = s.store.MarkFetched(ctx, item.ID)
		return
	}

	// Non-2xx handling: retry 5xx and 429 (rate limited) with backoff, fail other 4xx.
	if res.Status >= 400 {
		rateLimited := res.Status == 429
		if rateLimited {
			metrics.FetchTotal.WithLabelValues("rate_limited").Inc()
		} else {
			metrics.FetchTotal.WithLabelValues("error").Inc()
		}
		retry := res.Status >= 500 || rateLimited
		_ = s.store.MarkFailed(ctx, item.ID, retry, MaxAttempts, backoff(item.Attempts, res.RetryAfter))
		return
	}

	isHTML := fetch.IsHTML(res.ContentType)
	isText := !isHTML && fetch.IsText(res.ContentType)
	if !isHTML && !isText {
		metrics.FetchTotal.WithLabelValues("non_html").Inc()
		_ = s.store.MarkFetched(ctx, item.ID) // visited; binary parsing (PDF/doc) added later
		return
	}

	var doc *extract.Document
	extraMeta := map[string]any{}

	if isHTML {
		metrics.FetchTotal.WithLabelValues("ok").Inc()
		doc, err = extract.FromHTML(res.FinalURL, res.Body)
		if err != nil {
			s.retryOrFail(ctx, item, "extract error")
			return
		}
		// Escalation gate: decide whether this page's real content is locked
		// behind JavaScript and should be re-fetched by the browser worker.
		decision := render.NeedsRender(render.ParseMode(cfg.RenderJS), res.Body, doc.Text, len(doc.Links))
		if decision.Needs {
			for _, r := range decision.Reasons {
				metrics.RenderEscalations.WithLabelValues(r).Inc()
			}
			// Durable hand-off: enqueue the URL for the browser-worker pool.
			// Shallower pages render first (same priority shape as discovery).
			renderPriority := 1.0 / float64(item.Depth+1)
			if added, err := s.store.EnqueueRender(ctx, item.CampaignID, item.URL,
				urlx.Hash(item.URL), item.Host, renderPriority, decision.Reasons); err != nil {
				log.Printf("render enqueue error: %v", err)
			} else if added {
				metrics.RenderQueueEnqueued.Inc()
			}
			extraMeta["needs_render"] = true
			extraMeta["render_reasons"] = decision.Reasons
		}
	} else {
		// Plain-text (non-HTML) content: the body IS the text, so text/plain,
		// markdown, csv, logs, etc. reach the index instead of being dropped
		// (recall-first). No link discovery or render escalation applies.
		metrics.FetchTotal.WithLabelValues("text").Inc()
		doc, err = extract.FromPlainText(res.FinalURL, res.Body)
		if err != nil {
			s.retryOrFail(ctx, item, "extract error")
			return
		}
	}

	s.index(ctx, item, res, doc, extraMeta)
	// Persist HTTP validators so the next recrawl can issue a conditional GET.
	_ = s.store.SetValidators(ctx, item.ID, res.ETag, res.LastModified)
	if isHTML {
		s.discover(ctx, item, cfg, doc.Links)
	}
	_ = s.store.MarkFetched(ctx, item.ID)
}

// index persists a fetched document — raw + clean-text blobs (idempotent, keyed
// by content hash, so re-crawls backfill text for known rows), then the row.
// Shared by the HTML and plain-text paths.
func (s *Scheduler) index(ctx context.Context, item store.FrontierItem, res *fetch.Result, doc *extract.Document, extraMeta map[string]any) {
	blobKey, err := s.blob.PutRaw(ctx, doc.ContentHash, res.Body)
	if err != nil {
		log.Printf("blob put error: %v", err)
	}
	if doc.Text != "" {
		if _, err := s.blob.PutText(ctx, doc.ContentHash, doc.Text); err != nil {
			log.Printf("blob put text error: %v", err)
		}
	}

	sourceID, _ := s.store.EnsureSource(ctx, item.Host)
	meta := map[string]any{
		"excerpt":   doc.Excerpt,
		"site_name": doc.SiteName,
		"text_len":  len(doc.Text),
		"truncated": res.Truncated,
	}
	for k, v := range extraMeta {
		meta[k] = v
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
	_ = s.store.MarkFailed(ctx, item.ID, true, MaxAttempts, backoff(item.Attempts, 0))
}

const (
	backoffBase = 30 * time.Second
	backoffMax  = 30 * time.Minute
)

// backoff computes the reschedule delay for a failed fetch: exponential in the
// number of prior attempts (30s, 1m, 2m, … capped at 30m), but never shorter
// than a server-provided Retry-After (also capped). Honoring Retry-After keeps
// us polite with rate-limiting origins so their URLs stay reachable (recall).
func backoff(attempts int, retryAfter time.Duration) time.Duration {
	shift := attempts
	if shift > 6 {
		shift = 6 // cap the shift so 30s<<shift can't overflow / exceed the max
	}
	d := backoffBase << uint(shift)
	if d > backoffMax {
		d = backoffMax
	}
	if retryAfter > d {
		d = retryAfter
	}
	if d > backoffMax {
		d = backoffMax
	}
	return d
}
