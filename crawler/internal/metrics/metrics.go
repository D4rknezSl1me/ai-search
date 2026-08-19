// Package metrics defines Prometheus metrics for the crawler.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	FetchTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "crawler_fetch_total",
		Help: "Total fetch attempts by outcome.",
	}, []string{"result"}) // ok | error | non_html | too_large

	FetchStatus = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "crawler_fetch_status_total",
		Help: "Fetch results by HTTP status class.",
	}, []string{"class"}) // 2xx | 3xx | 4xx | 5xx

	DocsIndexed = promauto.NewCounter(prometheus.CounterOpts{
		Name: "crawler_documents_total",
		Help: "Documents extracted and stored.",
	})

	DocsDuplicate = promauto.NewCounter(prometheus.CounterOpts{
		Name: "crawler_documents_duplicate_total",
		Help: "Documents skipped as exact duplicates.",
	})

	LinksDiscovered = promauto.NewCounter(prometheus.CounterOpts{
		Name: "crawler_links_discovered_total",
		Help: "New URLs enqueued into the frontier.",
	})

	FetchDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "crawler_fetch_duration_seconds",
		Help:    "Fetch latency.",
		Buckets: prometheus.DefBuckets,
	})

	// --- social adapters (Phase 3) ---

	SocialFetch = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "crawler_social_fetch_total",
		Help: "Social adapter fetch attempts by adapter and outcome.",
	}, []string{"adapter", "result"}) // ok | error

	SocialItems = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "crawler_social_items_total",
		Help: "Normalized social documents produced, by adapter.",
	}, []string{"adapter"})

	FreshnessRuns = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "crawler_freshness_runs_total",
		Help: "Scheduled re-ingests of tracked social entities, by adapter and outcome.",
	}, []string{"adapter", "result"}) // ok | error

	// --- browser escalation (Phase 3) ---

	RenderEscalations = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "crawler_render_escalations_total",
		Help: "Static fetches flagged as needing browser rendering, by reason.",
	}, []string{"reason"})

	RenderQueueEnqueued = promauto.NewCounter(prometheus.CounterOpts{
		Name: "crawler_render_queue_enqueued_total",
		Help: "URLs newly enqueued into the browser render queue.",
	})

	RenderQueueClaimed = promauto.NewCounter(prometheus.CounterOpts{
		Name: "crawler_render_queue_claimed_total",
		Help: "Render jobs claimed by browser workers.",
	})

	RenderQueueCompleted = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "crawler_render_queue_completed_total",
		Help: "Render jobs completed, by outcome.",
	}, []string{"result"}) // rendered | failed

	RenderIngested = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "crawler_render_ingested_total",
		Help: "Browser-rendered documents landed in the pipeline, by outcome.",
	}, []string{"result"}) // indexed | duplicate | empty

	// --- sitemap discovery (Phase 4) ---

	SitemapURLs = promauto.NewCounter(prometheus.CounterOpts{
		Name: "crawler_sitemap_urls_total",
		Help: "URLs newly enqueued into the frontier from sitemap ingestion.",
	})

	FeedURLs = promauto.NewCounter(prometheus.CounterOpts{
		Name: "crawler_feed_urls_total",
		Help: "URLs newly enqueued into the frontier from RSS/Atom feed ingestion.",
	})
)

// StatusClass maps an HTTP status code to a coarse class label.
func StatusClass(code int) string {
	switch {
	case code >= 200 && code < 300:
		return "2xx"
	case code >= 300 && code < 400:
		return "3xx"
	case code >= 400 && code < 500:
		return "4xx"
	default:
		return "5xx"
	}
}
