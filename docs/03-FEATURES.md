# 03 — Feature Catalogue

Features are tagged by target phase (see [12-ROADMAP.md](12-ROADMAP.md)):
`P1` crawler MVP · `P2` search/RAG · `P3` JS/social · `P4` scale/quality · `P5` UI ·
`P6` agentic entity discovery.

## 1. Crawling & ingestion

- **Seed management** `P1` — add/remove seed URLs, sitemaps, URL lists; per-seed config.
- **Frontier with prioritization** `P1` — score URLs by depth, source authority, freshness need,
  and estimated value; high-priority URLs fetched first.
- **Per-host politeness & rate limiting** `P1` — configurable concurrency and delay per host;
  adaptive back-off on errors/429s.
- **Static HTML fetching at high concurrency** `P1`.
- **JavaScript rendering** `P3` — headless-browser fetch for SPA/dynamic pages.
- **Social-media ingestion** `P3` — per-platform adapters (see [08](08-SOCIAL-MEDIA.md)).
- **Non-HTML formats** `P2` — PDF, DOCX, plaintext, RSS/Atom, JSON/APIs.
- **Link discovery & recursion** `P1` — extract and enqueue outlinks with scope rules.
- **Agentic entity discovery** `P6` — targeted multi-hop lookups from a structured *target brief*
  (known attributes → the missing one): a local-LLM planner sequences the existing metasearch/crawl/
  social tools in a plan→act→observe loop, with entity resolution scoring which candidate is the
  target and a cited evidence trail. See [15-DISCOVERY-AGENT.md](15-DISCOVERY-AGENT.md).
- **Scope control** `P1` — allow/deny by domain, path, regex, depth, MIME.
- **Recrawl / freshness scheduling** `P4` — re-fetch cadence per source based on change rate.
- **Incremental crawl** `P4` — conditional GET (ETag/Last-Modified), change detection.
- **Anti-blocking toolkit** `P3` — user-agent rotation, header realism, proxy rotation,
  browser fingerprint mitigation, session/cookie reuse, CAPTCHA handling hooks.
- **Politeness/robots hooks** `P4` (deferred by owner) — wired but off; toggleable.

## 2. Extraction & processing

- **Main-content extraction** `P1` — strip nav/ads/boilerplate, keep article text.
- **Metadata extraction** `P1` — title, author, publish date, canonical URL, OpenGraph,
  schema.org, language.
- **Language detection** `P2`.
- **Near-duplicate detection** `P2` — SimHash/MinHash to avoid indexing mirrors/reposts.
- **Content-hash change detection** `P4`.
- **Chunking** `P2` — semantic/passage chunking with overlap, structure-aware.
- **Entity/keyword extraction** `P4` — NER for filtering and monitoring.
- **Media handling** `P4` — image/video URL capture; OCR/transcription (later).

## 3. Indexing & retrieval

- **Embedding generation (GPU)** `P2`.
- **Vector index (Qdrant)** `P2` — ANN semantic search with payload filters.
- **Keyword index (OpenSearch)** `P2` — BM25, phrase, fielded search, highlighting.
- **Hybrid retrieval + fusion** `P2` — reciprocal-rank fusion / weighted merge of lexical+vector.
- **Cross-encoder re-ranking (GPU)** `P2`.
- **Filters & facets** `P2` — by domain, date range, language, source type, entity.
- **Freshness-aware retrieval** `P2` — boost recent docs for time-sensitive queries.
- **Deduplication at query time** `P2` — collapse near-identical results.

## 4. AI answering

- **Query understanding** `P2` — intent detection, spelling, expansion, multi-part decomposition.
- **Grounded synthesis with citations** `P2` — answer strictly from retrieved passages; inline
  numbered citations mapped to source URLs.
- **Conflicting-source handling** `P2` — surface disagreement instead of silently picking one.
- **Confidence / coverage signaling** `P2` — flag low-evidence answers; "no reliable sources"
  path instead of hallucinating.
- **Follow-up / conversational context** `P4`.
- **Answer streaming (SSE)** `P2`.
- **Multi-language answering** `P4`.

## 5. API & product

- **Search API** `P2` — `POST /search` (query → cited answer + sources).
- **Raw retrieval API** `P2` — `POST /retrieve` (documents only, no synthesis).
- **Crawl control API** `P1` — submit seeds, inspect frontier, job status.
- **Coverage/stats API** `P4` — what's indexed, counts by domain/date, last crawl times.
- **Monitoring/alerts (saved queries)** `P4` — track a topic; notify on new matches.
- **Auth, API keys, rate limiting, usage metering** `P4` — multi-tenant readiness.
- **Web UI** `P5` — search box, streamed answer, source cards, filters, history.

## 6. Operations

- **Metrics & dashboards** `P1+` — crawl rate, queue depth, index size, GPU util, latency.
- **Structured logging & tracing** `P1+`.
- **Backups & snapshots** `P4` — Postgres, Qdrant, OpenSearch.
- **Eval harness** `P2` — labeled query set; recall@k, groundedness, citation accuracy.
- **Resource tracking** `P2` — per-query LLM tokens + GPU time (no monetary cost; fully self-hosted).
- **Runbooks** `P4` — restart, reindex, recover, scale.

## 7. Feature → doc map

| Feature area | Detailed in |
|--------------|-------------|
| Crawling | [04-CRAWLER.md](04-CRAWLER.md) |
| Extraction/processing | [05-EXTRACTION.md](05-EXTRACTION.md) |
| Storage/indexing | [06-DATA-MODEL.md](06-DATA-MODEL.md) |
| Retrieval/answering | [07-RAG-SEARCH.md](07-RAG-SEARCH.md) |
| Social | [08-SOCIAL-MEDIA.md](08-SOCIAL-MEDIA.md) |
| APIs | [13-API.md](13-API.md) |
