# 12 — Roadmap

Phased so each stage produces something runnable and measurable. Breadth-first, per owner
priority: get wide coverage working, then refine quality over already-collected data.

## Phase 0 — Infrastructure scaffold  ✅ done

**Goal:** one command brings up every backing service; skeleton crawler + AI service build & run.

Steps:
1. Repo structure (`crawler/` Go, `ai/` Python, `deploy/` compose, `config/`, `docs/`).
2. `docker-compose.yml`: Postgres, Redis, Qdrant, OpenSearch, MinIO, NATS, TEI (GPU),
   Prometheus, Grafana.
3. `.env.example`, `.gitignore`, `Makefile`/`Taskfile` (`up`, `down`, `ps`, `logs`).
4. Go crawler skeleton: config load, Postgres + Redis + NATS connectivity, `/healthz`.
5. Python AI service skeleton: FastAPI, connectivity to Qdrant/OpenSearch/TEI, `/healthz`.
6. DB migration tooling + initial schema ([06](06-DATA-MODEL.md)).
7. README quickstart; verify all services healthy.

**Exit criteria:** `make up` → all containers healthy; both skeletons pass health checks and can
reach their dependencies.

## Phase 1 — Crawler MVP  ✅ done

**Goal:** crawl the open (static) web end-to-end into stored, deduped documents.

- Frontier (Postgres + Redis, canonicalization, bloom dedupe, priority).
- Scheduler with per-host politeness + adaptive backoff.
- Go fetchers (HTTP, retries, UA rotation, blob store writes).
- Extractor (readability, metadata, language, exact + near dedupe).
- Link discovery + scope rules; campaign config.
- Crawl control API (submit seeds, frontier/job status).
- Metrics + dashboards for crawl health.

**Exit criteria:** seed a campaign → sustained fetch+extract with dedupe; documents + metadata
persisted; crawl dashboard live; target throughput demonstrated on a sample.

## Phase 2 — Search / RAG API  ✅ done

**Goal:** answer natural-language queries with cited answers over indexed content.

- [x] Crawler persists clean text (MinIO); poll-based chunker + TEI embeddings → Qdrant;
  text/metadata → OpenSearch (idempotent per `chunk_id`).
- [x] Hybrid retrieval (BM25 ∪ ANN) + RRF fusion + cross-encoder re-rank.
- [x] Filters (date/source/lang/domain); recall-first assembly (dedupe, diversify, backfill).
- [x] Local LLM synthesis (Ollama/llama3.1:8b on the RTX 5070) with numbered citations,
  streaming (SSE), citation verification, confidence + coverage. No paid API.
- [x] `/v1/search`, `/v1/retrieve`, `/v1/coverage`; response contract ([13](13-API.md)).
- [x] Eval harness (recall@k, MRR, nDCG; citation accuracy, LLM-as-judge groundedness).
- [x] Degradation paths (Qdrant → lexical-only, reranker → fused, LLM → retrieve-only).
- [~] Deferred/partial: richer query understanding (LLM expansion/decomposition) is minimal;
  embeddings + reranker run on **CPU** pending stable Blackwell/sm_120 TEI GPU kernels
  (image is one env swap away); multilingual `bge-reranker-v2-m3` swapped for MiniLM until then.

**Exit criteria (met):** end-to-end query → cited answer; recall@5/10 = 1.0, nDCG@10 = 0.91,
citation accuracy = 1.0, groundedness = 0.75 on the sample set; degradation paths verified.

## Phase 3 — JS + Social ingestion  ⬅️ in progress

**Goal:** cover dynamic and social content.

- [x] Playwright browser-worker pool + static→browser escalation. **Done: escalation gate +
  durable render queue + render-ingest boundary + the browser-worker process.** Gate: `crawler/internal/render`
  (`never|auto|always` policy + JS-app heuristics: sparse extracted text combined with SPA root
  markers / framework bundles / noscript prompts; wired into the scheduler, stamped into
  `documents.meta.needs_render` + `render_reasons`, counted via `crawler_render_escalations_total`).
  Queue: a frontier-shaped `render_queue` table (`PENDING→RENDERING→RENDERED|FAILED`,
  claim/lease/retry + stuck-render reaper) exposed over the control API (`GET /internal/render/queue`,
  `POST /internal/render/claim`, `POST /internal/render/complete`). Ingest: `POST
  /internal/render/ingest` lands a worker's rendered DOM in the documents + text-blob pipeline via
  the same `extract → blob → InsertDocument` path as a static fetch (`meta.rendered_by=browser`),
  marking the job RENDERED. Worker: `browser-worker/` (Node + Playwright, `app` profile) runs a
  long-lived Chromium that claims jobs, renders with a real browser (fresh isolated context per job,
  rotated viewport/locale/UA, lazy-load auto-scroll), and POSTs the resolved DOM to the ingest
  endpoint; verified live rendering a real Wikipedia SPA into the index.
- [ ] Anti-detection stack (fingerprints, proxies, sessions, pacing).
- [~] Social adapters, easy/open first (Reddit, Mastodon, Telegram public, YouTube transcripts),
  then hostile platforms. **Mastodon + Hacker News + Lemmy adapters done** (three credential-free
  open APIs — the ≥3-adapter exit bar is met on the ingestion side); Reddit/Telegram need
  owner-provided creds (will surface in `ralph/QUESTIONS.md`).
- [x] Social normalization (threads, engagement, media urls); social fetch queue.
  `NormalizedDoc` + `Meta()` plus an **`Ingester`** that drives adapters end-to-end
  (Discover→Fetch→Paginate→Parse→persist) and lands posts in the same `documents` + text-blob
  pipeline as web pages, triggered via `POST /internal/social/ingest`.
- [x] Adapter health metrics + auto-disable + contract tests. `HealthTracker` + Prometheus
  metrics + contract tests + **per-adapter status endpoint** (`social.Registry` →
  `GET /internal/social/adapters[/{name}]`) all done.

**Exit criteria:** JS pages render & index; ≥3 social adapters ingesting with health monitoring;
freshness cadence for tracked entities.

## Phase 4 — Scale & quality

**Goal:** harden, deepen, and refine over collected data.

- Recrawl/freshness scheduling; incremental/conditional GET; change detection.
- Enrichment: NER, keyphrases, topic tags; media OCR/ASR (optional).
- Query-time dedupe/diversification tuning; freshness-aware ranking.
- Coverage/stats API; monitoring/alerts (saved queries).
- Auth, API keys, rate limiting, usage metering (multi-tenant readiness).
- Backups, runbooks, capacity planning; quantize vectors for density.

**Exit criteria:** stable at target scale on current hardware; quality metrics improved vs P2;
operational runbooks + backups tested.

## Phase 5 — Web UI

**Goal:** client-facing product.

- Search UI: query box, streamed answer, source cards, filters/facets, history.
- Coverage/status views; monitoring dashboards for clients.
- Auth/onboarding; usage/billing views.

**Exit criteria:** a client can self-serve search with cited answers and see coverage.

## Later / scale-out (on owned hardware — no paid services)

- Distributed crawler fleet on the owner's own machines + self-run proxy pool; K8s/Nomad;
  clustered datastores.
- Synthesis LLM (and a larger model) on a dedicated GPU; vLLM for higher throughput.
- **Legal/compliance implementation** ([11](11-SECURITY-LEGAL.md)) — handled by the owner at the
  very end.

## Dependency graph

```
P0 ─▶ P1 ─▶ P2 ─▶ P4 ─▶ P5
             ▲       ▲
        P3 ──┘───────┘   (P3 needs P1 pipeline + P2 indexing; feeds P4/P5)
```
