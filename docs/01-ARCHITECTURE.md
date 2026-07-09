# 01 — Architecture

## 1. High-level view

The system is split into two decoupled halves joined by a message queue and shared datastores:

- **Ingestion plane** (Go + browser workers): discover → fetch → extract → dedupe → enqueue.
- **Intelligence plane** (Python + GPU): embed → index → retrieve → re-rank → synthesize.

```
                         ┌──────────────────────────────────────────────────────┐
                         │                      INGESTION PLANE                    │
                         │                                                         │
  seeds / sitemaps ──▶ ┌─┴────────┐   fetch   ┌───────────┐  raw html  ┌────────┐ │
  discovered links ──▶ │ Frontier │──────────▶│  Fetchers │───────────▶│ Blob   │ │
                       │ (Postgres│           │  (Go/HTTP)│            │ store  │ │
                       │  +Redis) │◀──────────│           │            │ (MinIO)│ │
                       └────┬─────┘  new links └─────┬─────┘            └───┬────┘ │
                            │                        │ JS/social            │      │
                            │                  ┌─────▼──────┐               │      │
                            │                  │ Browser    │               │      │
                            │                  │ workers    │               │      │
                            │                  │ (Playwright)│              │      │
                            │                  └─────┬──────┘               │      │
                            │                        │                      │      │
                         ┌──┴────────────────────────▼──────────────────────▼───┐  │
                         │              Extraction & Normalization              │  │
                         │  (readability, boilerplate strip, lang detect,       │  │
                         │   dedupe by SimHash/MinHash, metadata)               │  │
                         └───────────────────────────┬──────────────────────────┘ │
                         └─────────────────────────── │ ─────────────────────────┘
                                                       │  "document ready" events
                                        ┌──────────────▼───────────────┐
                                        │      Message Queue (NATS)     │
                                        └──────────────┬───────────────┘
                         ┌─────────────────────────────│──────────────────────────┐
                         │                    INTELLIGENCE PLANE                   │
                         │                             │                           │
                         │                    ┌────────▼────────┐                  │
                         │                    │  Chunk + Embed  │  (GPU: RTX 5070) │
                         │                    │  (Python)       │                  │
                         │                    └───┬─────────┬───┘                  │
                         │            vectors     │         │  text + metadata     │
                         │              ┌─────────▼──┐   ┌──▼────────────┐         │
                         │              │  Qdrant    │   │  OpenSearch   │         │
                         │              │ (vector DB)│   │ (keyword/BM25)│         │
                         │              └─────┬──────┘   └──────┬────────┘         │
                         │                    │  hybrid retrieve │                 │
                         │              ┌─────▼──────────────────▼─────┐           │
                         │  query ─────▶│      Search / RAG API        │           │
                         │  (client)    │  (FastAPI):                  │           │
                         │              │  understand→retrieve→rerank  │──▶ answer │
                         │              │  →synthesize (local LLM)→cite│  +sources │
                         │              └──────────────────────────────┘           │
                         └─────────────────────────────────────────────────────────┘
```

## 2. Components

### Ingestion plane
| Component | Responsibility | Tech |
|-----------|----------------|------|
| **Frontier** | URL queue, priority, dedupe of URLs, politeness scheduling | Postgres (durable) + Redis (hot set, bloom filter) |
| **Fetchers** | High-concurrency HTTP fetch of static content | Go (net/http, Colly) |
| **Browser workers** | Render JS-heavy & social pages, handle sessions/anti-bot | Playwright (Node or Python) |
| **Blob store** | Temporary raw HTML/JSON before extraction | MinIO (S3 API) or filesystem |
| **Extractor** | Boilerplate removal, main-content extraction, metadata, language, dedupe | Go workers + libraries |
| **Link discovery** | Parse out new URLs, feed back into frontier | Go |

### Intelligence plane
| Component | Responsibility | Tech |
|-----------|----------------|------|
| **Chunker/Embedder** | Split docs into passages, compute embeddings on GPU | Python, sentence-transformers, PyTorch/CUDA |
| **Vector DB** | Approximate nearest-neighbor semantic search | Qdrant |
| **Keyword index** | Lexical/BM25 search, filters, facets | OpenSearch |
| **Re-ranker** | Cross-encoder scoring of candidates vs query | Python, GPU |
| **Search/RAG API** | Orchestrates the query pipeline, streams answers | Python, FastAPI |
| **LLM synthesis** | Compose grounded, cited answer | Local LLM (Ollama/vLLM on the RTX 5070) — no paid API |

### Shared / cross-cutting
| Component | Responsibility | Tech |
|-----------|----------------|------|
| **Message queue** | Decouple ingestion from indexing; buffer bursts | NATS (JetStream) or Redis Streams |
| **Metadata DB** | Crawl state, jobs, source registry, dedupe hashes | PostgreSQL |
| **Object store** | Raw + archived content | MinIO |
| **Observability** | Metrics, logs, traces, dashboards | Prometheus + Grafana + Loki |
| **Config/secrets** | Central config, API keys, proxy creds | .env + (later) Vault |

## 3. Data flow (end to end)

1. **Seed** the frontier with starting URLs, sitemaps, and (later) search-API-derived URLs.
2. **Fetchers** pull URLs respecting per-host politeness; static pages go straight to the blob
   store, JS/social pages are routed to **browser workers**.
3. **Extractor** turns raw content into a normalized `Document` (clean text, title, metadata,
   discovered links, content hash), deduplicates, and stores it.
4. A **"document ready"** event is published to the queue.
5. **Embedder** consumes the event, chunks the document, computes embeddings on the GPU, and
   writes vectors to **Qdrant** and text+metadata to **OpenSearch**.
6. A **query** hits the **Search API**: it is understood/expanded, retrieved from both stores
   (hybrid), re-ranked on the GPU, and passed to the **LLM** which synthesizes a cited answer,
   streamed back to the client.

## 4. Why this shape

- **Decoupling via queue** lets the crawler run flat-out without waiting on GPU indexing, and
  lets you restart either plane independently.
- **Two datastores (vector + keyword)** is the single biggest recall lever — lexical catches
  exact/rare terms (names, IDs, code) that embeddings miss; vectors catch paraphrase/semantics
  that keywords miss. Fusing both maximizes recall, which is the product's whole point.
- **Provenance-first records** make quality iterative: re-embed or re-rank over stored text
  without re-crawling.
- **Single-node now, fleet later**: every component is horizontally scalable (add fetchers,
  add embedder workers, shard Qdrant/OpenSearch) without design changes.

## 5. Deployment topology

- **Dev / bootstrap (now):** everything via `docker-compose` on the home server; GPU-bound
  services (embedder, re-ranker) pinned to the machine with the RTX 5070.
- **Scale-out (funded):** crawler fleet on cheap CPU nodes / VPSes with proxies; datastores on
  dedicated nodes; GPU node(s) for embedding/inference; queue and Postgres clustered.

See [09-INFRASTRUCTURE.md](09-INFRASTRUCTURE.md) for concrete resource allocation.
