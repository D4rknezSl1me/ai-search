// Package api exposes the crawler's health and control HTTP endpoints.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/ai-search/crawler/internal/blob"
	"github.com/ai-search/crawler/internal/crawl"
	"github.com/ai-search/crawler/internal/feeds"
	"github.com/ai-search/crawler/internal/fetch"
	"github.com/ai-search/crawler/internal/metrics"
	"github.com/ai-search/crawler/internal/sitemap"
	"github.com/ai-search/crawler/internal/social"
	"github.com/ai-search/crawler/internal/store"
	"github.com/ai-search/crawler/internal/urlx"
)

// renderRetryBackoff spaces out re-render attempts after a browser worker
// reports a retryable failure.
const renderRetryBackoff = 60 * time.Second

// sitemap fetch defaults (control-plane fetch, independent of a campaign).
const (
	sitemapFetchTimeout = 20 * time.Second
	sitemapMaxBodyBytes = 20 << 20 // sitemaps can be large; cap at 20 MiB
	sitemapUserAgent    = "Mozilla/5.0 (compatible; ai-search/0.1; +sitemap)"
)

type Server struct {
	store   *store.Store
	blob    *blob.Store
	social  *social.Registry
	fetcher *fetch.Fetcher
}

func NewServer(st *store.Store, bl *blob.Store, sr *social.Registry) *Server {
	return &Server{
		store:   st,
		blob:    bl,
		social:  sr,
		fetcher: fetch.New(sitemapFetchTimeout, sitemapMaxBodyBytes, sitemapUserAgent),
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.healthz)
	mux.HandleFunc("/readyz", s.readyz)
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/internal/campaigns", s.createCampaign)       // POST
	mux.HandleFunc("/internal/frontier", s.frontier)              // GET ?campaign=ID
	mux.HandleFunc("/internal/coverage", s.coverage)              // GET
	mux.HandleFunc("/internal/documents/", s.document)            // GET /internal/documents/{id}
	mux.HandleFunc("/internal/social/adapters", s.socialAdapters) // GET (all)
	mux.HandleFunc("/internal/social/adapters/", s.socialAdapter) // GET /.../{name}
	mux.HandleFunc("/internal/social/ingest", s.socialIngest)     // POST
	mux.HandleFunc("/internal/social/tracked", s.socialTracked)   // GET | POST | DELETE
	mux.HandleFunc("/internal/render/queue", s.renderQueue)       // GET ?campaign=ID
	mux.HandleFunc("/internal/render/claim", s.renderClaim)       // POST {"n":N}
	mux.HandleFunc("/internal/render/complete", s.renderComplete) // POST {"id":ID,"ok":bool}
	mux.HandleFunc("/internal/render/ingest", s.renderIngest)     // POST {"id":ID,"url":...,"html":...}
	mux.HandleFunc("/internal/sitemap/ingest", s.sitemapIngest)   // POST {"campaign_id":ID,"url":...}
	mux.HandleFunc("/internal/feeds/ingest", s.feedsIngest)       // POST {"campaign_id":ID,"url":...}
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "crawler"})
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	deps := map[string]string{"postgres": "ok", "minio": "ok"}
	ready := true
	if err := s.store.Ping(r.Context()); err != nil {
		deps["postgres"] = "unreachable"
		ready = false
	}
	if err := s.blob.Ping(r.Context()); err != nil {
		deps["minio"] = "unreachable"
		ready = false
	}
	code := http.StatusOK
	state := "ready"
	if !ready {
		code = http.StatusServiceUnavailable
		state = "not_ready"
	}
	writeJSON(w, code, map[string]any{"status": state, "dependencies": deps})
}

type createCampaignReq struct {
	Name          string   `json:"name"`
	Seeds         []string `json:"seeds"`
	MaxDepth      int      `json:"max_depth"`
	MaxPages      int      `json:"max_pages"`
	AllowExternal bool     `json:"allow_external"`
	MinDelayMs    int      `json:"min_delay_ms"`
	RenderJS      string   `json:"render_js"` // never | auto | always (empty ⇒ auto)
}

func (s *Server) createCampaign(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	var req createCampaignReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if req.Name == "" || len(req.Seeds) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name and seeds are required"})
		return
	}
	if req.MaxDepth == 0 {
		req.MaxDepth = 2
	}

	ctx := r.Context()
	cfg := store.CampaignConfig{
		Seeds:         req.Seeds,
		MaxDepth:      req.MaxDepth,
		MaxPages:      req.MaxPages,
		AllowExternal: req.AllowExternal,
		MinDelayMs:    req.MinDelayMs,
		RenderJS:      req.RenderJS,
	}
	campaignID, err := s.store.UpsertCampaign(ctx, req.Name, cfg)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	seeded := s.seed(ctx, campaignID, req.Seeds)
	writeJSON(w, http.StatusCreated, map[string]any{
		"campaign_id": campaignID,
		"name":        req.Name,
		"seeded":      seeded,
	})
}

// seed enqueues seed URLs at depth 0 and returns how many were newly added.
func (s *Server) seed(ctx context.Context, campaignID int64, seeds []string) int {
	return s.enqueueURLs(ctx, campaignID, seeds)
}

// enqueueURLs canonicalizes and enqueues a batch of URLs at depth 0, returning
// how many were newly added (dedup via the frontier's unique constraint).
func (s *Server) enqueueURLs(ctx context.Context, campaignID int64, urls []string) int {
	added := 0
	for _, raw := range urls {
		canon, err := urlx.Canonicalize(raw)
		if err != nil {
			continue
		}
		host := urlx.Host(canon)
		if host == "" {
			continue
		}
		_, _ = s.store.EnsureSource(ctx, host)
		ok, err := s.store.AddURL(ctx, campaignID, canon, urlx.Hash(canon), host, 0, 1.0)
		if err == nil && ok {
			added++
		}
	}
	return added
}

type sitemapIngestReq struct {
	CampaignID int64  `json:"campaign_id"`
	URL        string `json:"url"`
	MaxURLs    int    `json:"max_urls"`
}

// sitemapIngest fetches a sitemap (or sitemap index) and enqueues the page URLs
// it lists into the given campaign's frontier — bulk breadth discovery beyond
// link-following (recall-first, CLAUDE.md north star).
func (s *Server) sitemapIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	var req sitemapIngestReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	req.URL = strings.TrimSpace(req.URL)
	if req.CampaignID == 0 || req.URL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "campaign_id and url are required"})
		return
	}
	ctx := r.Context()
	if _, err := s.store.GetCampaign(ctx, req.CampaignID); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown campaign"})
		return
	}

	urls, err := sitemap.Discover(ctx, req.URL, s.fetchBytes, sitemap.Limits{MaxURLs: req.MaxURLs})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	enqueued := s.enqueueURLs(ctx, req.CampaignID, urls)
	metrics.SitemapURLs.Add(float64(enqueued))
	writeJSON(w, http.StatusOK, map[string]any{
		"campaign_id": req.CampaignID,
		"discovered":  len(urls),
		"enqueued":    enqueued,
	})
}

// fetchBytes GETs a control-plane URL (sitemap/feed) and returns its body,
// erroring on any non-2xx status.
func (s *Server) fetchBytes(ctx context.Context, u string) ([]byte, error) {
	res, err := s.fetcher.Get(ctx, u)
	if err != nil {
		return nil, err
	}
	if res.Status < 200 || res.Status >= 300 {
		return nil, fmt.Errorf("fetch %s: status %d", u, res.Status)
	}
	return res.Body, nil
}

type feedsIngestReq struct {
	CampaignID int64  `json:"campaign_id"`
	URL        string `json:"url"`
	MaxURLs    int    `json:"max_urls"`
}

// feedsIngest fetches an RSS/Atom feed and enqueues its item URLs into the
// given campaign's frontier — breadth + freshness discovery (recall-first).
func (s *Server) feedsIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	var req feedsIngestReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	req.URL = strings.TrimSpace(req.URL)
	if req.CampaignID == 0 || req.URL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "campaign_id and url are required"})
		return
	}
	ctx := r.Context()
	if _, err := s.store.GetCampaign(ctx, req.CampaignID); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown campaign"})
		return
	}

	data, err := s.fetchBytes(ctx, req.URL)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	urls, err := feeds.Parse(data)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	if req.MaxURLs > 0 && len(urls) > req.MaxURLs {
		urls = urls[:req.MaxURLs]
	}
	enqueued := s.enqueueURLs(ctx, req.CampaignID, urls)
	metrics.FeedURLs.Add(float64(enqueued))
	writeJSON(w, http.StatusOK, map[string]any{
		"campaign_id": req.CampaignID,
		"discovered":  len(urls),
		"enqueued":    enqueued,
	})
}

func (s *Server) frontier(w http.ResponseWriter, r *http.Request) {
	idStr := r.URL.Query().Get("campaign")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "campaign query param required"})
		return
	}
	stats, err := s.store.FrontierStats(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"campaign_id": id, "frontier": stats})
}

func (s *Server) coverage(w http.ResponseWriter, r *http.Request) {
	total, err := s.store.TotalDocuments(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"total_documents": total})
}

func (s *Server) document(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimPrefix(r.URL.Path, "/internal/documents/")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid document id"})
		return
	}
	doc, err := s.store.GetDocumentMeta(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

// socialAdapters reports live health for every registered social adapter plus an
// aggregate summary — the per-adapter monitoring surface for Phase 3 (docs/08
// §8). Operators poll this to see which platforms are ingesting and whether any
// auto-disabled on error rate.
func (s *Server) socialAdapters(w http.ResponseWriter, _ *http.Request) {
	if s.social == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"summary": social.Summary{}, "adapters": []social.Health{},
		})
		return
	}
	statuses := s.social.Statuses()
	writeJSON(w, http.StatusOK, map[string]any{
		"summary":  social.Summarize(statuses),
		"adapters": statuses,
	})
}

type socialIngestReq struct {
	Adapter  string `json:"adapter"`
	Seed     string `json:"seed"`
	MaxPages int    `json:"max_pages"` // pagination cap per target; 0 → adapter default
}

// socialIngest drives one adapter over one seed synchronously and lands the
// resulting posts in the same documents + text-blob pipeline as web pages
// (docs/08 §7). This is the operator-facing trigger for the social fetch path:
// previously the adapters could only report health; now a seed can actually be
// ingested. The run summary (targets/pages/docs/inserted/duplicates/errors) is
// returned so the caller sees exactly what landed.
func (s *Server) socialIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	if s.social == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "social registry not configured"})
		return
	}
	var req socialIngestReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if req.Adapter == "" || req.Seed == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "adapter and seed are required"})
		return
	}

	ing := social.NewIngester(s.social, &socialSink{store: s.store, blob: s.blob}, req.MaxPages, 0)
	res, err := ing.IngestSeed(r.Context(), req.Adapter, req.Seed)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error(), "result": res})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// minTrackCadenceS floors the freshness cadence so an operator can't register an
// entity that hammers a platform every second. Ten seconds is well below any
// sensible social cadence while still preventing pathological configs.
const minTrackCadenceS = 10

type trackReq struct {
	Adapter        string `json:"adapter"`
	Seed           string `json:"seed"`
	CadenceSeconds int    `json:"cadence_seconds"`
	MaxPages       int    `json:"max_pages"`
}

// socialTracked manages the freshness registry — the set of (adapter, seed)
// entities the crawler re-ingests on a cadence (docs/08 §7). GET lists them
// (with each entity's schedule, last result, and last error for freshness-lag
// visibility); POST registers/updates one; DELETE removes one. The scheduler in
// main claims due entities from this table every tick.
func (s *Server) socialTracked(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		entities, err := s.store.ListTrackedEntities(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if entities == nil {
			entities = []store.TrackedEntity{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"count": len(entities), "tracked": entities})

	case http.MethodPost:
		var req trackReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		req.Adapter, req.Seed = strings.TrimSpace(req.Adapter), strings.TrimSpace(req.Seed)
		if req.Adapter == "" || req.Seed == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "adapter and seed are required"})
			return
		}
		// Reject unknown adapters up front — a tracked entity the scheduler can
		// never run is a silent freshness gap.
		if s.social != nil {
			if _, ok := s.social.Get(req.Adapter); !ok {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown adapter: " + req.Adapter})
				return
			}
		}
		if req.CadenceSeconds < minTrackCadenceS {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "cadence_seconds must be >= " + strconv.Itoa(minTrackCadenceS),
			})
			return
		}
		te, err := s.store.UpsertTrackedEntity(r.Context(), req.Adapter, req.Seed, req.CadenceSeconds, req.MaxPages)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, te)

	case http.MethodDelete:
		var req trackReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		req.Adapter, req.Seed = strings.TrimSpace(req.Adapter), strings.TrimSpace(req.Seed)
		if req.Adapter == "" || req.Seed == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "adapter and seed are required"})
			return
		}
		removed, err := s.store.DeleteTrackedEntity(r.Context(), req.Adapter, req.Seed)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if !removed {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not tracked"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"removed": true, "adapter": req.Adapter, "seed": req.Seed})

	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET, POST, or DELETE only"})
	}
}

// renderQueue reports render-queue counts by state. Global by default, or scoped
// to a campaign with ?campaign=ID. This is the observability surface for the
// static→browser escalation lane (docs/04 §5, §10).
func (s *Server) renderQueue(w http.ResponseWriter, r *http.Request) {
	var campaignID int64
	if v := r.URL.Query().Get("campaign"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid campaign id"})
			return
		}
		campaignID = id
	}
	stats, err := s.store.RenderQueueStats(r.Context(), campaignID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"campaign_id": campaignID, "render_queue": stats})
}

type renderClaimReq struct {
	N int `json:"n"`
}

// renderClaim hands a batch of PENDING render jobs to a browser worker, marking
// them RENDERING under a lease (claimed_at). The browser-worker pool is a
// separate-language service (docs/02) that consumes this over HTTP; the crawler
// owns the queue. Jobs not completed before the reaper's timeout are requeued.
func (s *Server) renderClaim(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	req := renderClaimReq{N: 1}
	// Body is optional; default to a single job when absent/blank.
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	if req.N <= 0 {
		req.N = 1
	}
	if req.N > 100 {
		req.N = 100
	}
	items, err := s.store.ClaimNextRender(r.Context(), req.N)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	metrics.RenderQueueClaimed.Add(float64(len(items)))
	if items == nil {
		items = []store.RenderItem{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"claimed": len(items), "items": items})
}

type renderCompleteReq struct {
	ID    int64 `json:"id"`
	OK    bool  `json:"ok"`
	Retry bool  `json:"retry"` // on failure, whether to reschedule (default: retry)
}

// renderComplete records the outcome of a claimed render job. ok=true marks it
// RENDERED; ok=false marks a failure, retrying (up to crawl.MaxAttempts) unless
// the worker explicitly opts out. Idempotent-ish: completing an unknown id is a
// no-op update.
func (s *Server) renderComplete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	req := renderCompleteReq{Retry: true}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if req.ID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id is required"})
		return
	}
	ctx := r.Context()
	if req.OK {
		if err := s.store.MarkRendered(ctx, req.ID); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		metrics.RenderQueueCompleted.WithLabelValues("rendered").Inc()
		writeJSON(w, http.StatusOK, map[string]any{"id": req.ID, "state": "RENDERED"})
		return
	}
	if err := s.store.MarkRenderFailed(ctx, req.ID, req.Retry, crawl.MaxAttempts, renderRetryBackoff); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	metrics.RenderQueueCompleted.WithLabelValues("failed").Inc()
	writeJSON(w, http.StatusOK, map[string]any{"id": req.ID, "ok": false, "retry": req.Retry})
}

// socialAdapter reports one adapter's health by name.
func (s *Server) socialAdapter(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/internal/social/adapters/")
	if name == "" {
		s.socialAdapters(w, r)
		return
	}
	if s.social == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "adapter not found"})
		return
	}
	status, ok := s.social.Status(name)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "adapter not found: " + name})
		return
	}
	writeJSON(w, http.StatusOK, status)
}
