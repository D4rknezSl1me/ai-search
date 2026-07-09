// Package social provides the per-platform adapter framework for ingesting
// social content (Phase 3). Each adapter turns a platform's API/JSON into the
// crawler's normalized Document model so social posts flow through the same
// store → index → RAG pipeline as web pages. See docs/08-SOCIAL-MEDIA.md.
//
// Adapters are deliberately credential-free first: the initial implementations
// target open platforms whose public endpoints need no auth (e.g. Mastodon),
// so the pipeline can be built and tested end-to-end before investing in the
// hostile, login-walled platforms (docs/08 §5).
package social

import (
	"context"
	"crypto/sha256"
	"strings"
	"time"

	"github.com/ai-search/crawler/internal/simhash"
)

// RawContent is a fetched, not-yet-parsed response from a social endpoint.
type RawContent struct {
	Target      string // the URL/endpoint that was fetched
	ContentType string
	Body        []byte
	// Next is the pagination cursor/URL for the following page, if the endpoint
	// advertised one (e.g. Mastodon's Link: rel="next" header). Empty when the
	// end of the timeline is reached.
	Next string
}

// NormalizedDoc is a single social post or comment mapped onto the crawler's
// Document model. The platform-specific fields live in Meta() (docs/08 §6);
// Text/ContentHash/Simhash mirror what extract.Document carries for web pages
// so social docs dedupe and index identically.
type NormalizedDoc struct {
	Platform     string
	PostID       string
	AuthorHandle string
	PostedAt     time.Time
	Permalink    string
	ParentID     string // parent post id for threads/comments, "" for roots
	Title        string
	Text         string
	Lang         string
	MediaURLs    []string
	Engagement   map[string]int // likes/shares/replies (platform-specific keys)

	ContentHash []byte
	Simhash     uint64
}

// finalize computes the content hash and near-dup fingerprint for a doc after
// its text fields are set. Exact-dedup identity is the post itself
// (platform+post_id) rather than the text, so two distinct posts that happen to
// share identical short text ("gm") are not collapsed by the documents table's
// unique content_hash constraint; simhash still catches near-duplicates.
func (d *NormalizedDoc) finalize() {
	sum := sha256.Sum256([]byte(d.Platform + "\x00" + d.PostID))
	d.ContentHash = sum[:]
	d.Simhash = simhash.Compute(d.Text)
}

// Meta builds the documents.meta JSON map (docs/08 §6). Empty optional fields
// are omitted to keep the payload tidy.
func (d *NormalizedDoc) Meta() map[string]any {
	m := map[string]any{
		"source":        "social",
		"platform":      d.Platform,
		"post_id":       d.PostID,
		"author_handle": d.AuthorHandle,
		"permalink":     d.Permalink,
		"posted_at":     d.PostedAt.UTC().Format(time.RFC3339),
		"engagement":    d.Engagement,
		"text_len":      len(d.Text),
	}
	if d.ParentID != "" {
		m["parent_id"] = d.ParentID
	}
	if len(d.MediaURLs) > 0 {
		m["media_urls"] = d.MediaURLs
	}
	return m
}

// Adapter is the common interface every platform implements (docs/08 §2).
// Adapters plug into the same frontier → fetch → extract → index pipeline but
// route through a dedicated, session-aware social path.
type Adapter interface {
	// Name is the platform label used in metrics and documents.meta.platform.
	Name() string
	// Discover turns a seed (an instance host, profile, or hashtag) into one or
	// more concrete fetch targets (API endpoints).
	Discover(ctx context.Context, seed string) ([]string, error)
	// Fetch retrieves a target, returning the raw body plus any pagination
	// cursor the platform advertised.
	Fetch(ctx context.Context, target string) (*RawContent, error)
	// Parse turns raw content into normalized documents.
	Parse(raw *RawContent) ([]NormalizedDoc, error)
	// Paginate returns the next target to fetch, or ("", false) at the end.
	Paginate(raw *RawContent) (next string, ok bool)
	// Health reports the adapter's current status for monitoring/auto-disable.
	Health() Health
}

// htmlToText flattens a fragment of HTML (as social platforms return in post
// bodies) to whitespace-collapsed plain text. Entities are unescaped and block
// breaks (<br>, </p>) become spaces so words don't run together.
func htmlToText(s string) string {
	// Reuse the tokenizer-based approach without importing extract (which is
	// web-page specific). Kept local and small on purpose.
	return collapseWS(stripTags(s))
}

func collapseWS(s string) string { return strings.Join(strings.Fields(s), " ") }
