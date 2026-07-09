// Package api exposes the crawler's health and control HTTP endpoints.
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/ai-search/crawler/internal/blob"
	"github.com/ai-search/crawler/internal/store"
	"github.com/ai-search/crawler/internal/urlx"
)

type Server struct {
	store *store.Store
	blob  *blob.Store
}

func NewServer(st *store.Store, bl *blob.Store) *Server {
	return &Server{store: st, blob: bl}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.healthz)
	mux.HandleFunc("/readyz", s.readyz)
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/internal/campaigns", s.createCampaign) // POST
	mux.HandleFunc("/internal/frontier", s.frontier)        // GET ?campaign=ID
	mux.HandleFunc("/internal/coverage", s.coverage)        // GET
	mux.HandleFunc("/internal/documents/", s.document)      // GET /internal/documents/{id}
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
	added := 0
	for _, raw := range seeds {
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
