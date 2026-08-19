// Package commoncrawl discovers seed URLs from the free Common Crawl URL index.
//
// Common Crawl publishes a CDX index of the billions of URLs it has crawled,
// queryable per-domain over a free HTTP API — the single biggest cold-start
// breadth source on the open web and a zero-cost substitute for commercial
// search APIs (docs/04 §7; CLAUDE.md rule 2 forbids paid discovery). Given a
// domain, this pulls the URLs CC has seen under it straight into the frontier.
// Parsing is pure and the crawl driver takes an injected fetch func, so both are
// unit-testable offline.
package commoncrawl

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
)

// DefaultCollinfoURL lists every CC monthly crawl and its CDX API endpoint,
// newest first.
const DefaultCollinfoURL = "https://index.commoncrawl.org/collinfo.json"

// FetchFunc returns the raw bytes at a URL (real impl wraps the HTTP fetcher;
// tests inject a fake).
type FetchFunc func(ctx context.Context, url string) ([]byte, error)

// ParseCollinfo extracts the CDX API endpoints (newest first) from the
// collinfo.json listing.
func ParseCollinfo(data []byte) ([]string, error) {
	var entries []struct {
		ID     string `json:"id"`
		CDXAPI string `json:"cdx-api"`
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}
	var apis []string
	for _, e := range entries {
		if a := strings.TrimSpace(e.CDXAPI); a != "" {
			apis = append(apis, a)
		}
	}
	return apis, nil
}

// ParseCDX extracts page URLs from a CDX `output=json` response, which is
// newline-delimited JSON objects (one per captured URL). Tolerant: blank lines
// and unparseable rows are skipped, and only http(s) URLs are kept.
func ParseCDX(data []byte) []string {
	var urls []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var row struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			continue
		}
		if u := strings.TrimSpace(row.URL); strings.HasPrefix(u, "http") {
			urls = append(urls, u)
		}
	}
	return urls
}

// Opts bound and configure a discovery run.
type Opts struct {
	MaxURLs     int    // cap total URLs collected (default 1000)
	MaxIndexes  int    // how many recent CC crawls to query (default 1)
	IndexURL    string // explicit CDX API endpoint; if set, skip collinfo lookup
	CollinfoURL string // override the collinfo endpoint (tests); default the real one
}

func (o Opts) withDefaults() Opts {
	if o.MaxURLs <= 0 {
		o.MaxURLs = 1000
	}
	if o.MaxIndexes <= 0 {
		o.MaxIndexes = 1
	}
	if o.CollinfoURL == "" {
		o.CollinfoURL = DefaultCollinfoURL
	}
	return o
}

// Discover queries the Common Crawl index for every URL captured under domain
// and returns the de-duplicated list, bounded by Opts. It queries the most
// recent MaxIndexes crawls (resolved from collinfo unless an explicit IndexURL
// is given). A failed index shard is skipped (best-effort, recall-first); only a
// failure to resolve the collinfo listing is fatal.
func Discover(ctx context.Context, domain string, fetch FetchFunc, opts Opts) ([]string, error) {
	opts = opts.withDefaults()
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return nil, errors.New("domain is required")
	}

	var cdxAPIs []string
	if opts.IndexURL != "" {
		cdxAPIs = []string{opts.IndexURL}
	} else {
		data, err := fetch(ctx, opts.CollinfoURL)
		if err != nil {
			return nil, err
		}
		if cdxAPIs, err = ParseCollinfo(data); err != nil {
			return nil, err
		}
	}
	if len(cdxAPIs) > opts.MaxIndexes {
		cdxAPIs = cdxAPIs[:opts.MaxIndexes]
	}

	seen := make(map[string]struct{})
	var urls []string
	for _, api := range cdxAPIs {
		if len(urls) >= opts.MaxURLs {
			break
		}
		remaining := opts.MaxURLs - len(urls)
		q := api + "?url=" + url.QueryEscape(domain+"/*") + "&output=json&limit=" + strconv.Itoa(remaining)
		data, err := fetch(ctx, q)
		if err != nil {
			continue
		}
		for _, u := range ParseCDX(data) {
			if len(urls) >= opts.MaxURLs {
				break
			}
			if _, ok := seen[u]; ok {
				continue
			}
			seen[u] = struct{}{}
			urls = append(urls, u)
		}
	}
	return urls, nil
}
