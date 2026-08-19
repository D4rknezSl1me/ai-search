// Package fetch performs polite HTTP fetches of web content.
package fetch

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"
)

type Fetcher struct {
	client       *http.Client
	userAgent    string
	maxBodyBytes int64
}

type Result struct {
	FinalURL    string
	Status      int
	ContentType string
	Body        []byte
	Truncated   bool
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", f.userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en;q=0.9,*;q=0.5")

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	limited := io.LimitReader(resp.Body, f.maxBodyBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	truncated := int64(len(body)) > f.maxBodyBytes
	if truncated {
		body = body[:f.maxBodyBytes]
	}

	return &Result{
		FinalURL:    resp.Request.URL.String(),
		Status:      resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		Body:        body,
		Truncated:   truncated,
	}, nil
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
