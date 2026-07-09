# ai-search

An AI-powered search engine that crawls, indexes, and reasons over the open web —
built to maximize **recall** ("find the little pieces others miss") while grounding
every answer in cited sources.

This repository currently contains the **full project documentation** and (from Phase 0
onward) the implementation. Read the docs before touching code.

## What this is

A self-hostable, hybrid web-search + retrieval-augmented-generation (RAG) system:

1. A **distributed crawler** (Go + headless browser workers) fetches open-web and
   JavaScript-heavy/social content.
2. An **extraction & indexing pipeline** cleans, deduplicates, embeds, and indexes content
   into a hybrid store (keyword + vector).
3. An **AI search API** (Python) answers natural-language queries by retrieving, re-ranking,
   and synthesizing an answer **with citations**.

The design target is **maximum measurable breadth** first, quality refinements second, running
initially on a single workstation + home server with one NVIDIA RTX 5070, and scaling out to a
fleet when funded.

## Documentation index

| Doc | What it covers |
|-----|----------------|
| [00 – Overview & Scope](docs/00-OVERVIEW.md) | Vision, goals, honest non-goals, success metrics |
| [01 – Architecture](docs/01-ARCHITECTURE.md) | System diagram, components, data flow |
| [02 – Tech Stack](docs/02-TECH-STACK.md) | Every technology chosen and why |
| [03 – Features](docs/03-FEATURES.md) | Complete feature catalogue |
| [04 – Crawler](docs/04-CRAWLER.md) | Fetch fleet, frontier, politeness, anti-blocking |
| [05 – Extraction & Processing](docs/05-EXTRACTION.md) | Parsing, cleaning, dedup, chunking, embeddings |
| [06 – Data Model & Storage](docs/06-DATA-MODEL.md) | Schemas, indexes, storage budgeting |
| [07 – RAG & Search Pipeline](docs/07-RAG-SEARCH.md) | Query → retrieve → rerank → synthesize → cite |
| [08 – Social Media Ingestion](docs/08-SOCIAL-MEDIA.md) | Approach, anti-detection, per-platform notes |
| [09 – Infrastructure & Deployment](docs/09-INFRASTRUCTURE.md) | Docker, hardware allocation, GPU usage |
| [10 – Operations & Observability](docs/10-OPERATIONS.md) | Monitoring, scaling, backups, runbooks |
| [11 – Security, Legal & Compliance](docs/11-SECURITY-LEGAL.md) | Risk register (deferred by owner decision) |
| [12 – Roadmap](docs/12-ROADMAP.md) | Phase-by-phase build plan with concrete steps |
| [13 – API Reference](docs/13-API.md) | External + internal API contracts |

## Quickstart (Phase 0)

Requires Docker + Docker Compose. GPU (TEI) is optional and needs the NVIDIA
Container Toolkit.

**Windows (PowerShell):**
```powershell
.\tasks.ps1 init-env     # create .env from .env.example (review secrets)
.\tasks.ps1 up           # start all infra services
.\tasks.ps1 health       # check service health
.\tasks.ps1 up-app       # (optional) build+run crawler & ai-api skeletons
.\tasks.ps1 up-gpu       # (optional) start TEI embeddings + local LLM (needs GPU)
.\tasks.ps1 down         # stop everything
```

**Linux/macOS (make):** `make up`, `make health`, `make up-app`, `make down`.

Service endpoints once up: OpenSearch `:9200`, Qdrant `:6333`, MinIO console
`:9001`, NATS monitor `:8222`, Prometheus `:9090`, Grafana `:3000`. With the
`app` profile: crawler health `:8090/healthz`, AI API `:8000/healthz`.

## Status

- [x] Documentation
- [x] **Phase 0 — Infrastructure scaffold** (services, DB schema, crawler + AI skeletons)
- [ ] Phase 1 — Crawler MVP
- [ ] Phase 2 — Search/RAG API
- [ ] Phase 3 — JS + social fetching
- [ ] Phase 4 — Scale & quality
- [ ] Phase 5 — Web UI

See [docs/12-ROADMAP.md](docs/12-ROADMAP.md) for details.
