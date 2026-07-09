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
