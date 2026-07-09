# 07 — RAG & Search Pipeline

The query-time pipeline that turns a natural-language question into a grounded, cited answer.

## 1. Pipeline overview

```
query
 └─▶ 1. Understand (normalize, detect intent, expand, decompose)
     └─▶ 2. Retrieve (hybrid: OpenSearch BM25 + Qdrant vectors, with filters)
         └─▶ 3. Fuse (reciprocal-rank fusion / weighted merge)
             └─▶ 4. Re-rank (cross-encoder on GPU, top-N)
                 └─▶ 5. Assemble context (dedupe, diversify, budget tokens)
                     └─▶ 6. Synthesize (LLM, grounded, cited, streamed)
                         └─▶ 7. Post-process (verify citations, confidence, format)
```

## 2. Query understanding

- Normalize: trim, spell-correct, language detect.
- **Intent classification:** factual / navigational / news-fresh / broad-research / entity-lookup.
- **Query expansion:** generate paraphrases and add synonyms/entities (boosts recall). For
  multilingual, optionally translate the query to index languages.
- **Decomposition:** break multi-part questions into sub-queries retrieved independently, then
  merge — critical for "find everything about X" style queries.
- **Filter derivation:** infer date ranges ("last week"), source types, language.

## 3. Retrieval (hybrid — the core recall lever)

- **Lexical (OpenSearch):** BM25 over `text`/`title` with phrase and shingle sub-fields; applies
  filters (date/source/lang). Catches exact terms, names, IDs, code — where embeddings fail.
- **Semantic (Qdrant):** embed the (expanded) query on GPU, ANN search with the same payload
  filters. Catches paraphrase/conceptual matches — where keywords fail.
- Retrieve top-K from each (e.g., K=100), union the candidate sets.
- Freshness handling: for news-intent, add a recency filter/boost.

## 4. Fusion

- **Reciprocal Rank Fusion (RRF):** `score = Σ 1/(k + rank_i)` across lexical & semantic lists;
  robust and parameter-light. Optionally weight by source authority.
- Produces a single ranked candidate list (e.g., top-50) for re-ranking.

## 5. Re-ranking

- Cross-encoder (`bge-reranker-v2-m3`) scores each (query, chunk) pair on the GPU.
- Reorder; keep top-N (e.g., 8–15 chunks) that fit the LLM context budget.
- Re-ranking is the biggest precision win and runs only on the fused shortlist, so it's cheap.

## 6. Context assembly

- Deduplicate near-identical chunks (same story from mirrors) using stored simhash/URL.
- **Diversify** sources so one domain doesn't dominate (max chunks per domain).
- Token budgeting: pack highest-scored chunks until the context window target is hit; keep each
  chunk's `[n]` id, URL, title, and date for citation.

## 7. Synthesis (grounded generation)

- Prompt structure:
  - System: "Answer only from the provided sources. Cite with [n]. If sources are insufficient,
    say so. Surface disagreements between sources."
  - Context: numbered chunks with metadata.
  - User: the (original) question.
- **KV-cache / persistent context** on the stable system/instructions to cut latency (Ollama and
  vLLM both keep the prompt prefix warm; vLLM supports prefix caching).
- **Streaming (SSE)** tokens to the client for low perceived latency.
- Model: **local instruct model on the RTX 5070** (Ollama in bootstrap, vLLM at scale) — no paid
  API. Model id is config-driven and swappable (default 8B-class; 14B by time-sharing VRAM).

## 8. Post-processing & verification

- **Citation verification:** check each `[n]` maps to a real provided source and that the cited
  chunk plausibly supports the sentence (lightweight entailment/keyword check; flag failures).
- **Confidence/coverage signal:** low if few/low-scored sources; the API returns this so the UI
  can warn rather than present false certainty.
- **Conflict flagging:** when sources disagree, the answer presents both with citations.
- Format: answer text + structured `sources[]` (url, title, date, snippet, score).

## 9. Response contract (summary)

```json
{
  "answer": "…text with inline [1][2] citations…",
  "citations": [
    { "n": 1, "url": "…", "title": "…", "published_at": "…", "snippet": "…", "score": 0.87 }
  ],
  "confidence": 0.72,
  "conflicts": [ { "claim": "…", "sources": [2, 5] } ],
  "coverage": { "candidates": 187, "used": 11, "domains": 6 },
  "latency_ms": 2410,
  "usage": { "llm_tokens_in": 4300, "llm_tokens_out": 380, "gpu_ms": 1900 }
}
```
Full schema in [13-API.md](13-API.md).

## 10. Evaluation harness

- **Labeled eval set:** queries with known-relevant URLs/answers.
- Metrics: recall@k, MRR/nDCG (retrieval); groundedness, citation accuracy, answer correctness
  (LLM-as-judge + human sample) for generation.
- Run on every meaningful change (model, chunker, fusion weights) to prevent regressions — this
  is how quality is *proven*, not asserted.

## 11. Caching

- Query → results cache (short TTL, respecting freshness intent).
- Embedding cache for repeated queries.
- LLM prefix/KV cache for the static system prompt (local server).
- Cache keys include filter/config versions to avoid stale hits.

## 12. Failure & degradation

- If Qdrant is down → lexical-only (degraded recall, still answers).
- If re-ranker is down → use fused order.
- If the local LLM is down/overloaded (GPU busy) → return ranked sources without synthesis
  ("retrieve" mode).
- Always prefer a partial, honest answer over an error or a hallucination.
