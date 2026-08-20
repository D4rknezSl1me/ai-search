// Package metasearch discovers candidate URLs for a free-text query via a
// self-hosted SearXNG metasearch instance.
//
// This is the "who mentions X across the web?" layer the URL-based sources
// (Common Crawl, sitemaps) can't answer. Given a name or topic, SearXNG
// aggregates results from many public search engines (no API keys, fully
// self-hosted — CLAUDE.md rule 2) and returns candidate URLs, which the crawler
// then deep-crawls + indexes. This is what surfaces the obscure local-newspaper
// mention (maximum recall, the north star). Parser is pure; the crawl driver
// takes an injected fetch func — both unit-testable offline.
package metasearch

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
)

// FetchFunc returns the raw bytes at a URL (real impl wraps the HTTP fetcher;
// tests inject a fake).
type FetchFunc func(ctx context.Context, url string) ([]byte, error)

// ParseResults extracts result URLs from a SearXNG `format=json` response
// (`{"results":[{"url":...},...]}`), de-duplicated, http(s) only.
func ParseResults(data []byte) []string {
	var doc struct {
		Results []struct {
			URL string `json:"url"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil
	}
	seen := make(map[string]struct{})
	var urls []string
	for _, r := range doc.Results {
		u := strings.TrimSpace(r.URL)
		if !strings.HasPrefix(u, "http") {
			continue
		}
		if _, ok := seen[u]; ok {
			continue
		}
		seen[u] = struct{}{}
		urls = append(urls, u)
	}
	return urls
}

// Opts bound a discovery run.
type Opts struct {
	MaxURLs  int // cap total URLs collected (default 50)
	MaxPages int // metasearch result pages to fetch (default 2)
}

func (o Opts) withDefaults() Opts {
	if o.MaxURLs <= 0 {
		o.MaxURLs = 50
	}
	if o.MaxPages <= 0 {
		o.MaxPages = 2
	}
	return o
}

// queryURL builds a SearXNG JSON search URL for one result page.
func queryURL(base, query string, page int) string {
	base = strings.TrimRight(base, "/")
	return base + "/search?q=" + url.QueryEscape(query) +
		"&format=json&pageno=" + strconv.Itoa(page)
}

// Discover queries SearXNG for `query` and returns the de-duplicated candidate
// URLs across up to MaxPages result pages, capped at MaxURLs. An empty base or
// query errors; a failed page is skipped (best-effort, recall-first) — only a
// failure on the very first page is fatal.
func Discover(ctx context.Context, base, query string, fetch FetchFunc, opts Opts) ([]string, error) {
	opts = opts.withDefaults()
	base = strings.TrimSpace(base)
	query = strings.TrimSpace(query)
	if base == "" {
		return nil, errors.New("metasearch base URL is required")
	}
	if query == "" {
		return nil, errors.New("query is required")
	}

	seen := make(map[string]struct{})
	var urls []string
	for page := 1; page <= opts.MaxPages; page++ {
		if len(urls) >= opts.MaxURLs {
			break
		}
		data, err := fetch(ctx, queryURL(base, query, page))
		if err != nil {
			if page == 1 {
				return nil, err
			}
			continue
		}
		got := ParseResults(data)
		if len(got) == 0 && page > 1 {
			break // no more results
		}
		for _, u := range got {
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
