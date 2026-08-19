package fetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGetConditionalNotModified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Emulate a validating origin: return 304 when the client's validator matches.
		if r.Header.Get("If-None-Match") == `"v1"` || r.Header.Get("If-Modified-Since") != "" {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Last-Modified", "Wed, 21 Oct 2026 07:28:00 GMT")
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>hi</html>"))
	}))
	defer srv.Close()

	f := New(5*time.Second, 1<<20, "test-agent")

	// First fetch: full 200 with validators captured.
	res, err := f.Get(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if res.NotModified || res.Status != 200 || len(res.Body) == 0 {
		t.Fatalf("first fetch: status=%d notmod=%v bodylen=%d", res.Status, res.NotModified, len(res.Body))
	}
	if res.ETag != `"v1"` || res.LastModified == "" {
		t.Fatalf("validators not captured: etag=%q lastmod=%q", res.ETag, res.LastModified)
	}

	// Conditional refetch with the ETag → 304, no body.
	res2, err := f.GetConditional(context.Background(), srv.URL, res.ETag, "")
	if err != nil {
		t.Fatal(err)
	}
	if !res2.NotModified || res2.Status != http.StatusNotModified || len(res2.Body) != 0 {
		t.Fatalf("conditional: status=%d notmod=%v bodylen=%d", res2.Status, res2.NotModified, len(res2.Body))
	}
}

func TestIsHTML(t *testing.T) {
	for _, ct := range []string{"text/html", "text/html; charset=utf-8", "application/xhtml+xml"} {
		if !IsHTML(ct) {
			t.Errorf("IsHTML(%q) = false, want true", ct)
		}
	}
	for _, ct := range []string{"text/plain", "application/json", ""} {
		if IsHTML(ct) {
			t.Errorf("IsHTML(%q) = true, want false", ct)
		}
	}
}

func TestIsText(t *testing.T) {
	for _, ct := range []string{
		"text/plain", "text/plain; charset=utf-8", "text/markdown", "text/csv", "TEXT/PLAIN",
	} {
		if !IsText(ct) {
			t.Errorf("IsText(%q) = false, want true", ct)
		}
	}
	for _, ct := range []string{
		"text/html", "text/html; charset=utf-8", "application/json", "application/pdf", "image/png", "",
	} {
		if IsText(ct) {
			t.Errorf("IsText(%q) = true, want false", ct)
		}
	}
}
