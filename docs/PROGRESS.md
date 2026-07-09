# Project Progress Log

Reverse-chronological record of meaningful changes. Update this on every meaningful change
(see `CLAUDE.md` rule 3). Format: date · what · why · verification.

## Backlog (deferred follow-ups)

- [x] **FETCHING reaper** — done (2026-07-09). `frontier_urls.claimed_at` + goroutine ticker
  (`CRAWLER_REAP_AFTER_S`, default 300s) requeues stuck URLs under the retry cap.
- [x] **Unit tests** — done (2026-07-09). `urlx` and `simhash` tests, run via `go test` in a
  golang container.
- [ ] **GPU embeddings/reranker on Blackwell (sm_120)** — TEI's candle backend hangs at warmup
  on sm_120 here (loads on CUDA, then stalls); running on **CPU** for now. The GPU image is
  built (`deploy/tei-blackwell`) and one `TEI_IMAGE` swap away once TEI ships stable Blackwell
  kernels. Also restore `bge-reranker-v2-m3` (multilingual) over MiniLM at that point.
- [ ] **Richer query understanding** — LLM-driven expansion/decomposition and intent-based
  freshness boosting (current pipeline normalizes + filters only).
- [ ] **Index reconciliation** — reconcile `documents.n_chunks` vs actual Qdrant/OpenSearch
  counts; prune stale chunks when a doc is re-chunked to fewer pieces.
- [ ] **max_pages best-effort overshoot** — tighten the concurrent cap if it matters.
- [ ] Non-HTML parsing (PDF/doc) and JS/social rendering are phase-tracked (Phase 3), not backlog.

---

## 2026-07-09 — Phase 3: social adapter framework + Mastodon adapter

**What**
- New `crawler/internal/social` package — the per-platform adapter framework (docs/08 §2):
  - `Adapter` interface (`Discover`/`Fetch`/`Parse`/`Paginate`/`Health`) and a `NormalizedDoc`
    that maps a social post/comment onto the crawler's Document model, carrying the social
    identity fields in `Meta()` (docs/08 §6: platform, post_id, author_handle, posted_at,
    engagement, parent_id, media_urls, permalink). `ContentHash` is keyed by `platform+post_id`
    (not text) so distinct posts with identical short text ("gm") stay distinct under the
    `documents` unique-content_hash constraint; `simhash` still handles near-dups.
  - `HealthTracker` — per-adapter fetch/error/item counters with an error-rate auto-disable
    threshold + min-sample guard (docs/08 §8, "fail loud"); feeds Prometheus.
  - **Mastodon adapter** (`mastodon.go`) — the credential-free first platform (open public API,
    no auth): `Discover` (instance host → public timeline, API URLs passed through), `Fetch`
    (HTTP + `Link: rel="next"` cursor extraction, 8 MiB cap), `Parse` (unwraps boosts to the
    original, strips post HTML to text w/ entity decode, declared-or-detected language, drops
    empty media-only posts), `Paginate` (max_id cursor).
- New Prometheus metrics: `crawler_social_fetch_total{adapter,result}`,
  `crawler_social_items_total{adapter}`.
- Chose Mastodon first (over Reddit/Telegram) because it needs **no credentials** — building the
  social path end-to-end without blocking on owner-provided API keys (Reddit OAuth, Telegram
  MTProto id/hash will be logged to `ralph/QUESTIONS.md` when those adapters land).

**Why**
- Start Phase 3 (JS + social ingestion) with the highest-ROI, zero-credential source so the
  normalization + health-monitoring scaffold is proven before the hostile platforms. Recall-first
  (CLAUDE.md north star): social is the highest-value source.

**Verification** (golang:1.25-alpine container; no local toolchain)
- `go vet ./...` clean; `go build ./...` clean; `go test ./...` green.
- New `internal/social` tests (5) all PASS: Mastodon parse contract (HTML→text + entity decode,
  boost unwrap/dedupe, reply `parent_id`, federated handle, engagement, permalink, meta shape),
  distinct content_hash per post, `Link` header `rel="next"` extraction, `Discover`, and
  HealthTracker auto-disable (min-sample + threshold).

**Status:** Phase 3 in progress — adapter framework + first (Mastodon) adapter parse/normalize
proven offline. Next: wire the social fetch queue into the scheduler (route adapters through the
frontier), then add Reddit/YouTube-transcript adapters (Reddit needs owner OAuth creds).

---

## 2026-07-09 — Phase 2: Search / RAG API

**What**
- **Clean-text gap resolved (the prerequisite):** the crawler now writes clean extracted text to
  MinIO at `text/{content_hash}.txt.gz` (idempotent, ungated by insert-dedup so re-crawls backfill
  known docs). Chosen over re-extraction in Python; the indexer reads it by content hash. NATS
  `doc.ready` streaming deferred — poll-based indexing covers backfill + new docs simply.
- **Intelligence plane (`ai/`, FastAPI):**
  - `indexer` — polls Postgres for un-indexed docs (`n_chunks=0`), pulls text from MinIO,
    structure-aware chunking (~400 tok, ~12% overlap), TEI embeddings, upserts to Qdrant
    (vectors) + OpenSearch (text) idempotently by `chunk_id`; `-1` sentinel for empty docs.
  - `indexes` — idempotent Qdrant collection (Cosine, payload indexes) + OpenSearch mapping
    (shingle sub-field) on startup.
  - `retrieval` — BM25 ∪ ANN with shared filters → RRF → cross-encoder rerank (TEI) →
    dedupe/diversify with recall-first backfill. BGE query instruction on the query side.
  - `synthesis` — grounded answer via local Ollama (`llama3.1:8b`), numbered `[n]` citations,
    citation verification, confidence/coverage; SSE streaming.
  - `main` — `/v1/search` (synthesis, streaming), `/v1/retrieve`, `/v1/coverage`, `/metrics`,
    `/readyz`; graceful degradation everywhere.
  - `eval/` — labeled set + runner: recall@k, MRR, nDCG; citation accuracy + LLM-as-judge
    groundedness (local model, no paid judge).
- **Backlog cleared:** FETCHING reaper (`claimed_at` col via migration `0002` + ticker) and
  `urlx`/`simhash` unit tests.
- **GPU/infra:** TEI image made swappable (`TEI_IMAGE`); built a Blackwell/sm_120 image
  (`deploy/tei-blackwell`, blackwell TEI binary + CUDA 12.9 math libs, compat driver removed).
  It loads on CUDA but the candle backend hangs at warmup on sm_120, so embeddings + reranker
  run on **CPU** for now (models pre-staged in the teicache volume, `HF_HUB_OFFLINE=1`). Reranker
  uses `ms-marco-MiniLM-L-6-v2` (candle-stable) instead of `bge-reranker-v2-m3` for now.

**Why**
- Deliver the query-time product: natural-language question → grounded, cited answer over
  crawled content, fully self-hosted (CLAUDE.md rule 2). Recall-first per the north star.

**Verification** (live stack, fresh `quotes-phase2` crawl: 33 docs)
- Indexing: 33/33 docs indexed → **52 chunks**, matching across Postgres, Qdrant (green, 52
  points), OpenSearch (52 docs).
- `/v1/retrieve`: hybrid hits with both lexical+vector ranks; rerank reorders (top result
  promoted). `/v1/search`: grounded answer with inline `[n]` citations resolving to real URLs;
  correctly refuses to hallucinate absent facts.
- **Eval (8 labeled queries):** recall@5 = recall@10 = **1.0**, nDCG@10 = 0.913, MRR = 0.9;
  citation accuracy = **1.0**, groundedness (LLM-judge) = 0.75, 8/8 answered.
- Degradation: Qdrant down → lexical-only (verified); `synthesize:false`/LLM down → retrieve-only
  (verified). SSE stream emits `source`→`token`→`citations`→`meta`→`done`.
- `go test` green for `urlx` + `simhash`.

**Status:** Phase 2 complete. Next: Phase 3 — JS + social ingestion. Follow-ups: GPU embeddings
on Blackwell once TEI kernels stabilize; richer query understanding; index reconciliation.

---

## 2026-07-09 — Phase 1: Crawler MVP

**What**
- Implemented the crawler ingestion loop in Go (`crawler/internal/...`):
  - `urlx`: URL canonicalization, hashing, scope helpers.
  - `simhash`: 64-bit near-duplicate fingerprint.
  - `store` (pgx): frontier (claim via `FOR UPDATE SKIP LOCKED`), documents (exact-dedup on
    content_hash), sources, campaigns; JSONB marshalled + cast to avoid pgx ambiguity.
  - `blob` (minio-go): gzip raw content to MinIO, keyed by content hash.
  - `fetch`: polite HTTP client (UA, timeout, body cap, redirect policy).
  - `extract`: go-readability main content + metadata, whatlanggo language detect, link
    discovery, content/simhash.
  - `crawl`: worker pool + per-host politeness limiter; fetch→extract→store→discover; retry
    with attempt cap; max_depth/max_pages scope.
  - `api`: control endpoints (`POST /internal/campaigns`, `GET /internal/frontier`,
    `/internal/coverage`, `/internal/documents/{id}`) + `/healthz` `/readyz` `/metrics`.
- Crawler Dockerfile builds via `go mod tidy` (Go 1.25; minio-go v7 needs ≥1.25).
- Added crawler tuning vars to `.env`/`.env.example`.

**Why**
- Deliver the breadth-first ingestion engine: seed a campaign and stream documents into storage.

**Verification** (live run against quotes.toscrape.com, max_pages 30)
- Campaign seeded; crawl ran end-to-end: 37 fetched (all 200), **28 unique documents**,
  **9 exact duplicates skipped**, 137 links discovered, max_pages enforced (101 skipped).
- Metadata extracted (title/author/lang), raw content gzipped in MinIO, `/metrics` populated,
  `/readyz` green (postgres+minio).

**Known limitations (follow-ups):**
- No reaper for URLs stuck in `FETCHING` if the process dies mid-fetch (add a timeout requeue).
- `max_pages` is best-effort (minor overshoot under concurrency).
- Non-HTML (PDF/doc) not yet parsed; JS/social rendering is Phase 3.

**Status:** Phase 1 complete. Next: Phase 2 — Search/RAG API (embeddings → indexes → retrieve
→ rerank → local-LLM synthesis).

---

## 2026-07-09 — Decision: no paid services; synthesis via local LLM

**What**
- New hard constraint (owner): **no paid or external SaaS dependencies** — fully self-hosted.
- Synthesis LLM changed from a hosted API (Claude) to a **local model via Ollama** on the RTX
  5070 (default `llama3.1:8b`, swappable; 14B possible by time-sharing VRAM).
- Removed `ANTHROPIC_API_KEY`; added `LLM_HOST/LLM_PORT/LLM_MODEL` to `.env`/`.env.example` and
  `ai/app/config.py`.
- Added an `llm` (Ollama) service to `deploy/docker-compose.yml` under the `gpu` profile
  (+ `ollamadata` volume).
- Discovery/ingestion narrowed to **free/open sources only** (Common Crawl URL indexes, sitemaps,
  RSS, free/open platform APIs, browser scraping); removed reliance on commercial search APIs,
  paid proxy providers, and paid social-data vendors.
- Swept docs: added rule 2 to `CLAUDE.md`; updated `00, 01, 02, 03, 04, 07, 08, 09, 10, 12, 13`
  and README; API `cost{usd}` → `usage{gpu_ms}`; GPU-fit plan for embeddings+reranker+LLM in 12 GB.

**Why**
- Owner requires the project to never depend on paid services; run entirely on owned hardware.

**Verification**
- `docker compose config` valid for base + `app` + `gpu` (tei + llm) profiles.
- ai-api rebuilt with new config; `/healthz` ok, `/readyz` ready (qdrant/opensearch ok).
- Grep sweep confirms no unintended paid-service references remain.

**Status:** Phase 0 intact. Local-LLM synthesis to be implemented in Phase 2.

---

## 2026-07-09 — Project bootstrap: docs + Phase 0 scaffold

**What**
- Wrote full documentation set (`docs/00`–`13`) and README index.
- Implemented **Phase 0 — Infrastructure scaffold**:
  - `deploy/docker-compose.yml`: Postgres, Redis, NATS (JetStream), Qdrant, OpenSearch, MinIO,
    Prometheus, Grafana (base); crawler + ai-api (`app` profile); TEI (`gpu` profile).
  - `db/migrations/0001_init.sql`: schema (sources, campaigns, documents, frontier_urls, jobs),
    auto-applied on Postgres init.
  - `crawler/`: Go skeleton (stdlib) with `/healthz`, `/readyz`, `/metrics`, TCP dep checks.
  - `ai/`: FastAPI skeleton with `/healthz`, `/readyz`, `/metrics`, HTTP dep checks.
  - Task runners `tasks.ps1` (Windows) and `Makefile`; `config/config.example.yaml`.
- Added project `CLAUDE.md` (core purpose = maximum recall; legal deferred to owner; always
  track progress in docs).
- Populated `.env` with strong generated secrets (local device only; gitignored).

**Why**
- Establish a runnable, measurable foundation before building the crawler (Phase 1), per the
  breadth-first roadmap.

**Verification**
- `docker compose config` valid.
- All 8 infra services healthy; DB shows 5 tables; OpenSearch `green`; Qdrant/NATS/MinIO OK.
- `crawler /readyz` → postgres/redis/nats ok. `ai-api /readyz` → qdrant/opensearch ok
  (tei optional, gpu profile off).
- Secrets rotated: volumes recreated so new `.env` credentials took effect (re-verified healthy).

**Status:** Phase 0 complete. Next: Phase 1 — Crawler MVP (`docs/12-ROADMAP.md`).
