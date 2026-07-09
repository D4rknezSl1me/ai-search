package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ai-search/crawler/internal/social"
)

// The social status handlers only read the adapter registry, so store/blob can
// be nil here — we exercise the HTTP contract, not the crawl path.
func newSocialServer() *Server {
	reg := social.DefaultRegistry("ai-search-test/0.1", 5*time.Second, 0)
	return NewServer(nil, nil, reg)
}

func TestSocialAdaptersList(t *testing.T) {
	srv := newSocialServer()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/internal/social/adapters", nil)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Summary  social.Summary  `json:"summary"`
		Adapters []social.Health `json:"adapters"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Summary.Adapters != 3 || len(body.Adapters) != 3 {
		t.Fatalf("expected 3 adapters, got summary=%d list=%d", body.Summary.Adapters, len(body.Adapters))
	}
	if body.Summary.Enabled != 3 {
		t.Fatalf("fresh adapters should all be enabled, got %d", body.Summary.Enabled)
	}
}

func TestSocialAdapterByName(t *testing.T) {
	srv := newSocialServer()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/internal/social/adapters/mastodon", nil)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var h social.Health
	if err := json.Unmarshal(rec.Body.Bytes(), &h); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if h.Adapter != "mastodon" {
		t.Fatalf("adapter = %q, want mastodon", h.Adapter)
	}
	if !h.Enabled {
		t.Fatal("fresh mastodon adapter should be enabled")
	}
}

func TestSocialAdapterUnknown(t *testing.T) {
	srv := newSocialServer()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/internal/social/adapters/does-not-exist", nil)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// A trailing-slash request with no name falls through to the full list.
func TestSocialAdapterEmptyNameListsAll(t *testing.T) {
	srv := newSocialServer()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/internal/social/adapters/", nil)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Adapters []social.Health `json:"adapters"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Adapters) != 3 {
		t.Fatalf("empty-name path should list all 3 adapters, got %d", len(body.Adapters))
	}
}

// A nil registry (defensive path) must not panic and returns empty results.
func TestSocialAdaptersNilRegistry(t *testing.T) {
	srv := NewServer(nil, nil, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/internal/social/adapters", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}
