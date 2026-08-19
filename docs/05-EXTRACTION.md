# 05 — Extraction & Processing

Turns raw fetched bytes into clean, deduplicated, indexable `Document` + `Chunk` records.

## 1. Pipeline stages

```
raw bytes ─▶ decode ─▶ format router ─▶ main-content extract ─▶ normalize
        ─▶ metadata extract ─▶ language detect ─▶ dedupe ─▶ Document
        ─▶ (event) ─▶ chunk ─▶ embed ─▶ index
```

## 2. Decoding & format routing

- Detect charset (headers → meta → chardet fallback), decode to UTF-8.
- Route by MIME/content sniff:
  - `text/html` → HTML extractor
  - `application/pdf` → PDF extractor (pdfplumber/Tika)
  - `application/*word*/*` → doc extractors
  - `application/json`, feeds → structured handlers
  - images/video → media handler (URL capture; OCR/ASR later)

## 3. Main-content extraction

- Use readability-style extraction (go-readability / trafilatura) to isolate the primary
  article/body and drop nav, ads, footers, cookie banners, related-links.
- Keep a structure-aware representation (headings, paragraphs, lists) to enable better chunking.
- Preserve links within content for discovery and citation anchoring.
- Fallback chain: readability → density heuristics → raw text, so extraction never fully fails.

## 4. Metadata extraction

Captured per document where available:
- `title`, `author`, `published_at`, `modified_at`
- `canonical_url`, `lang`, `site_name`
- OpenGraph / Twitter Card / schema.org / JSON-LD fields
- HTTP metadata: final URL, status, fetch timestamp, content-type, content hash
- Source classification: news / blog / forum / social / doc / gov / academic / other

## 5. Language detection

- Detect primary language (lingua/fastText); store per document and per chunk.
- Drives multilingual embedding model selection and per-language analyzers in OpenSearch.

## 6. Deduplication

Two levels:
1. **Exact:** SHA-256 of normalized content → skip identical re-fetches.
2. **Near-duplicate:** SimHash (or MinHash + LSH) fingerprints to catch mirrors, syndicated
   reposts, and boilerplate-heavy variants. Configurable similarity threshold.
- Dedup index stored in Postgres (hash → canonical doc id). Near-dupes are linked to a
  representative doc rather than fully re-indexed, preserving provenance of each URL.

## 7. Chunking

The unit of retrieval. Good chunking is a major recall/precision lever.

- **Strategy:** structure-aware, ~256–512 tokens per chunk with ~10–15% overlap; never split
  mid-sentence; respect heading boundaries.
- Attach chunk metadata: parent doc id, position, heading path, char offsets (for citation
  highlighting), language.
- Store chunk text in OpenSearch and its vector in Qdrant, sharing a `chunk_id`.
- **Implementation (`ai/app/chunking.py`):** packs paragraphs to `chunk_target_chars` with
  `chunk_overlap_chars` overlap. A paragraph longer than the target is windowed with each window
  end **snapped to the nearest sentence boundary** (then whitespace) within the back half of the
  window, so a chunk ends mid-sentence only for a run with no break at all (e.g. a long URL/token),
  and never mid-word otherwise. Tiny trailing crumbs (< `chunk_min_chars`) merge into the previous
  chunk. Char offsets index the original text for traceback.

## 8. Embeddings

- Model: `bge-large` (en) / `bge-m3` (multilingual), pinned by revision.
- Batched on the GPU via TEI (text-embeddings-inference) or sentence-transformers.
- Normalize vectors (cosine similarity). Store dimensionality + model id in payload so a model
  change is a detectable migration.
- Throughput target: saturate the RTX 5070 with batched inference; backpressure crawler if the
  embed queue grows beyond a threshold.

## 9. Enrichment (later, P4)

- Named-entity recognition (people/orgs/places) for filtering, monitoring, and entity search.
- Keyphrase extraction.
- Topic/classification tags.
- Media OCR/ASR to make images/video searchable.

## 10. Idempotency & resumability

- Each stage keyed by content hash + stage version; re-processing is safe and cheap.
- Bumping a stage version (e.g., new chunker) triggers targeted reprocessing without re-crawl —
  this is how "improve quality later" works over already-collected data.

## 11. Quality controls

- Minimum content-length threshold to skip empty/thin pages (configurable; keep low for recall).
- Spam/SEO-farm heuristics (later) to down-weight rather than drop (preserve recall, adjust rank).
- Extraction-failure metrics; sample audits of extracted vs raw.
