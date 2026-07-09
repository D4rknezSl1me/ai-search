package social

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"
)

// --- fakes -----------------------------------------------------------------

// fakeAdapter is a deterministic, network-free Adapter for driving the ingester.
type fakeAdapter struct {
	name     string
	enabled  bool
	targets  []string                   // Discover result
	discErr  error                      // Discover error (targets ignored when set)
	raw      map[string]*RawContent     // target -> raw response (Next chains pages)
	fetchErr map[string]error           // target -> fetch error
	parsed   map[string][]NormalizedDoc // raw.Target -> parsed docs
	parseErr map[string]error           // raw.Target -> parse error
	fetches  int                        // observed Fetch calls
}

func (f *fakeAdapter) Name() string   { return f.name }
func (f *fakeAdapter) Health() Health { return Health{Adapter: f.name, Enabled: f.enabled} }

func (f *fakeAdapter) Discover(_ context.Context, _ string) ([]string, error) {
	if f.discErr != nil {
		return nil, f.discErr
	}
	return f.targets, nil
}

func (f *fakeAdapter) Fetch(_ context.Context, target string) (*RawContent, error) {
	f.fetches++
	if err := f.fetchErr[target]; err != nil {
		return nil, err
	}
	if rc, ok := f.raw[target]; ok {
		return rc, nil
	}
	return &RawContent{Target: target}, nil
}

func (f *fakeAdapter) Parse(raw *RawContent) ([]NormalizedDoc, error) {
	if err := f.parseErr[raw.Target]; err != nil {
		return nil, err
	}
	return f.parsed[raw.Target], nil
}

func (f *fakeAdapter) Paginate(raw *RawContent) (string, bool) {
	if raw == nil || raw.Next == "" {
		return "", false
	}
	return raw.Next, true
}

// fakeSink records persisted docs and dedups by content hash.
type fakeSink struct {
	seen      map[string]bool
	persisted []NormalizedDoc
	err       error
}

func newFakeSink() *fakeSink { return &fakeSink{seen: map[string]bool{}} }

func (s *fakeSink) Persist(_ context.Context, d *NormalizedDoc) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	key := hex.EncodeToString(d.ContentHash)
	if s.seen[key] {
		return false, nil // duplicate
	}
	s.seen[key] = true
	s.persisted = append(s.persisted, *d)
	return true, nil
}

// mkDoc builds a finalized doc so its ContentHash reflects platform+post_id.
func mkDoc(platform, postID, text string) NormalizedDoc {
	d := NormalizedDoc{Platform: platform, PostID: postID, Text: text}
	d.finalize()
	return d
}

// --- tests -----------------------------------------------------------------

func TestIngestSeed_PaginatesAndDedups(t *testing.T) {
	docB := mkDoc("fake", "B", "shared")
	fa := &fakeAdapter{
		name:    "fake",
		enabled: true,
		targets: []string{"t1"},
		raw: map[string]*RawContent{
			"t1": {Target: "t1", Next: "t2"},
			"t2": {Target: "t2"}, // no Next → end
		},
		parsed: map[string][]NormalizedDoc{
			"t1": {mkDoc("fake", "A", "a"), docB},
			"t2": {docB, mkDoc("fake", "C", "c")}, // docB repeats across pages
		},
	}
	reg := NewRegistry()
	reg.Register(fa)
	sink := newFakeSink()

	res, err := NewIngester(reg, sink, 5, 0).IngestSeed(context.Background(), "fake", "seed")
	if err != nil {
		t.Fatalf("IngestSeed: %v", err)
	}
	if res.Targets != 1 || res.Pages != 2 {
		t.Errorf("targets/pages = %d/%d, want 1/2", res.Targets, res.Pages)
	}
	if res.Docs != 4 {
		t.Errorf("docs = %d, want 4", res.Docs)
	}
	if res.Inserted != 3 || res.Duplicates != 1 {
		t.Errorf("inserted/duplicates = %d/%d, want 3/1", res.Inserted, res.Duplicates)
	}
	if res.Errors != 0 {
		t.Errorf("errors = %d, want 0", res.Errors)
	}
	if len(sink.persisted) != 3 {
		t.Errorf("persisted %d docs, want 3", len(sink.persisted))
	}
}

func TestIngestSeed_MaxPagesCap(t *testing.T) {
	fa := &fakeAdapter{
		name:    "fake",
		enabled: true,
		targets: []string{"t1"},
		raw: map[string]*RawContent{
			"t1": {Target: "t1", Next: "t2"},
			"t2": {Target: "t2", Next: "t3"},
		},
		parsed: map[string][]NormalizedDoc{
			"t1": {mkDoc("fake", "A", "a")},
			"t2": {mkDoc("fake", "B", "b")},
		},
	}
	reg := NewRegistry()
	reg.Register(fa)

	res, err := NewIngester(reg, newFakeSink(), 1, 0).IngestSeed(context.Background(), "fake", "seed")
	if err != nil {
		t.Fatalf("IngestSeed: %v", err)
	}
	if res.Pages != 1 || fa.fetches != 1 {
		t.Errorf("pages/fetches = %d/%d, want 1/1 (maxPages cap)", res.Pages, fa.fetches)
	}
	if res.Inserted != 1 {
		t.Errorf("inserted = %d, want 1", res.Inserted)
	}
}

func TestIngestSeed_DefaultMaxPages(t *testing.T) {
	// maxPages<=0 must fall back to DefaultMaxPages (single page), not paginate
	// unboundedly.
	fa := &fakeAdapter{
		name:    "fake",
		enabled: true,
		targets: []string{"t1"},
		raw:     map[string]*RawContent{"t1": {Target: "t1", Next: "t2"}, "t2": {Target: "t2", Next: "t3"}},
		parsed:  map[string][]NormalizedDoc{"t1": {mkDoc("fake", "A", "a")}},
	}
	reg := NewRegistry()
	reg.Register(fa)

	res, _ := NewIngester(reg, newFakeSink(), 0, 0).IngestSeed(context.Background(), "fake", "seed")
	if res.Pages != DefaultMaxPages {
		t.Errorf("pages = %d, want DefaultMaxPages=%d", res.Pages, DefaultMaxPages)
	}
}

func TestIngestSeed_FetchErrorStopsTarget(t *testing.T) {
	fa := &fakeAdapter{
		name:     "fake",
		enabled:  true,
		targets:  []string{"t1"},
		fetchErr: map[string]error{"t1": errors.New("boom")},
	}
	reg := NewRegistry()
	reg.Register(fa)

	res, err := NewIngester(reg, newFakeSink(), 5, 0).IngestSeed(context.Background(), "fake", "seed")
	if err != nil {
		t.Fatalf("IngestSeed returned error, want nil (fetch error is counted, not fatal): %v", err)
	}
	if res.Errors != 1 || res.Pages != 0 || res.Inserted != 0 {
		t.Errorf("errors/pages/inserted = %d/%d/%d, want 1/0/0", res.Errors, res.Pages, res.Inserted)
	}
}

func TestIngestSeed_ParseAndPersistErrors(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&fakeAdapter{
		name:     "parsebad",
		enabled:  true,
		targets:  []string{"t1"},
		raw:      map[string]*RawContent{"t1": {Target: "t1"}},
		parseErr: map[string]error{"t1": errors.New("bad json")},
	})
	res, _ := NewIngester(reg, newFakeSink(), 5, 0).IngestSeed(context.Background(), "parsebad", "seed")
	if res.Pages != 1 || res.Errors != 1 || res.Docs != 0 {
		t.Errorf("parse error: pages/errors/docs = %d/%d/%d, want 1/1/0", res.Pages, res.Errors, res.Docs)
	}

	reg2 := NewRegistry()
	reg2.Register(&fakeAdapter{
		name:    "persistbad",
		enabled: true,
		targets: []string{"t1"},
		raw:     map[string]*RawContent{"t1": {Target: "t1"}},
		parsed:  map[string][]NormalizedDoc{"t1": {mkDoc("x", "1", "a"), mkDoc("x", "2", "b")}},
	})
	sink := newFakeSink()
	sink.err = errors.New("db down")
	res2, _ := NewIngester(reg2, sink, 5, 0).IngestSeed(context.Background(), "persistbad", "seed")
	if res2.Docs != 2 || res2.Errors != 2 || res2.Inserted != 0 {
		t.Errorf("persist error: docs/errors/inserted = %d/%d/%d, want 2/2/0", res2.Docs, res2.Errors, res2.Inserted)
	}
}

func TestIngestSeed_UnknownAndDisabled(t *testing.T) {
	reg := NewRegistry()
	if _, err := NewIngester(reg, newFakeSink(), 1, 0).IngestSeed(context.Background(), "nope", "s"); err == nil {
		t.Error("unknown adapter: want error, got nil")
	}

	fa := &fakeAdapter{name: "off", enabled: false, targets: []string{"t1"}}
	reg.Register(fa)
	_, err := NewIngester(reg, newFakeSink(), 1, 0).IngestSeed(context.Background(), "off", "s")
	if err == nil {
		t.Error("disabled adapter: want error, got nil")
	}
	if fa.fetches != 0 {
		t.Errorf("disabled adapter fetched %d times, want 0", fa.fetches)
	}
}

func TestIngestSeed_DiscoverError(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&fakeAdapter{name: "d", enabled: true, discErr: errors.New("nope")})
	res, err := NewIngester(reg, newFakeSink(), 1, 0).IngestSeed(context.Background(), "d", "s")
	if err == nil {
		t.Fatal("discover error: want error, got nil")
	}
	if res.Errors != 1 {
		t.Errorf("errors = %d, want 1", res.Errors)
	}
}
