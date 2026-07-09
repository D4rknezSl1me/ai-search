package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/ai-search/crawler/internal/extract"
	"github.com/ai-search/crawler/internal/metrics"
	"github.com/ai-search/crawler/internal/store"
	"github.com/ai-search/crawler/internal/urlx"
)

type renderIngestReq struct {
	ID       int64  `json:"id"`        // render_queue job id (0 ⇒ ingest without marking a job)
	URL      string `json:"url"`       // original (canonical) URL the job was queued for
	FinalURL string `json:"final_url"` // URL after in-browser redirects (defaults to url)
	Status   int    `json:"status"`    // browser HTTP status (defaults to 200)
	HTML     string `json:"html"`      // fully rendered DOM (document.documentElement.outerHTML)
}

// renderIngest lands a browser-rendered page in the crawler's storage, closing
// the loop the escalation gate opened: a (separate-language, docs/02) Playwright
// worker claims a job, renders the URL, and POSTs the resolved DOM here. The
// crawler — sole owner of the ingestion pipeline — runs that HTML through the
// exact same extract → blob → InsertDocument path as a static fetch, so rendered
// content is chunked/embedded identically, then marks the render job RENDERED.
// Keeping extraction/indexing in one place means the worker stays a thin renderer
// rather than a second, drifting copy of the pipeline.
//
// Unlike the static path this does NOT re-run the escalation gate or link
// discovery: the page has already been rendered (re-escalating would loop), and
// outlink expansion belongs to the static frontier that fed this URL in.
func (s *Server) renderIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	var req renderIngestReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if req.URL == "" || req.HTML == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "url and html are required"})
		return
	}
	if req.FinalURL == "" {
		req.FinalURL = req.URL
	}
	if req.Status == 0 {
		req.Status = 200
	}

	ctx := r.Context()
	doc, err := extract.FromHTML(req.FinalURL, []byte(req.HTML))
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "extract: " + err.Error()})
		return
	}

	// A render that yields no text is a failed render, not a stored empty doc —
	// report it so the worker can retry rather than silently marking RENDERED.
	if doc.Text == "" {
		metrics.RenderIngested.WithLabelValues("empty").Inc()
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error": "rendered page produced no extractable text", "id": req.ID,
		})
		return
	}

	// Raw rendered HTML + clean text to the blob store, keyed by content hash
	// (idempotent, same contract the intelligence plane reads).
	blobKey, err := s.blob.PutRaw(ctx, doc.ContentHash, []byte(req.HTML))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "blob put raw: " + err.Error()})
		return
	}
	if _, err := s.blob.PutText(ctx, doc.ContentHash, doc.Text); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "blob put text: " + err.Error()})
		return
	}

	host := urlx.Host(req.URL)
	if host == "" {
		host = urlx.Host(req.FinalURL)
	}
	sourceID, _ := s.store.EnsureSource(ctx, host)

	_, inserted, err := s.store.InsertDocument(ctx, &store.Document{
		URL:         req.URL,
		FinalURL:    req.FinalURL,
		SourceID:    sourceID,
		HTTPStatus:  req.Status,
		ContentType: "text/html",
		ContentHash: doc.ContentHash,
		Simhash:     int64(doc.Simhash),
		Title:       doc.Title,
		Author:      doc.Author,
		Lang:        doc.Lang,
		FetchedAt:   time.Now().UTC(),
		BlobKey:     blobKey,
		Meta: map[string]any{
			"excerpt":     doc.Excerpt,
			"site_name":   doc.SiteName,
			"text_len":    len(doc.Text),
			"rendered_by": "browser",
		},
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "insert document: " + err.Error()})
		return
	}
	if inserted {
		metrics.DocsIndexed.Inc()
		metrics.RenderIngested.WithLabelValues("indexed").Inc()
	} else {
		metrics.DocsDuplicate.Inc()
		metrics.RenderIngested.WithLabelValues("duplicate").Inc()
	}

	// Mark the queue job done (best-effort; ingest can be driven standalone with
	// id=0). Reuses the RENDERED terminal state so /internal/render/complete is
	// only needed for the failure path.
	if req.ID > 0 {
		if err := s.store.MarkRendered(ctx, req.ID); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "mark rendered: " + err.Error()})
			return
		}
		metrics.RenderQueueCompleted.WithLabelValues("rendered").Inc()
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"id":       req.ID,
		"state":    "RENDERED",
		"inserted": inserted,
		"text_len": len(doc.Text),
		"lang":     doc.Lang,
		"title":    doc.Title,
	})
}
