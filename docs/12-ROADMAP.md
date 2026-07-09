# 12 — Roadmap

Phased so each stage produces something runnable and measurable. Breadth-first, per owner
priority: get wide coverage working, then refine quality over already-collected data.

## Phase 0 — Infrastructure scaffold  ⬅️ current

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

## Phase 1 — Crawler MVP

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

## Phase 2 — Search / RAG API

**Goal:** answer natural-language queries with cited answers over indexed content.

- Chunker + GPU embeddings (TEI) → Qdrant; text/metadata → OpenSearch.
- Hybrid retrieval + RRF fusion + cross-encoder re-rank (GPU).
- Query understanding (intent, expansion, decomposition, filters).
- Local LLM serving (Ollama on the RTX 5070); grounded synthesis with citations, streaming,
  confidence + conflicts. No paid API.
- `/search` and `/retrieve` APIs; response contract ([13](13-API.md)).
- Eval harness (recall@k, groundedness, citation accuracy) + nightly run.
- Cost tracking.

**Exit criteria:** end-to-end query → cited answer; eval metrics meet v1 targets
([00](00-OVERVIEW.md) §5); degradation paths work.

## Phase 3 — JS + Social ingestion

**Goal:** cover dynamic and social content.

- Playwright browser-worker pool + static→browser escalation.
- Anti-detection stack (fingerprints, proxies, sessions, pacing).
- Social adapters, easy/open first (Reddit, Mastodon, Telegram public, YouTube transcripts),
  then hostile platforms.
- Social normalization (threads, engagement, media urls); social fetch queue.
- Adapter health metrics + auto-disable + contract tests.

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
