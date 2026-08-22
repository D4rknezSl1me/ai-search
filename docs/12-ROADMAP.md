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
  frontier-by-state, recrawl validators/eligibility via `store.CoverageStats`) + intelligence-plane
  `GET /v1/coverage` (resilient) with a UI panel. **Monitoring/alerts ✅** — `deploy/alerts.yml`
  (11 Prometheus rules across service/crawl/pipeline/discovery health, wired via `rule_files` +
  mounted). Alertmanager routing/paging still to come.
- Auth, API keys, rate limiting, usage metering (multi-tenant readiness).
- [x] **Index reconciliation** (`ai/app/reconcile.py` + `POST /internal/reconcile`): prune chunks
  orphaned in Qdrant/OpenSearch (index ≥ `n_chunks`) so re-indexing to fewer pieces can't leave
  stale results; idempotent, verified live.
- Backups, runbooks, capacity planning; quantize vectors for density.

**Exit criteria:** stable at target scale on current hardware; quality metrics improved vs P2;
operational runbooks + backups tested.

## Phase 5 — Web UI  🔨 in progress

**Goal:** client-facing product.

- [x] Search UI: query box, streamed answer, source cards, filters, history.
  `ai/app/static/index.html` served at `GET /` — a no-build single page that streams `/v1/search`
  (SSE), renders the cited answer with clickable `[n]` chips + source cards + confidence/coverage/
  degradation, synthesize + freshness controls, a **Filters** panel (date range / domains /
  languages → `/v1/search` `filters`), and **recent-search history** (localStorage chips, click to
  re-run, persists across reloads). Plus the Phase-6 "Find a person" mode tab. All verified in a real
  browser (history persists, chip re-runs, console clean). Server-driven facet *counts* are a later
  add.
- [~] Coverage/status views — **client coverage panel done**: a footer "coverage" toggle loads
  `GET /v1/coverage` (now degrades to `available:false` instead of 500 when Postgres is down) and
  renders documents/indexed/chunks + top domains, or a graceful "datastores unavailable" note.
  Verified in a real browser. Grafana-style monitoring dashboards for clients still to come.
- Auth/onboarding; usage/billing views.

**Exit criteria:** a client can self-serve search with cited answers and see coverage.

## Phase 6 — Agentic entity discovery  🔨 feature-complete (hardening left)

**Goal:** targeted, multi-hop lookups for ultra-specific needles — *"find the social handle of a
person given a surname + school + a mutual friend"* — that a single breadth fan-out can't reach.
Full design in [15-DISCOVERY-AGENT.md](15-DISCOVERY-AGENT.md).

Approach: a **plan → act → observe → refine** loop with the **local LLM as the reasoner** and the
**existing crawler/discovery/social tools as the actor** — no new fetch code, no paid API, no new
framework (CLAUDE.md rule 2). Only the orchestration + entity-resolution intelligence are net-new.

- [x] Orchestrator loop core (`ai/app/entity_orchestrator.py`) — the pure plan→act→observe→refine
  control flow composing brief+queries+resolve, with the PLAN + ACT steps dependency-injected;
  bounded by the brief's budget (hops/fetches/wall-clock), enriches the brief from above-threshold
  matches, dedupes+ranks candidates, and stops on {confident match, budget, no new leads}. **Wired
  end-to-end** via `POST /v1/discover/entity`.
- [x] Structured **target brief** model (`ai/app/entity_brief.py`) — tolerant `from_dict` parser
  (API- or LLM-supplied), attribute/relationship/budget model, and the loop's brief-enrichment merge
  (`with_attribute`/`with_handle`, first-write-wins).
- [x] **`POST /v1/discover/entity`** endpoint (`ai/app/main.py` + schemas) — brief in →
  `DiscoveryResult` out (ranked candidates + per-signal evidence + stats); rejects a brief with no
  name/handle.
- [x] **"Find a person" UI** (`ai/app/static/index.html`) — a mode tab on the self-hosted UI: brief
  inputs (surname/given/goal/city/school/employer + a related-person relationship + live-discovery
  toggle) → `POST /v1/discover/entity` → ranked candidate cards (name/handle, score, per-signal
  evidence chips, attributes, co-mentions, source link), best highlighted, with status/hops/fetches
  stats. Verified end-to-end in a real browser (form → endpoint → rendered result, console clean).
- [x] **Query generation** from attributes (`ai/app/entity_queries.py`) — the deterministic,
  attribute-anchored backbone (name × discriminator/relationship dorks, platform-scoped `site:`
  variants, ranked most-specific-first, §5 guardrail — every query carries ≥1 discriminator, a bare
  common surname is never fanned out). Also the LLM-down fallback.
- [x] **LLM planner** (`ai/app/entity_planner.py`) — the local LLM as reasoner: proposes extra
  queries (locale phrasings, username guesses, roster/venue angles), **unioned with** (never
  replacing) the deterministic backbone, each guardrail-filtered to carry a real discriminator.
  Degrades to the deterministic set on absent/errored/empty LLM. Wired into the endpoint (used when
  the local model is up).
- [x] **Entity-resolution scorer** (`ai/app/entity_resolve.py`) — scores a `Candidate`
  (name/handle/attributes/co-mentions) against the brief as the *fraction of known signals it
  corroborates* (accent- + spelling-tolerant via stdlib `difflib`/`unicodedata`), with a
  corroborated **relationship** as the heaviest signal, a per-signal breakdown for the evidence
  trail, and `propose_enrichments` (learned attributes + inferred given name) for OBSERVE→REFINE.
- [x] **Candidate extraction** (`ai/app/entity_extract.py`) — deterministic pass that turns a
  document into `Candidate`s anchored on the brief's name (nearby attribute corroboration,
  co-mentions, handle from URL/@mention). LLM-assisted "read" is a later refinement.
- [x] **Search adapter** (`ai/app/entity_search.py`) — `make_search` unions any number of doc
  sources (dedup by URL) and extracts candidates. Two sources wired: **retrieval-backed** (hybrid
  retrieval over the indexed corpus) and **live SearXNG discovery** (`make_searxng_discover` queries
  the self-hosted metasearch JSON API and extracts result snippets inline — reaching pages **not yet
  indexed**, the core recall lever; no key, degrades to []). The endpoint enables live discovery when
  reachable (request `discover` flag). Deeper full-page fetch of discovered URLs is a later refinement.
- [~] Per-run **budget** (`max_hops/fetches/wall_s`) enforced in the loop ✅; a persisted, resumable
  per-run lead frontier (so a long run survives a restart) is a later refinement.
- [x] **Resolution eval** (`ai/eval/resolution_eval.py` + `resolution_cases.jsonl`) — runs the real
  loop over labeled synthetic corpora (target + distractors + noise) and reports resolution accuracy
  / recall / resolved-rate / avg hops+fetches; **offline** (no stack), gated in CI by
  `tests/test_resolution_eval.py`. Current shipped set: 5/5 resolved, accuracy = recall = 1.0. Live
  targets over the real index build on this later.

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
