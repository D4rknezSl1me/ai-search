package crawl

import (
	"sync"
	"time"
)

// HostLimiter enforces a minimum delay between fetches to the same host.
// Callers that share a host queue up; different hosts proceed in parallel.
type HostLimiter struct {
	mu    sync.Mutex
	delay time.Duration
	next  map[string]time.Time
}

func NewHostLimiter(delay time.Duration) *HostLimiter {
	return &HostLimiter{delay: delay, next: make(map[string]time.Time)}
}

// Reserve returns how long the caller must wait before fetching host, and
// reserves the following slot so concurrent callers space out.
func (l *HostLimiter) Reserve(host string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	start := now
	if t, ok := l.next[host]; ok && t.After(now) {
		start = t
	}
	l.next[host] = start.Add(l.delay)
	return start.Sub(now)
}
