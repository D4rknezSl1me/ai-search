// Command crawler is the ai-search ingestion service.
//
// Phase 0: this is a skeleton. It loads configuration from the environment,
// exposes health/readiness endpoints, and verifies TCP connectivity to its
// backing services (Postgres, Redis, NATS). The actual frontier, fetchers and
// extractor arrive in Phase 1 (see docs/04-CRAWLER.md, docs/12-ROADMAP.md).
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"time"
)

// dep is a backing service the crawler depends on.
type dep struct {
	Name string
	Addr string
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func deps() []dep {
	return []dep{
		{"postgres", net.JoinHostPort(env("POSTGRES_HOST", "postgres"), env("POSTGRES_PORT", "5432"))},
		{"redis", net.JoinHostPort(env("REDIS_HOST", "redis"), env("REDIS_PORT", "6379"))},
		{"nats", net.JoinHostPort(env("NATS_HOST", "nats"), env("NATS_PORT", "4222"))},
	}
}

// checkTCP reports whether a TCP connection to addr can be established.
func checkTCP(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// healthz is liveness: the process is up.
func healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "crawler"})
}

// readyz is readiness: all backing services are reachable.
func readyz(w http.ResponseWriter, _ *http.Request) {
	results := map[string]string{}
	ready := true
	for _, d := range deps() {
		if checkTCP(d.Addr) {
			results[d.Name] = "ok"
		} else {
			results[d.Name] = "unreachable"
			ready = false
		}
	}
	status := http.StatusOK
	state := "ready"
	if !ready {
		status = http.StatusServiceUnavailable
		state = "not_ready"
	}
	writeJSON(w, status, map[string]any{"status": state, "dependencies": results})
}

func main() {
	port := env("CRAWLER_HEALTH_PORT", "8090")

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", healthz)
	mux.HandleFunc("/readyz", readyz)
	// Placeholder so Prometheus scrape config doesn't error; real metrics in Phase 1.
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "# ai-search crawler metrics placeholder\n")
	})

	addr := ":" + port
	log.Printf("crawler skeleton listening on %s (health: /healthz, /readyz)", addr)
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
