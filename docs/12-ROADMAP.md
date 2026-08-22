# 12 — Roadmap

Phased so each stage produces something runnable and measurable. Breadth-first, per owner
priority: get wide coverage working, then refine quality over already-collected data.

## Phase 0 — Infrastructure scaffold  ✅ done

**Goal:** one command brings up every backing service; skeleton crawler + AI service build & run.

Steps:
1. Repo structure (`crawler/` Go, `ai/` Python, `deploy/` compose, `config/`, `docs/`).
2. `docker-compose.yml`: Postgres, Redis, Qdrant, OpenSearch, MinIO, NATS, TEI (GPU),
   Prometheus, Grafana.
3. `.env.example`, `.gitignore`, `Makefile`/`Taskfile` (`up`, `down`, `ps`, `logs`).
4. Go crawler skeleton: config load, Postgres + Redis + NATS connectivity, `/healthz`.
5. Python AI service skeleton: FastAPI, connectivity to Qdrant/OpenSearch/TEI, `/healthz`.
6. DB migration tooling + initial schema ([06](06-DATA-MODEL.md)).
7. README quickstart; verify all services healthy.

**Exit criteria:** `make up` → all containers healthy; both skeletons pass health checks and can
reach their dependencies.

## Phase 1 — Crawler MVP  ✅ done

**Goal:** crawl the open (static) web end-to-end into stored, deduped documents.

- Frontier (Postgres + Redis, canonicalization, bloom dedupe, priority).
- Scheduler with per-host politeness + adaptive backoff.
- Go fetchers (HTTP, retries, UA rotation, blob store writes).
- Extractor (readability, metadata, language, exact + near dedupe).
- Link discovery + scope rules; campaign config.
- Crawl control API (submit seeds, frontier/job status).
- Metrics + dashboards for crawl health.

**Exit criteria:** seed a campaign → sustained fetch+extract with dedupe; documents + metadata
persisted; crawl dashboard live; target throughput demonstrated on a sample.

## Phase 2 — Search / RAG API  ✅ done

**Goal:** answer natural-language queries with cited answers over indexed content.

- [x] Crawler persists clean text (MinIO); poll-based chunker + TEI embeddings → Qdrant;
  text/metadata → OpenSearch (idempotent per `chunk_id`).
- [x] Hybrid retrieval (BM25 ∪ ANN) + RRF fusion + cross-encoder re-rank.
- [x] Filters (date/source/lang/domain); recall-first assembly (dedupe, diversify, backfill).
- [x] Local LLM synthesis (Ollama/llama3.1:8b on the RTX 5070) with numbered citations,
  streaming (SSE), citation verification, confidence + coverage. No paid API.
- [x] `/v1/search`, `/v1/retrieve`, `/v1/coverage`; response contract ([13](13-API.md)).
- [x] Eval harness (recall@k, MRR, nDCG; citation accuracy, LLM-as-judge groundedness).
- [x] Degradation paths (Qdrant → lexical-only, reranker → fused, LLM → retrieve-only).
- [~] Deferred/partial: richer query understanding — **LLM expansion/decomposition now done**
  in Phase 4 (`ai/app/understand.py`; intent classification + freshness boosting still pending);
  embeddings + reranker run on **CPU** pending stable Blackwell/sm_120 TEI GPU kernels
  (image is one env swap away); multilingual `bge-reranker-v2-m3` swapped for MiniLM until then.

**Exit criteria (met):** end-to-end query → cited answer; recall@5/10 = 1.0, nDCG@10 = 0.91,
citation accuracy = 1.0, groundedness = 0.75 on the sample set; degradation paths verified.

## Phase 3 — JS + Social ingestion  ✅ done

**Goal:** cover dynamic and social content.

- [x] Playwright browser-worker pool + static→browser escalation. **Done: escalation gate +
  durable render queue + render-ingest boundary + the browser-worker process.** Gate: `crawler/internal/render`
  (`never|auto|always` policy + JS-app heuristics: sparse extracted text combined with SPA root
  markers / framework bundles / noscript prompts; wired into the scheduler, stamped into
  `documents.meta.needs_render` + `render_reasons`, counted via `crawler_render_escalations_total`).
  Queue: a frontier-shaped `render_queue` table (`PENDING→RENDERING→RENDERED|FAILED`,
  claim/lease/retry + stuck-render reaper) exposed over the control API (`GET /internal/render/queue`,
  `POST /internal/render/claim`, `POST /internal/render/complete`). Ingest: `POST
  /internal/render/ingest` lands a worker's rendered DOM in the documents + text-blob pipeline via
  the same `extract → blob → InsertDocument` path as a static fetch (`meta.rendered_by=browser`),
  marking the job RENDERED. Worker: `browser-worker/` (Node + Playwright, `app` profile) runs a
  long-lived Chromium that claims jobs, renders with a real browser (fresh isolated context per job,
  rotated viewport/locale/UA, lazy-load auto-scroll), and POSTs the resolved DOM to the ingest
  endpoint; verified live rendering a real Wikipedia SPA into the index.
- [x] Anti-detection stack (fingerprints, proxies, sessions, pacing). **All four done.**
  Fingerprints + pacing + sessions: each render draws one internally-consistent identity (`browser-worker/src/fingerprint.ts`)
  — OS profile, region (locale+timezone+Accept-Language), Chrome version, hardware — and derives the
  UA, matching `Sec-CH-UA` client hints, and a stealth init-script (webdriver, `window.chrome`,
  `navigator.userAgentData`/high-entropy hints, plugins, WebGL vendor/renderer, `permissions.query`)
  from that single source, plus human-like pacing (post-load pause, mouse moves, jittered scroll).
  Verified in a real Chromium (no field contradicts another; outgoing headers carry the spoofed UA +
  client hints). **Session/cookie persistence done** (`browser-worker/src/sessions.ts`): a per-host
  store pins one coherent identity + its accumulated `storageState` (cookies + localStorage),
  persisted to the `sessionsdata` volume so warm/authenticated sessions survive restarts; return
  visits resume the same identity + jar, same-host renders are serialized (no jar race), and
  sessions rotate past a TTL. Verified end-to-end in a real Chromium (a cookie set on the first
  render is replayed on the return visit) and by unit tests. **Proxy pool done**
  (`browser-worker/src/proxies.ts`): egress is distributed across the owner's own self-run proxies
  (`RENDER_PROXIES`/`RENDER_PROXY_FILE` — no paid provider), each **pinned per host** so a warm
  session keeps a stable IP; failing proxies back off (capped exponential cooldown) and their hosts
  repin to a healthy one; least-loaded selection spreads assignments. Verified end-to-end in a real
  Chromium (a render's traffic provably transited the pooled proxy) and by unit tests.
- [~] Social adapters, easy/open first (Reddit, Mastodon, Telegram public, YouTube transcripts),
  then hostile platforms. **Mastodon + Hacker News + Lemmy adapters done** (three credential-free
  open APIs — the ≥3-adapter exit bar is met on the ingestion side); Reddit/Telegram need
  owner-provided creds (will surface in `ralph/QUESTIONS.md`).
- [x] Social normalization (threads, engagement, media urls); social fetch queue.
  `NormalizedDoc` + `Meta()` plus an **`Ingester`** that drives adapters end-to-end
  (Discover→Fetch→Paginate→Parse→persist) and lands posts in the same `documents` + text-blob
  pipeline as web pages, triggered via `POST /internal/social/ingest`.
- [x] Adapter health metrics + auto-disable + contract tests. `HealthTracker` + Prometheus
  metrics + contract tests + **per-adapter status endpoint** (`social.Registry` →
  `GET /internal/social/adapters[/{name}]`) all done.
- [x] **Freshness cadence for tracked entities.** A durable `tracked_entities` registry
  (`(adapter, seed)` + cadence + per-entity page cap, self-advancing `next_due_at`) and a
  `internal/freshness` scheduler that claims due entities every tick and re-runs the shared social
  ingester (dedupe makes re-runs idempotent). Managed via `GET·POST·DELETE
  /internal/social/tracked`; runs counted by `crawler_freshness_runs_total`. Incremental
  `since_id`/cursor fetch is a later optimization (dedupe already prevents re-indexing).

**Exit criteria (met):** JS pages render & index ✅; ≥3 social adapters ingesting with health
monitoring ✅; freshness cadence for tracked entities ✅; anti-detection stack complete ✅ —
coherent per-render identity + client hints + stealth patches + human-like pacing + per-host pinned
identity and persisted cookie jar + **per-host pinned self-run proxy pool with health-tracked
cooldown**, all verified in a real browser. **Phase 3 done.**

## Phase 4 — Scale & quality  🔨 in progress

**Goal:** harden, deepen, and refine over collected data.

- [x] Recrawl/freshness scheduling (`store.RequeueForRecrawl` + `main.runRecrawler`, gated by
  `CRAWLER_RECRAWL_AFTER_S`: re-enqueue `FETCHED` URLs older than the horizon) **+ conditional GET**
  (`fetch.GetConditional` sends ETag/If-Modified-Since; a 304 skips re-extraction/indexing and just
  refreshes the fetch time; validators stored per frontier URL). Verified live (304 on example.com
  recrawl). Deeper change detection (content diffing) can build on the stored validators.
- [x] **Sitemap-based seed discovery** (`crawler/internal/sitemap` + `POST /internal/sitemap/ingest`):
  parse `sitemap.xml` / sitemap-index (gzip-aware), follow one index level with URL/sitemap caps,
  and bulk-enqueue the listed URLs into a campaign's frontier — breadth beyond link-following
  (recall-first). Free/open source, no paid dependency.
- [x] **RSS/Atom feed seed discovery** (`crawler/internal/feeds` + `POST /internal/feeds/ingest`):
  parse RSS 2.0 / Atom / RSS 1.0-RDF (gzip-aware, Atom alternate-link preference) and enqueue item
  URLs into a campaign frontier — breadth + freshness, free/open. One-shot; recurring poll is a
  follow-up.
- [x] **Common Crawl URL-index discovery** (`crawler/internal/commoncrawl` +
  `POST /internal/commoncrawl/ingest`): resolve the latest crawls from `collinfo.json`, query the
  free CDX index per domain (`url=domain/*`), dedupe across crawls, and bulk-enqueue — the biggest
  cold-start breadth source, a free substitute for commercial search APIs (no paid dependency).
- [x] **Plain-text (`text/*`) extraction** (`fetch.IsText` + `extract.FromPlainText`, shared
  `Scheduler.index`): non-HTML text (plain, markdown, csv, logs) now indexes instead of being
  dropped — a free recall win (no new dependency). PDF/doc binary parsing still open.
- [~] Enrichment: **keyphrases/topic tags done end-to-end** — `extract.Keyphrases` (RAKE) →
  `documents.meta.keyphrases`, indexed into OpenSearch (`keyphrases` field) and **boosted in lexical
  retrieval** (`keyphrases^2`); verified live. NER and media OCR/ASR still to come; the `.raw`
  keyword sub-field sets up topic faceting.
- [x] **Query understanding — LLM expansion/decomposition** (`ai/app/understand.py`): the local
  LLM rewrites each query into paraphrases + decomposed sub-questions; every planned query is
  retrieved (hybrid) and the runs are RRF-fused, so a chunk agreed on by several phrasings accrues
  score from each. Off-path and degrades to the original query when the LLM is down (recall-first,
  never fails a search). Wired through `/v1/retrieve` + `/v1/search` (`expand` flag, `expansions`
  echoed).
- [x] **Query intent classification** (`ai/app/intent.py`): a rule-based classifier labels each
  query (news_fresh / broad_research / entity_lookup / navigational / factual). Today a news/recency
  intent upgrades `freshness="auto"` → `"fresh"` (an explicit user freshness always wins); the label
  is echoed on responses for observability and future hooks (filter derivation, per-intent tuning).
  Intent only nudges ranking — never filters or drops results (recall-first). **Query understanding
  (docs/07 §2) is now complete: normalize + expansion + decomposition + intent.**
- [x] **Freshness-aware ranking** (`ai/app/freshness.py`): the `freshness=auto|fresh|any` request
  option (previously accepted but inert) now blends a recency weight — halving every
  `FRESHNESS_HALF_LIFE_DAYS` — into the ordering after rerank. `auto` nudges, `fresh` pulls hard,
  `any` is pure relevance. Undated docs get a neutral weight (never buried for lacking a date) and
  freshness only *reorders* the shortlist, never drops candidates (recall-first).
- [x] **Sentence-aware chunking** (`ai/app/chunking.py`): over-target paragraphs are windowed with
  each break snapped to the nearest sentence boundary (then whitespace), so chunks no longer split
  mid-sentence/mid-word — better-formed chunks embed and match more accurately (docs/05 §7).
- [x] **Query-time near-duplicate dedup** (`ai/app/dedup.py`): assembly previously deduped only on
  an exact first-200-char match; it now collapses re-crawled/overlapping passages via word-shingle
  Jaccard (`DedupIndex`, threshold `dedup_jaccard_threshold`), so each of the `max_sources` slots
  carries distinct information. High threshold + short-snippet exact-only fallback keep it recall-safe
  (distinct brief facts never dropped); assembly still backfills to budget. Diversification tuning
  (per-domain cap) continues.
- [x] **Retrieval result cache** (`ai/app/cache.py`): short-TTL in-process TTL/LRU memoization of
  identical queries, bypassed for freshness-driven ranking so it never hides freshly-crawled content
  — cuts repeated embed/ANN/lexical/rerank work on the single-GPU box (docs/07 caching).
- [~] Coverage/stats API ✅ crawler side (`GET /internal/coverage` → totals, by content-type/lang,
  frontier-by-state, recrawl validators/eligibility via `store.CoverageStats`). Monitoring/alerts +
  richer intelligence-plane coverage still to come.
- Auth, API keys, rate limiting, usage metering (multi-tenant readiness).
- [x] **Index reconciliation** (`ai/app/reconcile.py` + `POST /internal/reconcile`): prune chunks
  orphaned in Qdrant/OpenSearch (index ≥ `n_chunks`) so re-indexing to fewer pieces can't leave
  stale results; idempotent, verified live.
- Backups, runbooks, capacity planning; quantize vectors for density.

**Exit criteria:** stable at target scale on current hardware; quality metrics improved vs P2;
operational runbooks + backups tested.

## Phase 5 — Web UI  🔨 in progress

**Goal:** client-facing product.

- [~] Search UI: query box, streamed answer, source cards, filters/facets, history. **Started:**
  `ai/app/static/index.html` served at `GET /` — a no-build single page that streams `/v1/search`
  (SSE), renders the cited answer with clickable `[n]` chips + source cards + confidence/coverage/
  degradation, with synthesize + freshness controls; verified in a real browser. Filters/facets +
  history still to come.
- Coverage/status views; monitoring dashboards for clients.
- Auth/onboarding; usage/billing views.

**Exit criteria:** a client can self-serve search with cited answers and see coverage.

## Phase 6 — Agentic entity discovery  📐 designed

**Goal:** targeted, multi-hop lookups for ultra-specific needles — *"find the social handle of a
person given a surname + school + a mutual friend"* — that a single breadth fan-out can't reach.
Full design in [15-DISCOVERY-AGENT.md](15-DISCOVERY-AGENT.md).

Approach: a **plan → act → observe → refine** loop with the **local LLM as the reasoner** and the
**existing crawler/discovery/social tools as the actor** — no new fetch code, no paid API, no new
framework (CLAUDE.md rule 2). Only the orchestration + entity-resolution intelligence are net-new.

- [ ] Orchestrator service driving the loop (local LLM emits schema-validated tool actions;
  degrades to a fixed query-generation heuristic when the LLM is down — recall-first).
- [~] Structured **target brief** model (`ai/app/entity_brief.py`) — done: tolerant `from_dict`
  parser (API- or LLM-supplied), attribute/relationship/budget model, and the loop's
  brief-enrichment merge (`with_attribute`/`with_handle`, first-write-wins). `POST
  /v1/discover/entity` + a "targeted lookup" UI form still to come.
- [~] **Query generation** from attributes (`ai/app/entity_queries.py`) — done: the deterministic,
  attribute-anchored backbone (name × discriminator/relationship dorks, platform-scoped `site:`
  variants, ranked most-specific-first, §5 guardrail — every query carries ≥1 discriminator, a bare
  common surname is never fanned out). This is also the LLM-down fallback; the LLM planner on top +
  wiring to `POST /internal/discover` + social search are next.
- [ ] **Entity-resolution scorer** (`candidate ↔ brief` attribute + relationship corroboration;
  reuses Phase 4 NER + the reranker) with brief-enrichment feedback.
- [ ] Per-run **lead frontier** (isolated, resumable) + explicit budget (`max_hops/fetches/wall_s`).
- [ ] Eval extension: labeled solvable targets → *resolution* precision/recall + "found @ hop-k".

**Depends on:** P1 fetch/render pipeline, P2 retrieval/rerank, P3 social adapters + anti-detection
(all done), P4 NER enrichment. **Credential-gated hops** wait on owner-supplied accounts
([14-CREDENTIALS.md](14-CREDENTIALS.md)); until then they degrade to open-web + metasearch evidence.

**Exit criteria:** given a solvable target brief, the loop returns ranked candidate(s) with a
confidence and a cited evidence trail, terminating within budget; resolution metrics tracked on the
labeled set.

## Later / scale-out (on owned hardware — no paid services)

- Distributed crawler fleet on the owner's own machines + self-run proxy pool; K8s/Nomad;
  clustered datastores.
- Synthesis LLM (and a larger model) on a dedicated GPU; vLLM for higher throughput.
- **Legal/compliance implementation** ([11](11-SECURITY-LEGAL.md)) — handled by the owner at the
  very end.

## Dependency graph

```
P0 ─▶ P1 ─▶ P2 ─▶ P4 ─▶ P5
             ▲       ▲     
        P3 ──┘───────┘──▶ P6   (P6 = agentic entity discovery; orchestrates
             (P3 needs P1 pipeline + P2 indexing;   P1 fetch + P2 retrieval +
              feeds P4/P5/P6)                        P3 social + P4 NER)
```
