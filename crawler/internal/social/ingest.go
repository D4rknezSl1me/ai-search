package social

import (
	"context"
	"fmt"
	"time"
)

// DefaultMaxPages bounds pagination when a caller does not specify a limit. It
// keeps a single ingest run from walking an unbounded timeline; deeper walks are
// an explicit opt-in (the control endpoint / caller passes a larger cap).
const DefaultMaxPages = 1

// Sink persists one normalized social document into the crawler's storage: the
// post's clean text (for the intelligence plane to chunk/embed, exactly as for
// web pages) plus a documents row carrying the social metadata. It is an
// interface so the ingester stays free of store/blob dependencies and can be
// unit-tested with a fake. Persist returns inserted=false for an already-known
// post (exact-dedup by content hash), which the ingester counts as a duplicate.
type Sink interface {
	Persist(ctx context.Context, d *NormalizedDoc) (inserted bool, err error)
}

// Ingester drives social adapters end-to-end — Discover → Fetch → Paginate →
// Parse → persist — routing every parsed post through the Sink into the same
// documents + text-blob pipeline that web pages use (docs/08 §7). Adapters on
// their own only produce NormalizedDocs in memory; the ingester is the piece
// that actually lands them in storage so they flow into the RAG index. Each
// adapter records its own fetch/item health internally, so the ingester does not
// double-count — it only reads Health().Enabled to honour auto-disable.
type Ingester struct {
	reg      *Registry
	sink     Sink
	maxPages int           // hard cap on pages fetched per discovered target
	pace     time.Duration // delay between successive fetches (politeness)
}

// NewIngester builds an ingester over a registry and sink. maxPages<=0 falls back
// to DefaultMaxPages; pace<=0 means no inter-fetch delay.
func NewIngester(reg *Registry, sink Sink, maxPages int, pace time.Duration) *Ingester {
	if maxPages <= 0 {
		maxPages = DefaultMaxPages
	}
	return &Ingester{reg: reg, sink: sink, maxPages: maxPages, pace: pace}
}

// Result summarizes one IngestSeed run for the control API and logs.
type Result struct {
	Adapter    string `json:"adapter"`
	Seed       string `json:"seed"`
	Targets    int    `json:"targets"`    // fetch targets discovered from the seed
	Pages      int    `json:"pages"`      // pages actually fetched (incl. pagination)
	Docs       int    `json:"docs"`       // normalized documents parsed
	Inserted   int    `json:"inserted"`   // newly stored (non-duplicate)
	Duplicates int    `json:"duplicates"` // posts already known
	Errors     int    `json:"errors"`     // fetch/parse/persist failures
}

// IngestSeed runs one adapter over one seed: it discovers the fetch targets, then
// for each target walks pages (up to maxPages) parsing and persisting posts,
// following the adapter's pagination cursor between pages. A fetch error ends
// that target's walk (there is no cursor without a response); parse/persist
// errors are counted but do not stop the walk, so one bad page does not sink the
// rest. If the adapter auto-disables mid-run (error rate crossed its threshold),
// the walk stops immediately — "fail loud, don't silently under-collect".
func (ing *Ingester) IngestSeed(ctx context.Context, adapter, seed string) (Result, error) {
	res := Result{Adapter: adapter, Seed: seed}

	a, ok := ing.reg.Get(adapter)
	if !ok {
		return res, fmt.Errorf("social: unknown adapter %q", adapter)
	}
	if !a.Health().Enabled {
		return res, fmt.Errorf("social: adapter %q is disabled", adapter)
	}

	targets, err := a.Discover(ctx, seed)
	if err != nil {
		res.Errors++
		return res, fmt.Errorf("social: discover %q: %w", adapter, err)
	}
	res.Targets = len(targets)

	for _, target := range targets {
		for pages := 0; target != "" && pages < ing.maxPages; pages++ {
			if err := ctx.Err(); err != nil {
				return res, err
			}
			if !a.Health().Enabled { // auto-disabled mid-run → stop collecting
				return res, nil
			}

			raw, err := a.Fetch(ctx, target)
			if err != nil {
				res.Errors++
				break // no response → no cursor → cannot paginate this target
			}
			res.Pages++

			if docs, err := a.Parse(raw); err != nil {
				res.Errors++
			} else {
				res.Docs += len(docs)
				for i := range docs {
					inserted, perr := ing.sink.Persist(ctx, &docs[i])
					switch {
					case perr != nil:
						res.Errors++
					case inserted:
						res.Inserted++
					default:
						res.Duplicates++
					}
				}
			}

			next, more := a.Paginate(raw)
			if !more {
				break
			}
			target = next
			if ing.pace > 0 {
				select {
				case <-ctx.Done():
					return res, ctx.Err()
				case <-time.After(ing.pace):
				}
			}
		}
	}
	return res, nil
}
