// Package extract turns raw HTML into a normalized document: main content,
// metadata, discovered links, language, and content/near-dup hashes.
package extract

import (
	"bytes"
	"crypto/sha256"
	"net/url"
	"strings"
	"time"

	readability "github.com/go-shiori/go-readability"
	"github.com/abadojack/whatlanggo"
	"golang.org/x/net/html"

	"github.com/ai-search/crawler/internal/simhash"
	"github.com/ai-search/crawler/internal/urlx"
)

type Document struct {
	Title       string
	Author      string
	Text        string
	Lang        string
	PublishedAt *time.Time
	Excerpt     string
	SiteName    string
	ContentHash []byte
	Simhash     uint64
	Links       []string
}

// FromHTML extracts a Document from raw HTML fetched at finalURL.
func FromHTML(finalURL string, raw []byte) (*Document, error) {
	pageURL, err := url.Parse(finalURL)
	if err != nil {
		return nil, err
	}

	art, err := readability.FromReader(bytes.NewReader(raw), pageURL)
	if err != nil {
		// Fall back to raw text so extraction never hard-fails (recall first).
		text := stripToText(raw)
		return buildDoc("", "", text, nil, "", "", finalURL, raw), nil
	}

	links := extractLinks(finalURL, raw)
	doc := buildDoc(art.Title, art.Byline, art.TextContent, art.PublishedTime,
		art.Excerpt, art.SiteName, finalURL, raw)
	doc.Links = links
	return doc, nil
}

// FromPlainText builds a Document from a non-HTML textual body (text/plain,
// markdown, csv, …). There is no readability step — the body is the content —
// so recall-first, such pages now reach the index instead of being dropped. The
// title is the first non-blank line; there is no link discovery.
func FromPlainText(finalURL string, raw []byte) (*Document, error) {
	text := strings.TrimSpace(string(raw))
	return buildDoc(firstLine(text), "", text, nil, truncateRunes(text, 280), "", finalURL, raw), nil
}

// firstLine returns the first non-blank line, capped, as a title.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return truncateRunes(t, 200)
		}
	}
	return ""
}

// truncateRunes caps a string at n runes (never splitting a UTF-8 sequence).
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func buildDoc(title, author, text string, published *time.Time, excerpt, site, finalURL string, raw []byte) *Document {
	text = strings.TrimSpace(text)
	sum := sha256.Sum256([]byte(text))

	lang := ""
	if len(text) >= 20 {
		info := whatlanggo.Detect(text)
		if info.IsReliable() {
			lang = info.Lang.Iso6391()
		}
	}

	return &Document{
		Title:       strings.TrimSpace(title),
		Author:      strings.TrimSpace(author),
		Text:        text,
		Lang:        lang,
		PublishedAt: published,
		Excerpt:     strings.TrimSpace(excerpt),
		SiteName:    strings.TrimSpace(site),
		ContentHash: sum[:],
		Simhash:     simhash.Compute(text),
	}
}

// extractLinks pulls absolute http(s) hrefs from raw HTML for discovery.
func extractLinks(base string, raw []byte) []string {
	tokenizer := html.NewTokenizer(bytes.NewReader(raw))
	seen := map[string]bool{}
	var links []string
	for {
		tt := tokenizer.Next()
		if tt == html.ErrorToken {
			break
		}
		if tt != html.StartTagToken && tt != html.SelfClosingTagToken {
			continue
		}
		tok := tokenizer.Token()
		if tok.Data != "a" {
			continue
		}
		for _, attr := range tok.Attr {
			if attr.Key != "href" {
				continue
			}
			abs, ok := urlx.Resolve(base, attr.Val)
			if !ok {
				continue
			}
			canon, err := urlx.Canonicalize(abs)
			if err != nil || seen[canon] {
				continue
			}
			seen[canon] = true
			links = append(links, canon)
		}
	}
	return links
}

// stripToText is a crude fallback that removes tags, leaving text.
func stripToText(raw []byte) string {
	tokenizer := html.NewTokenizer(bytes.NewReader(raw))
	var b strings.Builder
	skip := false
	for {
		tt := tokenizer.Next()
		if tt == html.ErrorToken {
			break
		}
		switch tt {
		case html.StartTagToken, html.SelfClosingTagToken:
			name, _ := tokenizer.TagName()
			n := string(name)
			if n == "script" || n == "style" || n == "noscript" {
				skip = true
			}
		case html.EndTagToken:
			name, _ := tokenizer.TagName()
			n := string(name)
			if n == "script" || n == "style" || n == "noscript" {
				skip = false
			}
		case html.TextToken:
			if !skip {
				b.Write(tokenizer.Text())
				b.WriteByte(' ')
			}
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
