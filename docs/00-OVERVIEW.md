# 00 — Overview & Scope

## 1. Vision

Build an AI search tool that finds information across the open internet — including
hard-to-reach and social content — and returns a **synthesized, cited answer** rather than a
list of blue links. The differentiator is **recall**: surfacing relevant material that
general-purpose engines drop, deprioritize, or never index.

## 2. Goals

1. **Breadth-first coverage.** Ingest as many relevant sources as physically possible on the
   available hardware, expanding continuously.
2. **Grounded answers.** Every factual claim in an answer links to a source URL + fetch
   timestamp. No uncited assertions.
3. **Freshness awareness.** Distinguish time-sensitive queries (news, prices, trends) from
   evergreen ones and retrieve accordingly.
4. **Fully self-hosted & free to run — no paid services.** Every component (crawler, indexes,
   embeddings, and the synthesis LLM) runs on the owner's own hardware — one workstation + home
   server + one RTX 5070 — with no hosted APIs, paid data feeds, or SaaS. Scale horizontally on
   owned hardware later without re-architecting.
5. **Measurable coverage.** Always be able to state *what* the system has indexed and *when*,
   so coverage is transparent rather than an unverifiable promise.

## 3. Non-goals (explicit, honest boundaries)

These are documented so expectations — including client expectations — stay realistic.

- **"Index the entire internet."** Not physically possible on any budget; the open web is
  hundreds of billions of pages and the deep web (auth-gated content, private databases) is
  larger still and uncrawlable. We maximize recall over a *defined, expanding* source universe.
- **"Never miss anything, provably."** Completeness over the whole web is mathematically
  unprovable (no denominator). We instead report coverage metrics and confidence.
- **Real-time totality of social platforms.** Platforms actively block automated access. We
  achieve best-effort coverage that will always have gaps and will break periodically as
  platforms change defenses.
- **Legal/compliance features (for now).** By owner decision, ToS/robots/GDPR/DMCA handling is
  deferred to a later, dedicated phase (see [11-SECURITY-LEGAL.md](11-SECURITY-LEGAL.md)). The
  architecture leaves hooks for it so it can be added without a rewrite.

## 4. Target users & use cases

- **Research / OSINT-style deep lookups** — find every mention of an entity across the web.
- **Monitoring** — track a topic/person/brand over time.
- **Question answering** — natural-language questions with sourced answers.
- **API consumers** — other apps that need "search + synthesize" as a service.

## 5. Success metrics

| Metric | Definition | Target (v1) |
|--------|------------|-------------|
| Recall@k | Fraction of known-relevant docs retrieved in top-k (measured on a labeled eval set) | ≥ 0.8 |
| Answer groundedness | % of answer sentences with a valid supporting citation | ≥ 0.95 |
| Citation accuracy | % of citations that actually support the claim (human-audited sample) | ≥ 0.9 |
| Freshness | Median age of indexed docs for "news" queries | < 24 h |
| Latency (P50) | Query → first token of streamed answer | < 3 s |
| Crawl throughput | Pages fetched + extracted per day on current hardware | ≥ 1M/day (static) |
| Cost per answered query | Search + LLM + infra amortized | tracked, minimized |

These are the numbers that let you *prove* quality to clients rather than assert it.

## 6. Guiding principles

1. **Store provenance always.** Source URL + fetch time + content hash on every record. This
   makes "improve quality later" a re-ranking/re-synthesis job over existing data, not a re-crawl.
2. **Right work on the right hardware, fully self-hosted.** Network-bound crawling on CPU/many
   workers; GPU for embeddings, re-ranking, **and a local synthesis LLM** — no paid APIs or SaaS.
   See [09-INFRASTRUCTURE.md](09-INFRASTRUCTURE.md).
3. **Idempotent, resumable pipelines.** Any stage can crash and resume without data loss.
4. **Decoupled via queues.** Crawler and AI layers never call each other synchronously.
5. **Everything measured.** No feature ships without metrics and an eval hook.

## 7. Glossary

- **Frontier** — the queue/priority structure of URLs waiting to be crawled.
- **Recall** — proportion of all relevant items that were retrieved.
- **RAG** — Retrieval-Augmented Generation; ground an LLM's answer in retrieved documents.
- **Hybrid search** — combining keyword (lexical) and vector (semantic) retrieval.
- **Re-ranker** — a model that reorders candidate documents by relevance to the query.
- **Chunk** — a passage-sized slice of a document used as the unit of embedding/retrieval.
- **Embedding** — a vector representation of text used for semantic similarity.
