# Phase 2 Kickoff Plan — Search / RAG API

Concrete build order for Phase 2 so a fresh session can start immediately. Design reference:
[07-RAG-SEARCH.md](07-RAG-SEARCH.md), [06-DATA-MODEL.md](06-DATA-MODEL.md),
[02-TECH-STACK.md](02-TECH-STACK.md). All local, no paid services.

## 0. Prerequisites

- Infra up: `.\tasks.ps1 up` (+ `.\tasks.ps1 up-gpu` for TEI embeddings and the Ollama LLM).
- Models cached: `bge-large-en-v1.5` (TEI, auto on first start) and `llama3.1:8b` (Ollama, pulled).
- Test data: seed a crawl (Phase 1) so there are documents to index.

## 1. ⚠️ Integration gap to solve FIRST — clean text availability

Phase 1 stores **raw HTML (gzip) in MinIO** and **metadata in Postgres**, but the *clean extracted
text is not persisted anywhere queryable*. Phase 2 needs that text to chunk/embed. Pick one:

- **(Recommended) Emit from the crawler:** on document insert, publish a NATS `doc.ready` event
  `{document_id, content_hash, blob_key, lang}` and also write the clean text to MinIO
  (`text/{hash}.txt.gz`). Matches [01-ARCHITECTURE.md](01-ARCHITECTURE.md). Small crawler change.
- **(Alt) Re-extract in Python:** the indexer reads raw from MinIO and re-extracts text with
  trafilatura. No crawler change, some duplicated logic.

Decide this before building the indexer.

## 2. Indexer (Python worker in `ai/`)

- Consume `doc.ready` (or poll Postgres for un-indexed docs).
- **Chunk** clean text: structure-aware, ~256–512 tokens, ~10–15% overlap (see
  [05-EXTRACTION.md](05-EXTRACTION.md) §7). Attach `chunk_id`, `document_id`, offsets, heading path.
- **Embed** each chunk via TEI (`POST http://tei:8080/embed`), batched.
- **Write vectors** to Qdrant collection `chunks` (create on startup: size = embed dim, Cosine,
  payload = document_id/url/source_type/lang/published_at/chunk_id).
- **Write text+metadata** to OpenSearch index `chunks` (mapping in [06-DATA-MODEL.md](06-DATA-MODEL.md) §3).
- Update `documents.n_chunks`. Idempotent upserts keyed by `chunk_id`.

## 3. Retrieval + RAG API (FastAPI in `ai/`)

Endpoints (contracts in [13-API.md](13-API.md)):
- `POST /v1/retrieve` — hybrid retrieve only.
- `POST /v1/search` — full pipeline with synthesis (SSE streaming).

Pipeline ([07-RAG-SEARCH.md](07-RAG-SEARCH.md)):
1. **Understand** — normalize, intent, expansion, optional decomposition, derive filters.
2. **Retrieve** — BM25 (OpenSearch, top-K) ∪ vector (Qdrant, top-K) with shared filters.
3. **Fuse** — Reciprocal Rank Fusion.
4. **Re-rank** — cross-encoder `bge-reranker-v2-m3` on the shortlist (GPU).
5. **Assemble** — dedupe (simhash/url), diversify per-domain, token-budget the context.
6. **Synthesize** — local LLM via Ollama (`POST http://llm:11434/api/chat`), grounded prompt with
   numbered citations, "insufficient sources" path, conflict surfacing. Stream tokens.
7. **Post-process** — verify citations map to sources; confidence + coverage; return `usage`.

## 4. Eval harness (`ai/eval/`)

- Labeled set: `queries.jsonl` = `{query, relevant_urls[], notes}`.
- Metrics: recall@k, MRR/nDCG (retrieval); groundedness, citation accuracy (generation,
  LLM-as-judge + spot human check). Nightly run; gate model/chunker/prompt changes.

## 5. Verification (do end-to-end, commit per working sub-component)

1. Index a crawled campaign → Qdrant + OpenSearch populated (check counts).
2. `/v1/retrieve` returns sensible ranked chunks with scores.
3. `/v1/search` returns a cited answer; citations resolve to real source URLs.
4. Degradation paths: Qdrant down → lexical-only; LLM busy → retrieve-only.

## 6. Also pick up (backlog — see PROGRESS.md)

- [x] FETCHING reaper in the crawler (timeout requeue; `claimed_at` column + ticker).
- [x] Unit tests for `urlx` and `simhash` (via `go test` in a Go container).

## Commit discipline

Commit after each working sub-component (indexer, retrieve, search, eval). No AI attribution
(CLAUDE.md rule 5). Update PROGRESS.md as you go.
