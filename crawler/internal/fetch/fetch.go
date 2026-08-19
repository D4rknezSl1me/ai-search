// Package fetch performs polite HTTP fetches of web content.
package fetch

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Fetcher struct {
	client       *http.Client
	userAgent    string
	maxBodyBytes int64
}

type Result struct {
	FinalURL     string
	Status       int
	ContentType  string
	Body         []byte
	Truncated    bool
	NotModified  bool          // server returned 304 (conditional GET); Body is empty
	ETag         string        // response ETag, if any (store for the next conditional GET)
	LastModified string        // response Last-Modified, if any
	RetryAfter   time.Duration // parsed Retry-After (429/503), 0 if absent
}

// parseRetryAfter interprets a Retry-After header value: either delta-seconds or
// an HTTP-date. Returns 0 when absent/unparseable/in the past.
func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

func New(timeout time.Duration, maxBodyBytes int64, userAgent string) *Fetcher {
	return &Fetcher{
		client: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return http.ErrUseLastResponse
				}
				return nil
			},
		},
		userAgent:    userAgent,
		maxBodyBytes: maxBodyBytes,
	}
}

// Get fetches a URL, capping the body at maxBodyBytes.
func (f *Fetcher) Get(ctx context.Context, rawURL string) (*Result, error) {
	return f.GetConditional(ctx, rawURL, "", "")
}

// GetConditional fetches a URL, sending If-None-Match / If-Modified-Since when
// prior validators are supplied. A 304 response returns a Result with
// NotModified=true and no body, so the caller can skip re-processing an
// unchanged page (recrawl efficiency).
func (f *Fetcher) GetConditional(ctx context.Context, rawURL, etag, lastModified string) (*Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", f.userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en;q=0.9,*;q=0.5")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if lastModified != "" {
		req.Header.Set("If-Modified-Since", lastModified)
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	res := &Result{
		FinalURL:     resp.Request.URL.String(),
		Status:       resp.StatusCode,
		ContentType:  resp.Header.Get("Content-Type"),
		ETag:         resp.Header.Get("ETag"),
		LastModified: resp.Header.Get("Last-Modified"),
		RetryAfter:   parseRetryAfter(resp.Header.Get("Retry-After")),
	}
	if resp.StatusCode == http.StatusNotModified {
		res.NotModified = true
		return res, nil // 304 has no body
	}

	limited := io.LimitReader(resp.Body, f.maxBodyBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	truncated := int64(len(body)) > f.maxBodyBytes
	if truncated {
		body = body[:f.maxBodyBytes]
	}
	res.Body = body
	res.Truncated = truncated
	return res, nil
}

// IsHTML reports whether a Content-Type header denotes HTML.
func IsHTML(contentType string) bool {
	ct := strings.ToLower(contentType)
	return strings.Contains(ct, "text/html") || strings.Contains(ct, "application/xhtml")
}

// IsText reports whether a Content-Type denotes plain textual content that can
// be indexed directly (the body IS the content) — text/plain, markdown, csv,
// logs, etc. HTML is excluded (it has its own extractor). Recall-first: these
// were previously dropped as "non-HTML".
func IsText(contentType string) bool {
	ct := strings.ToLower(contentType)
	return strings.HasPrefix(ct, "text/") && !strings.Contains(ct, "html")
}
