# Project Progress Log

Reverse-chronological record of meaningful changes. Update this on every meaningful change
(see `CLAUDE.md` rule 3). Format: date · what · why · verification.

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
