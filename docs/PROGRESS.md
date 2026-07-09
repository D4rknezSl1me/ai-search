# Project Progress Log

Reverse-chronological record of meaningful changes. Update this on every meaningful change
(see `CLAUDE.md` rule 3). Format: date · what · why · verification.

## Backlog (deferred follow-ups)

- [x] **FETCHING reaper** — done (2026-07-09). `frontier_urls.claimed_at` + goroutine ticker
  (`CRAWLER_REAP_AFTER_S`, default 300s) requeues stuck URLs under the retry cap.
- [x] **Unit tests** — done (2026-07-09). `urlx` and `simhash` tests, run via `go test` in a
  golang container.
- [ ] **GPU embeddings/reranker on Blackwell (sm_120)** — TEI's candle backend hangs at warmup
  on sm_120 here (loads on CUDA, then stalls); running on **CPU** for now. The GPU image is
  built (`deploy/tei-blackwell`) and one `TEI_IMAGE` swap away once TEI ships stable Blackwell
  kernels. Also restore `bge-reranker-v2-m3` (multilingual) over MiniLM at that point.
- [ ] **Richer query understanding** — LLM-driven expansion/decomposition and intent-based
  freshness boosting (current pipeline normalizes + filters only).
- [ ] **Index reconciliation** — reconcile `documents.n_chunks` vs actual Qdrant/OpenSearch
  counts; prune stale chunks when a doc is re-chunked to fewer pieces.
- [ ] **max_pages best-effort overshoot** — tighten the concurrent cap if it matters.
- [ ] Non-HTML parsing (PDF/doc) and JS/social rendering are phase-tracked (Phase 3), not backlog.
- [ ] **Playwright browser-worker pool** — consume `documents.meta.needs_render` (set by the
  escalation gate below): a separate low-concurrency render queue that re-fetches flagged URLs
  with a headless browser, then re-extracts/re-indexes. Anti-detection stack rides on this.

---

## 2026-07-09 — Phase 3: static→browser escalation gate

**What**
- New `crawler/internal/render` package — the decision layer at the front of the browser path.
  `NeedsRender(mode, rawHTML, extractedText, linkCount)` returns whether a statically-fetched page
  is really a JS-rendered shell that a headless browser must re-fetch.
  - `Mode` (`never | auto | always`, parsed from the campaign's new `render_js` config field;
    empty ⇒ auto). `never`/`always` short-circuit; `auto` applies heuristics.
  - Auto heuristic is **recall-first but cost-aware**: it escalates only when the extracted text is
    sparse (`<400` bytes) AND a positive JS-app signal is present — SPA root markers (`id="__next"`,
    `id="root"`+`data-reactroot`, `ng-app`, Nuxt/Gatsby/Vue-SSR markers, `window.__INITIAL_STATE__`),
    framework bundles (`_next/static`, `/static/js/`, `webpack`, …) on a link-starved page, a
    `<noscript>` "enable JavaScript" prompt, or near-empty text (`<120` bytes) with any `<script>`.
    A short but link-rich nav/index page with no JS signal stays on the static path.
  - Returns the matched `Reasons` so the verdict is explainable in metrics and doc metadata.
- Wired into `crawler/internal/crawl/scheduler.go`: after extraction, the scheduler computes the
  decision using the campaign's `render_js` mode. On escalation it increments
  `crawler_render_escalations_total{reason}` (new metric) and stamps `needs_render:true` +
  `render_reasons:[…]` into the `documents.meta` JSON — the durable hand-off the future Playwright
  pool will poll. Non-escalated pages are unchanged.
- `store.CampaignConfig` gains `RenderJS string` (`render_js`), already documented in
  `docs/04-CRAWLER.md`'s campaign schema.

**Why**
- Phase 3's JS half needs an entry point: the crawler must *know which* pages are worth the
  expensive browser render before a pool exists to render them. This gate makes escalation
  observable and durable end-to-end now (metrics + `needs_render` flags accumulating on real
  crawls), so the browser-worker pool becomes a consumer of an already-proven signal rather than a
  big-bang addition. Bias-to-recall (per `CLAUDE.md`) with a cost guard: we only pay for a browser
  when static extraction genuinely came up empty against a JS-app shell.

**Verification**
- `go build ./...` + `go vet ./internal/render/...` clean in a `golang:1.25-alpine` container
  (same toolchain as the crawler Dockerfile; `go mod tidy` resolves deps at build time).
- `go test ./internal/render/...` → `ok` — 9 tests covering mode parsing/round-trip and the
  heuristic: rich article stays static, Next.js shell / React root+bundle / noscript prompt all
  escalate with the expected reasons, short-but-no-signal page stays static, and never/always
  short-circuit correctly. `./internal/crawl` compiles against the new wiring.

---

## 2026-07-09 — Phase 3: social ingester — posts land in the RAG pipeline

**What**
- New `crawler/internal/social/ingest.go` — an `Ingester` that drives an adapter over a seed
  **end-to-end** and persists what it parses: `Discover(seed)` → for each target walk pages
  (`Fetch` → `Parse` → persist each doc → follow the adapter's `Paginate` cursor) up to a
  per-target page cap. This is the missing wire in Phase 3: adapters previously only produced
  `NormalizedDoc`s in memory (+ health), with nothing driving them or storing their output —
  "scheduler routing/queue pending." The ingester is that piece.
  - A small `Sink` interface (`Persist(ctx, *NormalizedDoc) (inserted, err)`) keeps package
    `social` free of `store`/`blob` deps and makes the runner unit-testable with a fake.
  - Failure policy matches the crawler's "fail loud, don't under-collect": a **fetch error** ends
    that target's walk (no response ⇒ no pagination cursor) but is only counted, not fatal;
    **parse/persist errors** are counted but do not stop the remaining docs/pages; if the adapter
    **auto-disables mid-run** (error rate crossed its threshold) the walk stops immediately.
  - `maxPages<=0` falls back to `DefaultMaxPages=1` (single page) so a run never walks an
    unbounded timeline by accident; deeper walks are an explicit opt-in via the request.
  - Returns a `Result` (targets/pages/docs/inserted/duplicates/errors) so a caller sees exactly
    what landed. Adapters record their own fetch/item health internally, so the ingester does
    **not** double-count — it only reads `Health().Enabled`.
- New `crawler/internal/api/socialsink.go` — the concrete `Sink` bridging to storage: writes the
  post's clean text to MinIO keyed by content hash (same `blob.TextKey` contract as web pages, so
  the intelligence plane chunks/embeds social posts identically) and inserts a `documents` row
  carrying `NormalizedDoc.Meta()` (`source:"social"`, platform, post_id, engagement, …). Source
  host is the permalink's host (the real instance, e.g. `mastodon.social`) falling back to the
  platform label. Exact-dedup is by content hash (platform+post_id), so re-ingesting a post is a
  no-op insert reported back as a duplicate.
- Wired `POST /internal/social/ingest` into the control API (`{adapter, seed, max_pages}`) — the
  operator-facing trigger for the social fetch path. It builds an ingester over the live registry
  + a `socialSink{store, blob}`, runs synchronously, and returns the `Result`. Guard paths: 405
  (non-POST), 400 (missing adapter/seed), 503 (no registry), 502 (unknown/disabled adapter or a
  discover error, with the partial `result` echoed).

**Why**
- Phase 3's social half is only useful if social posts actually reach the index. The adapters +
  health endpoint existed, but nothing connected `Adapter` output to the `documents`/text-blob
  pipeline the RAG plane reads. This closes that gap: a seed can now be ingested and its posts
  become searchable alongside crawled web pages — completing the "social fetch queue / scheduler
  routing" roadmap item.

**Verification** (golang:1.25-alpine container; no local toolchain, `go mod tidy` at build)
- `gofmt` clean on all changed/new files; `go vet ./...` clean; `go build ./...` clean; full
  `go test ./...` green.
- New `social/ingest_test.go` (7 tests, fake adapter + fake sink, no network) PASS: pagination
  across two pages with cross-page dedup (docs=4 → inserted=3/duplicates=1); `maxPages` cap
  fetches one page; `maxPages<=0` falls back to `DefaultMaxPages`; a fetch error is counted and
  ends the target (errors=1, pages=0); parse **and** persist errors are counted without aborting
  the batch (docs=2, errors=2, inserted=0); unknown + disabled adapters error before any fetch;
  discover error surfaces with errors=1.
- New `api` endpoint tests (4, via `httptest`, store/blob nil since guard paths resolve first)
  PASS: 405 on GET, 400 on empty adapter/seed, 503 on nil registry, 502 on unknown adapter with
  the `result` echoing the requested adapter.

**Status:** Phase 3 — social ingestion is now **end-to-end** (3 credential-free adapters →
ingester → storage/index, with health + status endpoint). Remaining for the phase: the Playwright
browser path (JS render) and anti-detection stack.

---

## 2026-07-09 — Phase 3: social adapter registry + per-adapter status endpoint

**What**
- New `crawler/internal/social/registry.go` — a `Registry` that holds the wired-up adapters by
  name in registration order and exposes live health without reaching into each adapter:
  `Register`, `Get`, `Names`, `Status(name)`, `Statuses()`. Re-registering a name is last-write-
  wins but **keeps the adapter's original position** so listings stay stable across a hot-swap.
  Added `Summary` + `Summarize()` — an aggregate fold (`adapters/enabled/disabled/fetches/
  errors/items`) so a caller gets an "is anything wrong" signal without folding the list itself.
  `DefaultRegistry(userAgent, timeout, pageLimit)` preloads the three credential-free adapters
  (Mastodon, Hacker News, Lemmy).
- Wired into the control API (`crawler/internal/api/api.go`): `NewServer` now takes the registry;
  `GET /internal/social/adapters` returns `{summary, adapters[]}` and
  `GET /internal/social/adapters/{name}` returns one snapshot (404 if unknown; empty-name path
  falls through to the full list; nil-registry path is defensive and non-panicking).
- Wired into `crawler/main.go`: builds `social.DefaultRegistry` from config (user-agent + fetch
  timeout) and logs the registered adapter names at startup. This is the **first place the social
  adapters are actually instantiated in the running binary** — previously they existed only as a
  package + tests.
- Closes the last open item under Phase 3's "adapter health metrics + auto-disable + contract
  tests" (the per-adapter status endpoint), completing the health-monitoring half of the exit
  criteria on the ingestion side.

**Why**
- Phase 3 exit criteria require "≥3 social adapters ingesting **with health monitoring**." The
  counters + auto-disable + Prometheus metrics existed, but there was no operator-facing surface
  to see which platforms are live and whether any auto-disabled — this endpoint provides it, and
  instantiating the registry in `main` means the adapters are now part of the running service,
  not just library code.

**Verification** (golang:1.25-alpine container; no local toolchain, `go mod tidy` at build time)
- `go vet ./...` clean; `go build ./...` clean; `gofmt` clean on all changed files.
- New `registry_test.go` (5 tests) PASS: registration order preserved; re-register keeps position
  and replaces the adapter; unknown `Status`/`Get` report `ok=false`; `Statuses` + `Summarize`
  reflect a healthy vs an auto-disabled (100%% error rate) adapter with correct aggregate totals;
  `DefaultRegistry` has exactly the three named adapters, all fresh/enabled with zero counters.
- New `api/social_test.go` (5 tests, via `httptest`) PASS: list endpoint returns 3 adapters +
  summary; by-name returns the mastodon snapshot; unknown name → 404; trailing-slash empty name
  lists all; nil-registry list path returns 200 without panicking. store/blob are nil in these
  tests since the social handlers don't touch them.

**Status:** Phase 3 in progress — 3 credential-free adapters **with a live health/status endpoint
and Prometheus metrics**; the Playwright browser path (JS render) and scheduler/queue routing
remain for the JS half of the exit criteria.

---

## 2026-07-09 — Phase 3: Lemmy adapter (third credential-free source; ≥3-adapter bar met)

**What**
- New `crawler/internal/social/lemmy.go` — a third adapter on the Phase 3 framework, using
  **Lemmy's public v3 REST API** (the fediverse's Reddit-shaped link-aggregator; public listings
  need no auth, docs/08 §5):
  - `Discover` handles three seed shapes: a **bare instance host** (`lemmy.ml`) → its
    all-communities post listing; a **community reference** (`lemmy.ml/c/technology`, also as a
    URL) → that community's listing (`community_name=technology`); a **full v3 API URL** →
    passthrough (so a caller can also point at `/api/v3/comment/list` to ingest a thread).
    Empty seeds error loudly.
  - `Fetch` GETs a listing page (8 MiB cap, health recorded). Lemmy paginates by **page number**,
    not a cursor header, so `Paginate` increments the target's `page` param and **stops when the
    current page came back empty** (no endless blank-page fetching).
  - `Parse` decodes the response envelope, handling **both** `posts` (PostView) and `comments`
    (CommentView) so the same adapter ingests roots and threads. Post/comment ids live in
    **separate integer spaces** on Lemmy, so `PostID` is namespaced (`post/<id>`, `comment/<id>`)
    to keep the `platform+post_id` content hash collision-free and to let `ParentID` reference the
    right ancestor. Comment threading is rebuilt from the **materialized `path`** (`0.<self>` →
    parented to the post; `0.<ancestor>.<self>` → parented to the ancestor comment). Deleted/
    removed/empty-text items are dropped; link-posts carry the external URL in `MediaURLs` and
    `Text = title + body` (headline searchable, mirrors HN). A tolerant timestamp parser accepts
    both RFC3339 and the timezone-less naive-UTC form some Lemmy versions emit.
  - Reuses the framework's `htmlToText`, `resolveLang`, `makeTitle`, `firstNonEmpty`, and
    `HealthTracker`; emits the existing `crawler_social_{fetch,items}_total{adapter="lemmy"}`
    metrics — no new deps.
- This is a **third distinct pipeline shape**: page-numbered listing pagination + a post/comment
  envelope with nested creator/community/counts + Markdown bodies + path-based threading —
  different from both Mastodon (one Link-cursor timeline page) and HN (id-array feed + per-item
  fetch), further proving the adapter interface generalizes.

**Why**
- Meets Phase 3's **"≥3 social adapters ingesting"** exit criterion on the ingestion side with a
  **zero-credential** source (no owner blocker), keeping breadth-first/recall-first momentum
  (CLAUDE.md north star) while the login-walled platforms wait on owner creds. Lemmy adds
  high-signal community discussion with full comment threads.

**Verification** (golang:1.25-alpine container; no local toolchain, `go mod tidy` at build time)
- `go vet ./...` clean; `go build ./...` clean; full `go test ./internal/social/` green.
- New `lemmy_test.go` (5 tests) all PASS: post listing (link-post title-only text + external
  url→media + score/comments engagement + RFC3339 decode; self-post title+body + naive-UTC
  decode; removed post dropped; meta shape), comment listing (entity decode, top-level→`post/…`
  and nested→`comment/…` parent mapping, `@handle` title, no post-level engagement key), Discover
  (bare host vs community vs URL-community vs API passthrough + empty-seed error), Paginate
  (page increment on a non-empty page, stop on an empty listing), and distinct content_hash for
  a `post/5` vs a `comment/5` (namespacing prevents dedupe collision).

**Status:** Phase 3 in progress — **3 credential-free social adapters** (Mastodon, Hacker News,
Lemmy) with health monitoring; the Playwright browser path (JS render) and scheduler/queue
routing remain for the JS half of the exit criteria.

---

## 2026-07-09 — Phase 3: Hacker News adapter (second credential-free source)

**What**
- New `crawler/internal/social/hackernews.go` — a second adapter on the Phase 3 framework,
  using the **official HN Firebase API** (`hacker-news.firebaseio.com/v0`, fully public, no auth):
  - `Discover` handles three seed shapes: a **feed name** (`top`/`new`/`best`/`ask`/`show`/`job`,
    also tolerating the `topstories` form) → one API round-trip that expands the story list to the
    first `limit` item URLs (default 30, the front-page size); a **numeric item id** → that item's
    URL; a **full item API URL** → passthrough. Unknown seeds error loudly (docs/08 §8).
  - `Fetch` GETs a target (story list or item), 8 MiB cap, records health.
  - `Parse` turns a single item object into one `NormalizedDoc`: strips comment/Ask-HN HTML to
    text, sets `Text = title + body` (so a link-story's headline is searchable), maps `parent`→
    `ParentID` for thread reconstruction, `by`→author, unix `time`→`PostedAt`, external story
    `url`→`MediaURLs`, and `score`/`descendants` engagement for stories/jobs (not comments).
    Deleted/dead/`null`/empty items are dropped.
  - Reuses the framework's `htmlToText`, `resolveLang`, `makeTitle`, and `HealthTracker`; emits the
    existing `crawler_social_{fetch,items}_total{adapter="hackernews"}` metrics — no new deps.
- This is a **different pipeline shape** than Mastodon (a flat id-array feed expanded at Discover
  time + per-item fetch, vs one paginated timeline page), which proves the adapter interface
  generalizes beyond a single platform model.

**Why**
- Directly advances Phase 3's "≥3 social adapters" exit criterion with a **zero-credential**
  source (no owner blocker), and validates the framework against a second, structurally different
  API before investing in the login-walled platforms. Recall-first (CLAUDE.md north star): HN adds
  high-signal tech discussion with full comment threads.

**Verification** (golang:1.25-alpine container; no local toolchain)
- `go vet ./...` clean; `go build ./...` clean; `go test ./...` green.
- New `hackernews_test.go` (6 tests) all PASS: story parse (title-only text, external url→media,
  score/descendants, unix time decode, meta shape), comment parse (HTML strip + entity decode,
  `parent_id`, @handle title fallback, no score key), Ask-HN (title+body), skip contract
  (deleted/`null`/empty → 0 docs), Discover (id/URL passthrough + unknown-seed error), and
  distinct content_hash per post.

**Status:** Phase 3 in progress — 2 credential-free social adapters (Mastodon, Hacker News);
one more open adapter or the Playwright browser path next.

---

## 2026-07-09 — Phase 3: social adapter framework + Mastodon adapter

**What**
- New `crawler/internal/social` package — the per-platform adapter framework (docs/08 §2):
  - `Adapter` interface (`Discover`/`Fetch`/`Parse`/`Paginate`/`Health`) and a `NormalizedDoc`
    that maps a social post/comment onto the crawler's Document model, carrying the social
    identity fields in `Meta()` (docs/08 §6: platform, post_id, author_handle, posted_at,
    engagement, parent_id, media_urls, permalink). `ContentHash` is keyed by `platform+post_id`
    (not text) so distinct posts with identical short text ("gm") stay distinct under the
    `documents` unique-content_hash constraint; `simhash` still handles near-dups.
  - `HealthTracker` — per-adapter fetch/error/item counters with an error-rate auto-disable
    threshold + min-sample guard (docs/08 §8, "fail loud"); feeds Prometheus.
  - **Mastodon adapter** (`mastodon.go`) — the credential-free first platform (open public API,
    no auth): `Discover` (instance host → public timeline, API URLs passed through), `Fetch`
    (HTTP + `Link: rel="next"` cursor extraction, 8 MiB cap), `Parse` (unwraps boosts to the
    original, strips post HTML to text w/ entity decode, declared-or-detected language, drops
    empty media-only posts), `Paginate` (max_id cursor).
- New Prometheus metrics: `crawler_social_fetch_total{adapter,result}`,
  `crawler_social_items_total{adapter}`.
- Chose Mastodon first (over Reddit/Telegram) because it needs **no credentials** — building the
  social path end-to-end without blocking on owner-provided API keys (Reddit OAuth, Telegram
  MTProto id/hash will be logged to `ralph/QUESTIONS.md` when those adapters land).

**Why**
- Start Phase 3 (JS + social ingestion) with the highest-ROI, zero-credential source so the
  normalization + health-monitoring scaffold is proven before the hostile platforms. Recall-first
  (CLAUDE.md north star): social is the highest-value source.

**Verification** (golang:1.25-alpine container; no local toolchain)
- `go vet ./...` clean; `go build ./...` clean; `go test ./...` green.
- New `internal/social` tests (5) all PASS: Mastodon parse contract (HTML→text + entity decode,
  boost unwrap/dedupe, reply `parent_id`, federated handle, engagement, permalink, meta shape),
  distinct content_hash per post, `Link` header `rel="next"` extraction, `Discover`, and
  HealthTracker auto-disable (min-sample + threshold).

**Status:** Phase 3 in progress — adapter framework + first (Mastodon) adapter parse/normalize
proven offline. Next: wire the social fetch queue into the scheduler (route adapters through the
frontier), then add Reddit/YouTube-transcript adapters (Reddit needs owner OAuth creds).

---

## 2026-07-09 — Phase 2: Search / RAG API

**What**
- **Clean-text gap resolved (the prerequisite):** the crawler now writes clean extracted text to
  MinIO at `text/{content_hash}.txt.gz` (idempotent, ungated by insert-dedup so re-crawls backfill
  known docs). Chosen over re-extraction in Python; the indexer reads it by content hash. NATS
  `doc.ready` streaming deferred — poll-based indexing covers backfill + new docs simply.
- **Intelligence plane (`ai/`, FastAPI):**
  - `indexer` — polls Postgres for un-indexed docs (`n_chunks=0`), pulls text from MinIO,
    structure-aware chunking (~400 tok, ~12% overlap), TEI embeddings, upserts to Qdrant
    (vectors) + OpenSearch (text) idempotently by `chunk_id`; `-1` sentinel for empty docs.
  - `indexes` — idempotent Qdrant collection (Cosine, payload indexes) + OpenSearch mapping
    (shingle sub-field) on startup.
  - `retrieval` — BM25 ∪ ANN with shared filters → RRF → cross-encoder rerank (TEI) →
    dedupe/diversify with recall-first backfill. BGE query instruction on the query side.
  - `synthesis` — grounded answer via local Ollama (`llama3.1:8b`), numbered `[n]` citations,
    citation verification, confidence/coverage; SSE streaming.
  - `main` — `/v1/search` (synthesis, streaming), `/v1/retrieve`, `/v1/coverage`, `/metrics`,
    `/readyz`; graceful degradation everywhere.
  - `eval/` — labeled set + runner: recall@k, MRR, nDCG; citation accuracy + LLM-as-judge
    groundedness (local model, no paid judge).
- **Backlog cleared:** FETCHING reaper (`claimed_at` col via migration `0002` + ticker) and
  `urlx`/`simhash` unit tests.
- **GPU/infra:** TEI image made swappable (`TEI_IMAGE`); built a Blackwell/sm_120 image
  (`deploy/tei-blackwell`, blackwell TEI binary + CUDA 12.9 math libs, compat driver removed).
  It loads on CUDA but the candle backend hangs at warmup on sm_120, so embeddings + reranker
  run on **CPU** for now (models pre-staged in the teicache volume, `HF_HUB_OFFLINE=1`). Reranker
  uses `ms-marco-MiniLM-L-6-v2` (candle-stable) instead of `bge-reranker-v2-m3` for now.

**Why**
- Deliver the query-time product: natural-language question → grounded, cited answer over
  crawled content, fully self-hosted (CLAUDE.md rule 2). Recall-first per the north star.

**Verification** (live stack, fresh `quotes-phase2` crawl: 33 docs)
- Indexing: 33/33 docs indexed → **52 chunks**, matching across Postgres, Qdrant (green, 52
  points), OpenSearch (52 docs).
- `/v1/retrieve`: hybrid hits with both lexical+vector ranks; rerank reorders (top result
  promoted). `/v1/search`: grounded answer with inline `[n]` citations resolving to real URLs;
  correctly refuses to hallucinate absent facts.
- **Eval (8 labeled queries):** recall@5 = recall@10 = **1.0**, nDCG@10 = 0.913, MRR = 0.9;
  citation accuracy = **1.0**, groundedness (LLM-judge) = 0.75, 8/8 answered.
- Degradation: Qdrant down → lexical-only (verified); `synthesize:false`/LLM down → retrieve-only
  (verified). SSE stream emits `source`→`token`→`citations`→`meta`→`done`.
- `go test` green for `urlx` + `simhash`.

**Status:** Phase 2 complete. Next: Phase 3 — JS + social ingestion. Follow-ups: GPU embeddings
on Blackwell once TEI kernels stabilize; richer query understanding; index reconciliation.

---

## 2026-07-09 — Phase 1: Crawler MVP

**What**
- Implemented the crawler ingestion loop in Go (`crawler/internal/...`):
  - `urlx`: URL canonicalization, hashing, scope helpers.
  - `simhash`: 64-bit near-duplicate fingerprint.
  - `store` (pgx): frontier (claim via `FOR UPDATE SKIP LOCKED`), documents (exact-dedup on
    content_hash), sources, campaigns; JSONB marshalled + cast to avoid pgx ambiguity.
  - `blob` (minio-go): gzip raw content to MinIO, keyed by content hash.
  - `fetch`: polite HTTP client (UA, timeout, body cap, redirect policy).
  - `extract`: go-readability main content + metadata, whatlanggo language detect, link
    discovery, content/simhash.
  - `crawl`: worker pool + per-host politeness limiter; fetch→extract→store→discover; retry
    with attempt cap; max_depth/max_pages scope.
  - `api`: control endpoints (`POST /internal/campaigns`, `GET /internal/frontier`,
    `/internal/coverage`, `/internal/documents/{id}`) + `/healthz` `/readyz` `/metrics`.
- Crawler Dockerfile builds via `go mod tidy` (Go 1.25; minio-go v7 needs ≥1.25).
- Added crawler tuning vars to `.env`/`.env.example`.

**Why**
- Deliver the breadth-first ingestion engine: seed a campaign and stream documents into storage.

**Verification** (live run against quotes.toscrape.com, max_pages 30)
- Campaign seeded; crawl ran end-to-end: 37 fetched (all 200), **28 unique documents**,
  **9 exact duplicates skipped**, 137 links discovered, max_pages enforced (101 skipped).
- Metadata extracted (title/author/lang), raw content gzipped in MinIO, `/metrics` populated,
  `/readyz` green (postgres+minio).

**Known limitations (follow-ups):**
- No reaper for URLs stuck in `FETCHING` if the process dies mid-fetch (add a timeout requeue).
- `max_pages` is best-effort (minor overshoot under concurrency).
- Non-HTML (PDF/doc) not yet parsed; JS/social rendering is Phase 3.

**Status:** Phase 1 complete. Next: Phase 2 — Search/RAG API (embeddings → indexes → retrieve
→ rerank → local-LLM synthesis).

---

## 2026-07-09 — Decision: no paid services; synthesis via local LLM

**What**
- New hard constraint (owner): **no paid or external SaaS dependencies** — fully self-hosted.
- Synthesis LLM changed from a hosted API (Claude) to a **local model via Ollama** on the RTX
  5070 (default `llama3.1:8b`, swappable; 14B possible by time-sharing VRAM).
- Removed `ANTHROPIC_API_KEY`; added `LLM_HOST/LLM_PORT/LLM_MODEL` to `.env`/`.env.example` and
  `ai/app/config.py`.
- Added an `llm` (Ollama) service to `deploy/docker-compose.yml` under the `gpu` profile
  (+ `ollamadata` volume).
- Discovery/ingestion narrowed to **free/open sources only** (Common Crawl URL indexes, sitemaps,
  RSS, free/open platform APIs, browser scraping); removed reliance on commercial search APIs,
  paid proxy providers, and paid social-data vendors.
- Swept docs: added rule 2 to `CLAUDE.md`; updated `00, 01, 02, 03, 04, 07, 08, 09, 10, 12, 13`
  and README; API `cost{usd}` → `usage{gpu_ms}`; GPU-fit plan for embeddings+reranker+LLM in 12 GB.

**Why**
- Owner requires the project to never depend on paid services; run entirely on owned hardware.

**Verification**
- `docker compose config` valid for base + `app` + `gpu` (tei + llm) profiles.
- ai-api rebuilt with new config; `/healthz` ok, `/readyz` ready (qdrant/opensearch ok).
- Grep sweep confirms no unintended paid-service references remain.

**Status:** Phase 0 intact. Local-LLM synthesis to be implemented in Phase 2.

---

## 2026-07-09 — Project bootstrap: docs + Phase 0 scaffold

**What**
- Wrote full documentation set (`docs/00`–`13`) and README index.
- Implemented **Phase 0 — Infrastructure scaffold**:
  - `deploy/docker-compose.yml`: Postgres, Redis, NATS (JetStream), Qdrant, OpenSearch, MinIO,
    Prometheus, Grafana (base); crawler + ai-api (`app` profile); TEI (`gpu` profile).
  - `db/migrations/0001_init.sql`: schema (sources, campaigns, documents, frontier_urls, jobs),
    auto-applied on Postgres init.
  - `crawler/`: Go skeleton (stdlib) with `/healthz`, `/readyz`, `/metrics`, TCP dep checks.
  - `ai/`: FastAPI skeleton with `/healthz`, `/readyz`, `/metrics`, HTTP dep checks.
  - Task runners `tasks.ps1` (Windows) and `Makefile`; `config/config.example.yaml`.
- Added project `CLAUDE.md` (core purpose = maximum recall; legal deferred to owner; always
  track progress in docs).
- Populated `.env` with strong generated secrets (local device only; gitignored).

**Why**
- Establish a runnable, measurable foundation before building the crawler (Phase 1), per the
  breadth-first roadmap.

**Verification**
- `docker compose config` valid.
- All 8 infra services healthy; DB shows 5 tables; OpenSearch `green`; Qdrant/NATS/MinIO OK.
- `crawler /readyz` → postgres/redis/nats ok. `ai-api /readyz` → qdrant/opensearch ok
  (tei optional, gpu profile off).
- Secrets rotated: volumes recreated so new `.env` credentials took effect (re-verified healthy).

**Status:** Phase 0 complete. Next: Phase 1 — Crawler MVP (`docs/12-ROADMAP.md`).
