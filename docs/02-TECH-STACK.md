# 02 — Tech Stack

Every choice below is justified against the project's constraints: **maximize recall**,
**breadth-first**, **run on one workstation + home server + RTX 5070 now**, **scale out later**,
**cost-efficient bootstrap**.

## 1. Languages

| Language | Used for | Rationale |
|----------|----------|-----------|
| **Go** | Crawler/fetch fleet, extractor, frontier service | Cheap goroutine concurrency → thousands of parallel network fetches on one box with low RAM; static binaries; excellent HTTP tooling. Crawling is I/O-bound, so this is where throughput lives. |
| **Python 3.11+** | AI/RAG service, embedding & re-rank workers, eval harness | Owns the ML ecosystem (PyTorch, sentence-transformers, vLLM, LangChain/LlamaIndex optional). Runs the GPU workloads. |
| **TypeScript/Node** | Playwright browser workers, later the web UI | Playwright's most mature bindings; natural for the eventual front-end. (Python Playwright is an option to reduce languages — see note.) |
| **SQL** | Schema, migrations | Postgres. |

> **One-language alternative:** an all-Python build (Scrapy + Playwright-Python + FastAPI) is
> viable and lowers cognitive load for a solo dev, at some throughput/efficiency cost. The
> polyglot split above is the performance-optimal choice the owner prioritized.

## 2. Crawling & fetching

| Tool | Purpose |
|------|---------|
| **Go `net/http` + `colly`** | Static/HTML crawling with concurrency, throttling, cookie jars |
| **`chromedp` or Playwright** | Headless-browser rendering for JS-heavy/social pages |
| **`goquery`** | HTML parsing / link extraction |
| **`robotstxt` (optional, deferred)** | Robots parsing — wired but disabled per owner decision |
| **Rod/Playwright stealth plugins** | Anti-bot fingerprint mitigation for social targets |
| **Proxy pool** (later) | Residential/datacenter proxy rotation for scale & blocking evasion |

## 3. Content extraction & NLP preprocessing

| Tool | Purpose |
|------|---------|
| **`go-readability` / trafilatura (Py)** | Main-content extraction, boilerplate removal |
| **`lingua` / fastText** | Language detection |
| **SimHash / MinHash (datasketch)** | Near-duplicate detection |
| **`unstructured` / `pdfplumber` / `tika`** | Non-HTML formats (PDF, docx, etc.) |
| **trafilatura / selectolax** | Fast HTML→text |

## 4. AI / ML

| Component | Choice | Notes |
|-----------|--------|-------|
| **Embedding model** | `BAAI/bge-large-en-v1.5` or `intfloat/e5-large-v2` (multilingual: `bge-m3`) | Runs locally on the RTX 5070 — free, high-volume. `bge-m3` gives multilingual + hybrid signals. |
| **Re-ranker** | `BAAI/bge-reranker-v2-m3` (cross-encoder) | GPU, applied to top-N candidates only. |
| **Synthesis LLM** | **Local instruct model** (8–14B, quantized) served by **Ollama** (bootstrap) or **vLLM** (scale), on the RTX 5070 | **No paid API — fully self-hosted.** Default an 8B-class model (fits comfortably alongside embeddings+reranker in 12 GB); a 14B quantized model is possible by time-sharing VRAM. Model is swappable via config. |
| **Inference runtime** | PyTorch + CUDA; **Ollama/vLLM** for the local LLM; **TEI** (text-embeddings-inference) for embeddings | Ollama manages quantized models + loading on a single GPU with minimal setup; vLLM for higher throughput later. TEI gives high-throughput batched embeddings. |

## 5. Storage & indexing

| Store | Role | Why |
|-------|------|-----|
| **PostgreSQL** | Frontier state, source registry, job/pipeline state, dedupe hashes, metadata | Reliable, transactional, great tooling. |
| **Redis** | Hot frontier set, per-host rate-limit counters, bloom filter for seen-URLs, caches | Speed. |
| **Qdrant** | Vector (semantic) index | Rust, fast, single-node friendly, payload filtering, scales to sharding. |
| **OpenSearch** | Keyword/BM25 index, filters, facets, highlighting | Free/open, mature lexical search; the recall complement to vectors. |
| **MinIO** | Object storage for raw/archived content (S3 API) | Local now, S3-compatible for cloud later. |

## 6. Messaging & orchestration

| Tool | Role |
|------|------|
| **NATS (JetStream)** | Primary event bus: "document ready", "index me", retries, DLQ. Lightweight, fast, single-binary. |
| **Redis Streams** | Alternative/lightweight queue for simple stages. |
| **(Later) Kafka** | Only if throughput outgrows NATS at fleet scale. |
| **Cron/scheduler** | Recrawl scheduling, freshness jobs (start with Go tickers / systemd timers; later Temporal or a workflow engine). |

## 7. API & services

| Tool | Role |
|------|------|
| **FastAPI (Python)** | Search/RAG API, streaming (SSE) answers, OpenAPI docs |
| **gRPC or REST (Go)** | Internal crawler/control APIs |
| **Pydantic** | Request/response schemas, validation |
| **Uvicorn/Gunicorn** | ASGI serving |

## 8. Observability

| Tool | Role |
|------|------|
| **Prometheus** | Metrics (crawl rate, queue depth, GPU util, latency) |
| **Grafana** | Dashboards + alerts |
| **Loki** or **OpenSearch** | Log aggregation |
| **OpenTelemetry** | Distributed tracing across planes |
| **Sentry** (optional) | Error tracking |

## 9. Dev & ops tooling

| Tool | Role |
|------|------|
| **Docker + docker-compose** | Local/bootstrap orchestration of all services |
| **NVIDIA Container Toolkit** | GPU passthrough into containers |
| **Makefile / Taskfile** | Common commands (`up`, `down`, `crawl`, `index`, `eval`) |
| **golangci-lint / ruff / black / mypy** | Linting & formatting |
| **pytest / go test** | Testing |
| **Alembic / golang-migrate** | DB migrations |
| **GitHub Actions** (later) | CI: lint, test, build images |
| **(Later) Kubernetes / Nomad** | Fleet orchestration when scaling beyond one host |

## 10. Version pinning policy

- Pin exact versions in `go.mod`, `requirements.txt`/`pyproject.toml`, and image tags.
- Model versions pinned by revision hash for reproducible embeddings (changing the embedding
  model requires a re-embed; treat it as a migration).

## 11. Rejected alternatives (and why)

- **Build a from-scratch inverted index** → reinventing OpenSearch; no.
- **Kafka from day one** → operational weight not justified at single-node bootstrap.
- **Embeddings via cloud API** → cost-prohibitive and a paid dependency; the GPU makes it free.
- **Hosted LLM API (Claude/OpenAI/etc.) for synthesis** → rejected: **no paid services** policy.
  Synthesis runs on a local model. Trade-off (lower ceiling than a frontier hosted model) accepted.
- **Commercial search / social-data / proxy providers** → rejected as paid dependencies; use the
  own crawler + free/open sources instead (see §12).
- **Pure vector search (no keyword)** → hurts recall on exact/rare terms; hybrid is mandatory.
- **Scrapy-only crawler** → fine and simpler, but lower throughput than Go for the fleet goal.
- **Elasticsearch (vs OpenSearch)** → licensing; OpenSearch is the open fork.

## 12. No-paid-services policy

Hard constraint (see `CLAUDE.md`): the system is **fully self-hosted and free to run**. All
components above are open-source and run on the owner's hardware. Discovery and ingestion use the
own crawler plus **free/open** sources only — sitemaps, RSS/Atom, Common Crawl URL indexes, free
open platform APIs (e.g. Mastodon/Fediverse, Reddit/Telegram public within free limits), and
browser-based scraping. No commercial search APIs, paid proxy pools, or paid data vendors.
