package api

import (
	"context"
	"time"

	"github.com/ai-search/crawler/internal/blob"
	"github.com/ai-search/crawler/internal/social"
	"github.com/ai-search/crawler/internal/store"
	"github.com/ai-search/crawler/internal/urlx"
)

// socialSink lands a normalized social post in the crawler's storage exactly the
// way the crawl scheduler lands a web page: the clean text goes to the blob store
// (keyed by content hash, so the intelligence plane chunks/embeds it identically)
// and a documents row carries the social metadata. Exact-dedup is by the post's
// content hash (platform+post_id), so re-ingesting the same post is a no-op
// insert reported back as a duplicate.
type socialSink struct {
	store *store.Store
	blob  *blob.Store
}

// NewSocialSink builds the shared social persistence sink so callers outside the
// API package (e.g. the freshness scheduler in main) drive ingests through the
// exact same store+blob landing path as POST /internal/social/ingest.
func NewSocialSink(st *store.Store, bl *blob.Store) social.Sink {
	return &socialSink{store: st, blob: bl}
}

func (s *socialSink) Persist(ctx context.Context, d *social.NormalizedDoc) (bool, error) {
	// Clean text first (ungated by the insert dedup), keyed by content hash —
	// same contract as web docs so a re-ingest backfills text for known posts.
	blobKey := ""
	if d.Text != "" {
		if _, err := s.blob.PutText(ctx, d.ContentHash, d.Text); err != nil {
			return false, err
		}
		blobKey = blob.TextKey(d.ContentHash)
	}

	// Source host: prefer the post's permalink host (the real instance, e.g.
	// mastodon.social), falling back to the platform label when there is none.
	host := d.Platform
	if h := urlx.Host(d.Permalink); h != "" {
		host = h
	}
	sourceID, _ := s.store.EnsureSource(ctx, host)

	_, inserted, err := s.store.InsertDocument(ctx, &store.Document{
		URL:         d.Permalink,
		FinalURL:    d.Permalink,
		SourceID:    sourceID,
		HTTPStatus:  200,
		ContentType: "application/social+json",
		ContentHash: d.ContentHash,
		Simhash:     int64(d.Simhash),
		Title:       d.Title,
		Author:      d.AuthorHandle,
		Lang:        d.Lang,
		FetchedAt:   time.Now().UTC(),
		BlobKey:     blobKey,
		Meta:        d.Meta(),
	})
	return inserted, err
}
