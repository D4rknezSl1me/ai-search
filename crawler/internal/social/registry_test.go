package social

import (
	"context"
	"errors"
	"testing"
	"time"
)

// stubAdapter is a minimal Adapter whose health is driven directly, so registry
// behavior can be tested without hitting any platform.
type stubAdapter struct {
	name string
	h    *HealthTracker
}

func newStub(name string) *stubAdapter {
	// threshold 0.5 over a 2-fetch minimum: two errors flip it to disabled.
	return &stubAdapter{name: name, h: NewHealthTracker(name, 0.5, 2)}
}

func (s *stubAdapter) Name() string                                       { return s.name }
func (s *stubAdapter) Discover(context.Context, string) ([]string, error) { return nil, nil }
func (s *stubAdapter) Fetch(context.Context, string) (*RawContent, error) { return nil, nil }
func (s *stubAdapter) Parse(*RawContent) ([]NormalizedDoc, error)         { return nil, nil }
func (s *stubAdapter) Paginate(*RawContent) (string, bool)                { return "", false }
func (s *stubAdapter) Health() Health                                     { return s.h.Status() }

func TestRegistryRegisterAndOrder(t *testing.T) {
	r := NewRegistry()
	r.Register(newStub("mastodon"))
	r.Register(newStub("hackernews"))
	r.Register(newStub("lemmy"))

	got := r.Names()
	want := []string{"mastodon", "hackernews", "lemmy"}
	if len(got) != len(want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("registration order not preserved: got %v, want %v", got, want)
		}
	}
}

func TestRegistryReRegisterKeepsPosition(t *testing.T) {
	r := NewRegistry()
	r.Register(newStub("a"))
	r.Register(newStub("b"))
	// Hot-swap "a"; it must keep its leading position, not jump to the end.
	replacement := newStub("a")
	replacement.h.RecordItems(7)
	r.Register(replacement)

	names := r.Names()
	if len(names) != 2 || names[0] != "a" || names[1] != "b" {
		t.Fatalf("re-register changed order/count: %v", names)
	}
	st, ok := r.Status("a")
	if !ok || st.Items != 7 {
		t.Fatalf("re-register did not replace adapter: status=%+v ok=%v", st, ok)
	}
}

func TestRegistryStatusUnknown(t *testing.T) {
	r := NewRegistry()
	if _, ok := r.Status("nope"); ok {
		t.Fatal("Status of unregistered adapter should report ok=false")
	}
	if _, ok := r.Get("nope"); ok {
		t.Fatal("Get of unregistered adapter should report ok=false")
	}
}

func TestRegistryStatusesAndSummary(t *testing.T) {
	r := NewRegistry()
	good := newStub("good")
	good.h.RecordFetch(nil)
	good.h.RecordFetch(nil)
	good.h.RecordItems(5)

	bad := newStub("bad")
	// Two failed fetches over a 2-fetch minimum at threshold 0.5 → disabled.
	bad.h.RecordFetch(errors.New("boom"))
	bad.h.RecordFetch(errors.New("boom"))

	r.Register(good)
	r.Register(bad)

	statuses := r.Statuses()
	if len(statuses) != 2 {
		t.Fatalf("Statuses() len = %d, want 2", len(statuses))
	}
	if !statuses[0].Enabled {
		t.Fatal("good adapter should be enabled")
	}
	if statuses[1].Enabled {
		t.Fatal("bad adapter should have auto-disabled at 100%% error rate")
	}
	if statuses[1].LastError != "boom" {
		t.Fatalf("bad adapter last_error = %q, want boom", statuses[1].LastError)
	}

	sum := Summarize(statuses)
	if sum.Adapters != 2 || sum.Enabled != 1 || sum.Disabled != 1 {
		t.Fatalf("summary counts wrong: %+v", sum)
	}
	if sum.Fetches != 4 || sum.Errors != 2 || sum.Items != 5 {
		t.Fatalf("summary totals wrong: %+v", sum)
	}
}

func TestDefaultRegistry(t *testing.T) {
	r := DefaultRegistry("ai-search-test/0.1", 5*time.Second, 0)
	names := r.Names()
	want := map[string]bool{"mastodon": false, "hackernews": false, "lemmy": false}
	if len(names) != len(want) {
		t.Fatalf("DefaultRegistry has %d adapters, want %d (%v)", len(names), len(want), names)
	}
	for _, n := range names {
		if _, ok := want[n]; !ok {
			t.Fatalf("unexpected adapter %q in default registry", n)
		}
		want[n] = true
	}
	for n, seen := range want {
		if !seen {
			t.Fatalf("default registry missing adapter %q", n)
		}
	}
	// Fresh adapters have no fetches yet → all enabled, zero counters.
	sum := Summarize(r.Statuses())
	if sum.Enabled != 3 || sum.Fetches != 0 || sum.Items != 0 {
		t.Fatalf("fresh default registry summary unexpected: %+v", sum)
	}
}
