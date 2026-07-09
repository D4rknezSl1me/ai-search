package social

import (
	"sync"
	"time"

	"github.com/ai-search/crawler/internal/metrics"
)

// Health is a point-in-time snapshot of an adapter's status (docs/08 §8).
type Health struct {
	Adapter   string    `json:"adapter"`
	Enabled   bool      `json:"enabled"`
	Fetches   int64     `json:"fetches"`
	Errors    int64     `json:"errors"`
	Items     int64     `json:"items"`
	ErrorRate float64   `json:"error_rate"`
	LastError string    `json:"last_error,omitempty"`
	LastAt    time.Time `json:"last_activity,omitempty"`
}

// HealthTracker accumulates per-adapter counters and auto-disables an adapter
// when its error rate crosses a threshold over a minimum sample — "fail loud,
// don't silently under-collect" (docs/08 §8). Counters also feed Prometheus.
type HealthTracker struct {
	name      string
	threshold float64 // error-rate at/above which the adapter disables
	minSample int64   // don't judge until this many fetches have happened

	mu        sync.Mutex
	fetches   int64
	errors    int64
	items     int64
	lastError string
	lastAt    time.Time
}

// NewHealthTracker creates a tracker. A threshold <= 0 disables auto-disable
// (the adapter always reports enabled); minSample guards against tripping on
// the first unlucky fetch.
func NewHealthTracker(name string, threshold float64, minSample int64) *HealthTracker {
	if minSample < 1 {
		minSample = 1
	}
	return &HealthTracker{name: name, threshold: threshold, minSample: minSample}
}

// RecordFetch records a completed fetch (err may be nil).
func (h *HealthTracker) RecordFetch(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.fetches++
	h.lastAt = time.Now().UTC()
	if err != nil {
		h.errors++
		h.lastError = err.Error()
		metrics.SocialFetch.WithLabelValues(h.name, "error").Inc()
		return
	}
	metrics.SocialFetch.WithLabelValues(h.name, "ok").Inc()
}

// RecordItems records how many normalized documents a parse produced.
func (h *HealthTracker) RecordItems(n int) {
	if n <= 0 {
		return
	}
	h.mu.Lock()
	h.items += int64(n)
	h.mu.Unlock()
	metrics.SocialItems.WithLabelValues(h.name).Add(float64(n))
}

// Enabled reports whether the adapter should keep running given its error rate.
func (h *HealthTracker) Enabled() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.enabledLocked()
}

func (h *HealthTracker) enabledLocked() bool {
	if h.threshold <= 0 || h.fetches < h.minSample {
		return true
	}
	return h.errorRateLocked() < h.threshold
}

func (h *HealthTracker) errorRateLocked() float64 {
	if h.fetches == 0 {
		return 0
	}
	return float64(h.errors) / float64(h.fetches)
}

// Status returns a snapshot for the monitoring endpoint.
func (h *HealthTracker) Status() Health {
	h.mu.Lock()
	defer h.mu.Unlock()
	return Health{
		Adapter:   h.name,
		Enabled:   h.enabledLocked(),
		Fetches:   h.fetches,
		Errors:    h.errors,
		Items:     h.items,
		ErrorRate: h.errorRateLocked(),
		LastError: h.lastError,
		LastAt:    h.lastAt,
	}
}
