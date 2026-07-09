package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

// --- ingest endpoint -------------------------------------------------------
// These exercise the handler's guard paths, which resolve before the sink
// touches store/blob (both nil here), so no live datastores are needed.

func postIngest(t *testing.T, srv *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/internal/social/ingest", strings.NewReader(body))
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestSocialIngestMethodNotAllowed(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/internal/social/ingest", nil)
	newSocialServer().Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestSocialIngestValidation(t *testing.T) {
	rec := postIngest(t, newSocialServer(), `{"adapter":"","seed":""}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for missing adapter/seed", rec.Code)
	}
}

func TestSocialIngestNilRegistry(t *testing.T) {
	rec := postIngest(t, NewServer(nil, nil, nil), `{"adapter":"mastodon","seed":"x"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 when registry unconfigured", rec.Code)
	}
}

func TestSocialIngestUnknownAdapter(t *testing.T) {
	rec := postIngest(t, newSocialServer(), `{"adapter":"nope","seed":"x"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 for unknown adapter", rec.Code)
	}
	var body struct {
		Error  string        `json:"error"`
		Result social.Result `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error == "" || body.Result.Adapter != "nope" {
		t.Fatalf("expected error + result echoing adapter, got %+v", body)
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
