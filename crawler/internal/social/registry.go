package social

import (
	"sync"
	"time"
)

// Registry is the set of social adapters the crawler has wired up. It is the
// single place monitoring queries for per-adapter health (docs/08 §8): the
// control API's status endpoint reads it, so operators can see which platforms
// are ingesting, how many items they've produced, and whether any auto-disabled
// on error rate — without reaching into each adapter individually.
type Registry struct {
	mu       sync.RWMutex
	adapters map[string]Adapter
	order    []string // registration order, for stable "all" listings
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{adapters: make(map[string]Adapter)}
}

// Register adds an adapter under its Name(). Registering the same name twice
// replaces the earlier one (last write wins) but keeps its original position so
// listings stay stable across a hot-swap.
func (r *Registry) Register(a Adapter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := a.Name()
	if _, exists := r.adapters[name]; !exists {
		r.order = append(r.order, name)
	}
	r.adapters[name] = a
}

// Get returns the adapter registered under name, if any.
func (r *Registry) Get(name string) (Adapter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.adapters[name]
	return a, ok
}

// Names returns the registered adapter names in registration order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// Status returns a single adapter's live health snapshot.
func (r *Registry) Status(name string) (Health, bool) {
	r.mu.RLock()
	a, ok := r.adapters[name]
	r.mu.RUnlock()
	if !ok {
		return Health{}, false
	}
	return a.Health(), true
}

// Statuses returns every adapter's live health snapshot in registration order.
func (r *Registry) Statuses() []Health {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Health, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.adapters[name].Health())
	}
	return out
}

// Summary is an aggregate view across all registered adapters for the status
// endpoint's top-level fields — a quick "is anything wrong" signal without the
// caller having to fold over the per-adapter list.
type Summary struct {
	Adapters int   `json:"adapters"`
	Enabled  int   `json:"enabled"`
	Disabled int   `json:"disabled"`
	Fetches  int64 `json:"fetches"`
	Errors   int64 `json:"errors"`
	Items    int64 `json:"items"`
}

// Summarize folds a set of health snapshots into aggregate counters.
func Summarize(hs []Health) Summary {
	var s Summary
	s.Adapters = len(hs)
	for _, h := range hs {
		if h.Enabled {
			s.Enabled++
		} else {
			s.Disabled++
		}
		s.Fetches += h.Fetches
		s.Errors += h.Errors
		s.Items += h.Items
	}
	return s
}

// DefaultRegistry builds a registry preloaded with the credential-free adapters
// (Mastodon, Hacker News, Lemmy). userAgent identifies the crawler; timeout and
// pageLimit bound each adapter's HTTP calls. Login-walled platforms are added
// later once the owner provides credentials (ralph/QUESTIONS.md).
func DefaultRegistry(userAgent string, timeout time.Duration, pageLimit int) *Registry {
	r := NewRegistry()
	r.Register(NewMastodon(userAgent, timeout))
	r.Register(NewHackerNews(userAgent, timeout, pageLimit))
	r.Register(NewLemmy(userAgent, timeout, pageLimit))
	return r
}
