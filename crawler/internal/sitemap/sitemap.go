// Package sitemap discovers seed URLs from XML sitemaps and sitemap indexes.
//
// Sitemaps are the highest-leverage breadth source for the open web: one
// document can list every canonical URL a site wants crawled, so ingesting them
// pulls thousands of pages into the frontier in one shot rather than waiting to
// discover them link-by-link. Breadth-first / recall-first (CLAUDE.md north
// star). The parser is pure and the crawl driver takes an injected fetch func,
// so both are unit-testable offline with no network.
package sitemap

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/xml"
	"io"
	"strings"
)

// Result is the parsed content of one sitemap document: page URLs (from a
// <urlset>) and/or child sitemap URLs (from a <sitemapindex>).
type Result struct {
	URLs     []string
	Sitemaps []string
}

type locEntry struct {
	Loc string `xml:"loc"`
}

// sitemapDoc matches both document shapes by child element local-name: a
// <urlset> fills URLSet via <url>, a <sitemapindex> fills Index via <sitemap>.
// Unqualified tags match regardless of the sitemap XML namespace.
type sitemapDoc struct {
	XMLName xml.Name
	URLSet  []locEntry `xml:"url"`
	Index   []locEntry `xml:"sitemap"`
}

// Parse decodes a sitemap or sitemap-index document, transparently gunzipping a
// gzip-compressed payload (sitemaps are commonly served as .xml.gz).
func Parse(data []byte) (*Result, error) {
	if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
		zr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		if data, err = io.ReadAll(zr); err != nil {
			return nil, err
		}
	}
	var doc sitemapDoc
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	res := &Result{}
	for _, e := range doc.URLSet {
		if u := strings.TrimSpace(e.Loc); u != "" {
			res.URLs = append(res.URLs, u)
		}
	}
	for _, e := range doc.Index {
		if u := strings.TrimSpace(e.Loc); u != "" {
			res.Sitemaps = append(res.Sitemaps, u)
		}
	}
	return res, nil
}

// FetchFunc returns the raw bytes at a URL. The real implementation wraps the
// crawler's HTTP fetcher; tests inject a fake. A returned error skips that one
// document rather than aborting the whole run.
type FetchFunc func(ctx context.Context, url string) ([]byte, error)

// Limits bound a discovery run so a huge (or hostile) sitemap index can't fan
// out or grow memory without end.
type Limits struct {
	MaxURLs     int // stop collecting page URLs past this many
	MaxSitemaps int // stop fetching child sitemaps past this many
}

func (l Limits) withDefaults() Limits {
	if l.MaxURLs <= 0 {
		l.MaxURLs = 50000
	}
	if l.MaxSitemaps <= 0 {
		l.MaxSitemaps = 50
	}
	return l
}

// Discover fetches the sitemap (or sitemap index) at rootURL and returns the
// de-duplicated page URLs it references, following one level of sitemap-index
// nesting (the common case) up to the configured limits. A failure to fetch or
// parse the root is fatal; a bad child sitemap is skipped (best-effort,
// recall-first — one broken shard shouldn't lose the rest).
func Discover(ctx context.Context, rootURL string, fetch FetchFunc, lim Limits) ([]string, error) {
	lim = lim.withDefaults()
	data, err := fetch(ctx, rootURL)
	if err != nil {
		return nil, err
	}
	root, err := Parse(data)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]struct{})
	var urls []string
	add := func(u string) {
		if _, ok := seen[u]; ok {
			return
		}
		seen[u] = struct{}{}
		urls = append(urls, u)
	}

	for _, u := range root.URLs {
		if len(urls) >= lim.MaxURLs {
			return urls, nil
		}
		add(u)
	}

	fetched := 0
	for _, sm := range root.Sitemaps {
		if len(urls) >= lim.MaxURLs || fetched >= lim.MaxSitemaps {
			break
		}
		fetched++
		cdata, err := fetch(ctx, sm)
		if err != nil {
			continue
		}
		child, err := Parse(cdata)
		if err != nil {
			continue
		}
		for _, u := range child.URLs {
			if len(urls) >= lim.MaxURLs {
				break
			}
			add(u)
		}
	}
	return urls, nil
}
