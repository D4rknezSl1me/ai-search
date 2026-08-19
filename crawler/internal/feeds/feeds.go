// Package feeds discovers seed URLs from RSS and Atom feeds.
//
// Feeds are a free/open breadth-and-freshness source (docs/04 §7): most sites,
// blogs, and news outlets publish one, and it lists their newest items' URLs
// directly. Ingesting a feed pushes those URLs into the frontier without waiting
// to discover them by link-following — breadth-first / recall-first (CLAUDE.md).
// The parser is pure (stdlib encoding/xml) and unit-testable offline.
package feeds

import (
	"bytes"
	"compress/gzip"
	"encoding/xml"
	"io"
	"strings"
)

// link captures both feed shapes: an RSS <link>URL</link> (character data) and
// an Atom <link href="URL" rel="alternate" type="text/html"/> (attributes).
type link struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
	Type string `xml:"type,attr"`
	Text string `xml:",chardata"`
}

type entry struct {
	Links []link `xml:"link"`
}

type channel struct {
	Items []entry `xml:"item"`
}

// feedDoc matches RSS 2.0 (rss>channel>item), Atom (feed>entry), and RSS 1.0 /
// RDF (RDF>item at the top level) by element local-name, so the feed namespace
// is irrelevant.
type feedDoc struct {
	XMLName xml.Name
	Channel channel `xml:"channel"`
	Entries []entry `xml:"entry"`
	Items   []entry `xml:"item"`
}

// bestLink picks the canonical page URL for one item/entry: an RSS <link> text,
// or the Atom alternate/html link (falling back to the first href present).
func bestLink(links []link) string {
	var fallback string
	for _, l := range links {
		if t := strings.TrimSpace(l.Text); t != "" && l.Href == "" {
			return t // RSS <link>URL</link>
		}
		href := strings.TrimSpace(l.Href)
		if href == "" {
			continue
		}
		if (l.Rel == "" || l.Rel == "alternate") && (l.Type == "" || strings.Contains(l.Type, "html")) {
			return href // preferred Atom link
		}
		if fallback == "" {
			fallback = href
		}
	}
	return fallback
}

// Parse decodes an RSS/Atom/RDF feed and returns the de-duplicated item URLs, in
// document order. Gzip payloads are transparently inflated.
func Parse(data []byte) ([]string, error) {
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
	var doc feedDoc
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}

	seen := make(map[string]struct{})
	var urls []string
	add := func(e entry) {
		u := strings.TrimSpace(bestLink(e.Links))
		if u == "" {
			return
		}
		if _, ok := seen[u]; ok {
			return
		}
		seen[u] = struct{}{}
		urls = append(urls, u)
	}
	for _, e := range doc.Channel.Items { // RSS 2.0
		add(e)
	}
	for _, e := range doc.Entries { // Atom
		add(e)
	}
	for _, e := range doc.Items { // RSS 1.0 / RDF (top-level <item>)
		add(e)
	}
	return urls, nil
}
