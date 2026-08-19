# Project Progress Log

Reverse-chronological record of meaningful changes. Update this on every meaningful change
(see `CLAUDE.md` rule 3). Format: date · what · why · verification.

## Backlog (deferred follow-ups)

- [x] **Live end-to-end verification of Phase 4 discovery + text extraction** — done (2026-08-19,
  see the live-verification entry below). The four features whose live checks were previously
  deferred (sitemap / RSS-Atom / Common Crawl ingestion, plain-text extraction) are now verified
  against a running crawler+Postgres+MinIO stack.
- [x] **Plain-text (non-HTML) extraction** — done (2026-08-19). `text/*` bodies (plain, markdown,
  csv, logs) now index instead of being dropped; see entry below. PDF/doc binary parsing still open.

- [x] **FETCHING reaper** — done (2026-07-09). `frontier_urls.claimed_at` + goroutine ticker
  (`CRAWLER_REAP_AFTER_S`, default 300s) requeues stuck URLs under the retry cap.
- [x] **Unit tests** — done (2026-07-09). `urlx` and `simhash` tests, run via `go test` in a
  golang container.
- [ ] **GPU embeddings/reranker on Blackwell (sm_120)** — TEI's candle backend hangs at warmup
  on sm_120 here (loads on CUDA, then stalls); running on **CPU** for now. The GPU image is
  built (`deploy/tei-blackwell`) and one `TEI_IMAGE` swap away once TEI ships stable Blackwell
  kernels. Also restore `bge-reranker-v2-m3` (multilingual) over MiniLM at that point.
- [x] **Richer query understanding** — done (2026-08-19). LLM expansion/decomposition
  (`ai/app/understand.py`), freshness-aware ranking (`ai/app/freshness.py`), and rule-based intent
  classification driving intent→freshness boosting (`ai/app/intent.py`). See entries below.
- [ ] **Index reconciliation** — reconcile `documents.n_chunks` vs actual Qdrant/OpenSearch
  counts; prune stale chunks when a doc is re-chunked to fewer pieces.
- [ ] **max_pages best-effort overshoot** — tighten the concurrent cap if it matters.
- [ ] Non-HTML parsing (PDF/doc) and JS/social rendering are phase-tracked (Phase 3), not backlog.
- [x] **Playwright browser-worker pool** — **done** (2026-07-09, see entries below): the
  escalation gate enqueues flagged URLs into a durable `render_queue` (claim/lease/retry + reaper),
  `POST /internal/render/ingest` lands a worker's rendered DOM in the documents + text-blob pipeline
  (same extract path as a static fetch), and the `browser-worker/` Playwright service now closes the
  loop — a long-lived Chromium claims jobs, renders JS-heavy pages, and POSTs the resolved DOM back
  for indexing. Verified live (rendered a real Wikipedia SPA → indexed). Lightweight anti-detection
  is in place; the full fingerprint/proxy stack remains a later Phase 3 refinement.

---

## 2026-08-19 — Phase 4: crawler coverage/stats API (operator visibility)

**What**
- `GET /internal/coverage` returned only `{total_documents}`. Replaced with a real coverage summary
  (`store.CoverageStats` → `store/coverage.go`): total documents, source count, **documents by
  content-type** (base type, charset stripped) and **by language** (top 10 each), **frontier by
  state**, plus recrawl visibility — `with_validators` (frontier rows carrying an ETag/Last-Modified)
  and `recrawlable` (FETCHED rows with a `last_fetched_at`). A small `labelCounts` helper runs the
  grouped aggregates; the handler just serializes the struct.

**Why**
- Phase 4 "coverage/stats API." Operating a breadth-first crawl (and the future UI) needs to see
  *what* has been collected and the frontier's health at a glance — content-type/language mix flags
  extraction gaps, frontier-by-state shows progress/failures, and the recrawl counters make the new
  freshness machinery observable. Cheap (a few `GROUP BY`s), no new dependency.

**Verification** (crawler rebuilt into the running stack):
- `go build ./...` + `go vet ./...` clean.
- **Live** `GET /internal/coverage` against the session's real data returned: `total_documents:80`,
  `sources:13`; by_content_type `text/html:49, application/social+json:30, text/plain:1` (the
  plain-text extraction shows up); by_lang `en:58, unknown:21, sv:1`; frontier `SKIPPED:319,
  FETCHED:67, FAILED:2`; `with_validators:3`, `recrawlable:5` — i.e. the conditional-GET/recrawl
  features are reflected in the numbers.

---

## 2026-08-19 — Phase 4: conditional GET (ETag / If-Modified-Since) — cheap recrawls

**What**
- Made recrawl cheap: an unchanged page now costs one conditional round-trip and **no**
  re-extraction/re-indexing. Builds directly on the recrawl scheduler.
  - `frontier_urls.etag` / `last_modified` columns (migration `0005` + `EnsureRecrawlColumns`, both
    extended). `store.ClaimNext` returns them on each `FrontierItem`; `store.SetValidators(id, etag,
    last_modified)` records the response validators (`NULLIF ''` so blanks clear).
  - `fetch.GetConditional(ctx, url, etag, lastModified)` sends `If-None-Match` / `If-Modified-Since`
    and, on **304**, returns `Result{NotModified:true}` with no body; it also captures the response
    `ETag`/`Last-Modified` on every fetch. `fetch.Get` is now a thin wrapper (no validators).
  - Scheduler: fetches conditionally with the URL's stored validators; a `304` short-circuits to
    `MarkFetched` (refresh `last_fetched_at`, skip extraction) and increments
    `crawler_conditional_not_modified_total` + `fetch_total{result="not_modified"}`; a `2xx` stores
    the new validators via `SetValidators` after indexing.

**Why**
- Recrawl (previous entry) keeps content fresh but would re-fetch+re-extract+re-embed every URL each
  cycle — wasteful on the owner's single box and on target sites. Conditional GET means only genuinely
  changed pages pay the full pipeline; unchanged ones are a tiny HEAD-like check. This is the
  "incremental/conditional GET" half of the Phase 4 recrawl item; explicit content-change detection
  (beyond validators) can build on the stored ETags later.

**Verification** (crawler rebuilt into the running stack; golang:1.25 container for tests):
- `go build ./...` + `go vet ./...` clean.
- **Unit** (`fetch_test.go`, httptest): first GET → 200 with `ETag`/`Last-Modified` captured; a
  conditional refetch with the ETag → `304`, `NotModified=true`, empty body. **PASS.**
- **Live integration** (`-tags integration`, running Postgres): `TestRecrawlRequeue` still passes
  with `ClaimNext` now selecting the validator columns.
- **Live end-to-end**: seeded `example.com` (sends `Last-Modified`) → the frontier row stored the
  validator (`last_modified` set) after the first fetch. Re-enqueued it (keeping the validator); the
  scheduler's conditional re-fetch returned **304** → `crawler_conditional_not_modified_total` went
  `0 → 1`, `fetch_total{result="not_modified"}=1`, and the row returned to `FETCHED` **without
  re-indexing**.

---

## 2026-08-19 — Phase 4: recrawl / freshness scheduling (re-fetch stale URLs)

**What**
- The frontier fetched each URL once and never revisited it, so the index went stale — a recall cap
  for anything that changes (news, profiles, listings). Added a **recrawl scheduler** that re-enqueues
  `FETCHED` URLs older than a horizon back to `PENDING` so the existing crawl loop refreshes them.
  - Migration `0005_frontier_recrawl.sql` + idempotent `store.EnsureRecrawlColumns` (run at boot,
    covers pre-migration volumes): `frontier_urls.last_fetched_at timestamptz` + a partial index on
    `(last_fetched_at) WHERE state='FETCHED'` for cheap staleness scans.
  - `store.MarkFetched` now stamps `last_fetched_at = now()`. New `store.RequeueForRecrawl(olderThan,
    limit)` flips the oldest-fetched stale rows to `PENDING` with a **reset attempt budget**
    (`attempts=0`, `claimed_at=NULL`), under `FOR UPDATE SKIP LOCKED`, returning the count.
  - `main.runRecrawler` — a ticker (interval = horizon/4, clamped to [1m, 1h]) gated by
    `CRAWLER_RECRAWL_AFTER_S` (0 = off), batch `CRAWLER_RECRAWL_BATCH` (128). Counter
    `crawler_recrawl_requeued_total`. Config knobs + `.env.example`.
  - Mirrors the existing FETCHING reaper's shape, so it's a small, well-understood addition.

**Why**
- Phase 4's headline crawler item (recrawl/freshness). Maximum recall over *current* information
  (CLAUDE.md) requires revisiting content, not just a one-time fetch. Conditional GET (skip
  re-processing unchanged pages via ETag/If-Modified-Since) is the natural next iteration to make
  recrawl cheap; this lands the scheduling half first.

**Verification** (crawler rebuilt into the running stack; golang:1.25 container for tests):
- `go build ./...` + `go vet ./...` clean; **full `go test ./...` passes** (every crawler package ok).
- **Live integration** (`-tags integration` against the running Postgres, DSN from `.env`):
  `TestRecrawlRequeue` — add two URLs → claim → `MarkFetched` (stamps `last_fetched_at`) → backdate
  one an hour → `RequeueForRecrawl(30m)` re-enqueues **exactly the stale one** (PENDING, attempts=0),
  leaving the fresh one FETCHED; a second immediate pass re-enqueues 0. **PASS.**
- **Live boot**: rebuilt crawler came up healthy; Postgres confirms the `last_fetched_at` column
  exists (the boot `EnsureRecrawlColumns` ran), and `/healthz` = ok.

---

## 2026-08-19 — Phase 4: live end-to-end verification (discovery + text extraction)

**What** — Brought the stack up (`postgres` + `redis` + `nats` + `minio` base infra, then a freshly
`--build`-ed `crawler` in the `app` profile) and exercised, against real external sources, the four
features that had only been unit-verified. Discharges their "deferred: live" notes.

**Verification** (crawler `/healthz` ok, `/readyz` → postgres+minio ok):
- **RSS/Atom feeds** — `POST /internal/feeds/ingest` on the BBC News RSS → `{discovered:10,
  enqueued:10}`. Live RSS parse + frontier enqueue confirmed.
- **Sitemaps** — `POST /internal/sitemap/ingest` on `cloudflare.com/sitemap.xml` and
  `wordpress.org/sitemap.xml` → `{discovered:15, enqueued:15}` each (capped at `max_urls:15`).
  A missing sitemap (`mozilla.org`, `python.org`) returned a clean `502` with the upstream `404`
  surfaced — error path confirmed too.
- **Common Crawl** — `POST /internal/commoncrawl/ingest {domain:"example.com", max_urls:5}` →
  `{discovered:2, enqueued:2}`, hitting the **real** CC `collinfo.json` + CDX index end-to-end.
- **Frontier populated** — campaign 9 (used for the ingests) held **128 `frontier_urls`** rows
  (seed + sitemap 30 + feeds 10 + CC 2 + crawl-discovered links), 12 `FETCHED`, rest `SKIPPED`
  under the campaign's `max_pages` — proving discovered URLs actually land in the frontier.
- **Plain-text extraction** — seeded a campaign at `rfc-editor.org/rfc/rfc1.txt` (a `text/plain`
  body). Within one crawl tick Postgres held the document: `content_type=text/plain;charset=utf-8`,
  `http_status=200`, `title="Network Working Group … Steve Crocker"` (first line), `meta.text_len=
  21079` — i.e. text that the old `IsHTML` gate would have dropped is now extracted and indexed.

**Why** — CLAUDE.md rule 4 ("bring services up, hit endpoints, report actual output; no done without
evidence"). Converts accumulated unit-only claims into live-proven behavior and catches any wiring
bugs (none found — all endpoints, validation, and the shared enqueue/index paths worked as built).

---

## 2026-08-19 — Phase 4: plain-text (non-HTML) extraction — stop dropping text/* content

**What**
- The crawler previously **dropped every non-HTML response** (`scheduler.go`: `IsHTML` gate →
  marked visited, never extracted/indexed), so `text/plain`, markdown, csv, logs, etc. never reached
  the index — a straight recall loss. Now `text/*` (excluding HTML) is extracted and indexed:
  - `fetch.IsText(contentType)` — true for `text/*` except HTML.
  - `extract.FromPlainText(url, raw)` — the body *is* the content, so no readability step: title =
    first non-blank line, no link discovery, rune-safe title/excerpt truncation (`truncateRunes`
    never splits a UTF-8 sequence). Reuses `buildDoc`, so content hash / language detect / simhash /
    dedup are identical to the HTML path.
  - Scheduler: factored the persist logic (raw+text blobs → `InsertDocument`) into a shared
    `Scheduler.index(...)` helper used by both paths. HTML keeps its escalation gate + link
    discovery; the text path skips both. Non-`text/*` (binary) still recorded as visited only.
    New `crawler_fetch_total{result="text"}` label.

**Why**
- Maximum recall is the north star (CLAUDE.md): a large amount of the open web — READMEs, docs,
  data files, mailing-list archives, `robots`/`ads.txt`, plaintext articles — is served as `text/*`.
  Indexing it is free (no new dependency; the bytes are already the text) and closes a real gap. The
  shared `index` helper also removes the duplicated persist code the two paths would otherwise carry.
  (PDF/doc binary parsing remains a separate, dependency-bearing follow-up.)

**Verification** (golang:1.25-alpine container; `go mod tidy` at build; minimal `go.mod`/no `go.sum`
restored after):
- `go build ./...` clean; `go vet ./...` clean; full `go test ./...` passes.
- New `fetch_test.go` — `IsText`/`IsHTML` across text/plain (+charset, +case), markdown, csv vs
  html/json/pdf/image/empty. New `extract_test.go` — `FromPlainText` (title = first line, body text,
  32-byte hash, simhash, `en` language detect, no links, excerpt), empty body, **UTF-8-safe rune
  truncation** (300×`é` → 200 runes, no split), and `firstLine` skipping blanks. Existing crawler
  packages still build/test clean.
- **Verified live** (2026-08-19, see the live-verification entry above): crawling
  `rfc-editor.org/rfc/rfc1.txt` indexed a `text/plain` document (`text_len=21079`) that the old gate
  would have dropped.

---

## 2026-08-19 — Phase 4: retrieval result cache (recall-safe, short TTL)

**What**
- New `ai/app/cache.py` — a small in-process `TTLCache` (fixed-size, LRU eviction, per-entry TTL,
  injectable clock). Lazy expiry on `get`, expired-purge + LRU trim on `set`, `ttl<=0` disables it.
- Wired into `retrieval.retrieve()`: a module-level `_result_cache` memoizes the `RetrievalResult`
  for identical queries. Key (`_cache_key`) is a normalized, order-insensitive digest of everything
  that changes the result — casefolded query, sorted filter lists, `max_sources`, `expand`, and the
  **effective** freshness. The cache is **bypassed whenever ranking is freshness-driven**
  (`effective_freshness == "fresh"`, which covers both an explicit `freshness=fresh` and a
  news/recency intent), so time-sensitive queries always recompute against the newest content.
- Config knobs `retrieval_cache_enabled` / `retrieval_cache_ttl_s` (60s) / `retrieval_cache_size`
  (512) + `.env.example`.

**Why**
- docs/07 lists a short-TTL query→results cache "respecting freshness intent." On the owner's single
  RTX 5070, every hit avoids a burst of embed + ANN + lexical + cross-encoder work (and downstream
  LLM for search), which matters for throughput on one box and for repeated/dashboard/eval queries.
  Kept strictly recall-safe: short default TTL and an automatic bypass for freshness-driven ranking
  mean freshly-crawled documents are never hidden behind a stale hit (north star intact).

**Verification**
- **Full offline suite: 83 tests pass** (`ai/tests/`, local `pytest`, no network). New
  `test_cache.py` (8): miss→hit, TTL expiry at the boundary (with drop), LRU eviction honoring
  recency touches, re-set refreshing the TTL window, `ttl=0` disabling storage, expired-purge on
  `set`; and `_cache_key` — stable across case/whitespace + filter-order differences, and distinct
  when any of query/max_sources/expand/freshness/filters changes. Prior tests (75) green.
- Import smoke: `app.main` builds; `_result_cache` initialized from settings (ttl 60, size 512).

---

## 2026-08-19 — Phase 4: Common Crawl URL-index seed discovery (massive cold-start breadth)

**What**
- New `crawler/internal/commoncrawl` package — discovers seed URLs from the **free Common Crawl URL
  index** (docs/04 §7), the biggest cold-start breadth source on the open web and a zero-cost
  substitute for commercial search APIs (CLAUDE.md rule 2). Given a domain, it queries the CC CDX
  index for every page CC has captured under it. Pure/offline-testable pieces:
  - `ParseCollinfo(data)` — extracts the CDX API endpoints (newest crawl first) from
    `collinfo.json`.
  - `ParseCDX(data)` — parses a CDX `output=json` response (newline-delimited JSON, one row per
    capture) into page URLs; tolerant (skips blank/unparseable rows and non-http(s) URLs).
  - `Discover(ctx, domain, fetch, opts)` — resolves the latest `MaxIndexes` crawls from collinfo
    (or an explicit `IndexURL`), queries each with `url=<domain>/*&output=json&limit=<remaining>`,
    de-dupes across crawls, and caps at `MaxURLs`. A failed index shard is skipped (best-effort,
    recall-first); only a collinfo-resolution failure is fatal. `fetch` is injected, so the whole
    flow is unit-tested with a fake router (no network).
- Control endpoint `POST /internal/commoncrawl/ingest {campaign_id, domain, max_urls?, max_indexes?}`
  — validates the campaign (404 otherwise), runs `Discover` with the shared `fetchBytes`, and
  enqueues via the shared `enqueueURLs` path. Counter `crawler_commoncrawl_urls_total`.

**Why**
- Maximum recall is the north star, and Common Crawl is the highest-leverage way to widen the mouth
  of the funnel: instead of waiting to discover a site link-by-link (or even from its sitemap), CC
  already holds a large sample of its URLs, free. This is the docs/04 §7 "free substitute for
  commercial search APIs" made real — bulk discovery input to the existing frontier.

**Verification** (golang:1.25-alpine container; `go mod tidy` at build; minimal `go.mod`/no `go.sum`
restored after):
- `go build ./...` clean; `go vet ./...` clean.
- `go test ./internal/commoncrawl/... ./internal/api/...` → both **ok**. New `commoncrawl_test.go`
  (8): parse collinfo; CDX parse skipping junk + non-http; `Discover` resolves collinfo and queries
  the newest crawl; **builds the correct `url=domain%2F%2A&output=json&limit=` query**; multi-index
  cross-crawl dedupe + `MaxURLs` cap; explicit `IndexURL` skips collinfo; empty-domain error;
  collinfo-fetch-error fatal. Existing `api` tests still pass.
- **Verified live** (2026-08-19): `POST /internal/commoncrawl/ingest {domain:"example.com"}` against
  the real CC `collinfo.json`+CDX index → `{discovered:2, enqueued:2}`.

---

## 2026-08-19 — Phase 4: RSS/Atom feed seed discovery (breadth + freshness)

**What**
- New `crawler/internal/feeds` package — discovers seed URLs from **RSS 2.0, Atom, and RSS 1.0/RDF**
  feeds (docs/04 §7), a free/open breadth-and-freshness source most sites publish. `Parse(data)`
  decodes all three shapes with one `encoding/xml` struct (matching `channel>item`, `feed>entry`,
  and top-level RDF `<item>` by local-name, namespace-agnostic), is gzip-aware, and returns
  de-duplicated item URLs in document order. A single `link` struct captures both an RSS
  `<link>URL</link>` (char-data) and an Atom `<link href=… rel=… type=…/>` (attributes); `bestLink`
  prefers the Atom `alternate`/HTML link, falls back to the first href, else the RSS link text.
- Control endpoint `POST /internal/feeds/ingest {campaign_id, url, max_urls?}` — validates the
  campaign (404 otherwise), fetches (200-only via the shared `fetchBytes` helper), parses, caps to
  `max_urls`, and enqueues via the same `enqueueURLs` path as sitemaps/seeds. Counter
  `crawler_feed_urls_total`. Refactored the sitemap handler's inline fetch into the shared
  `fetchBytes` method (used by both discovery endpoints).

**Why**
- Same north-star rationale as sitemaps (breadth = recall) with a freshness angle: a feed lists a
  site's newest items directly, so ingesting it captures new content quickly without waiting for
  link-following. Free/open, no paid dependency (rule 2). One-shot ingest for now; a recurring feed
  poll (like the social freshness scheduler) is a natural follow-up.

**Verification** (golang:1.25-alpine container; `go mod tidy` at build; minimal `go.mod`/no `go.sum`
restored after):
- `go build ./...` clean; `go vet ./...` clean.
- `go test ./internal/feeds/... ./internal/api/... ./internal/sitemap/...` → all **ok**. New
  `feeds_test.go` (8): RSS 2.0 with cross-item dedupe, Atom preferring the alternate/HTML link over
  self, RSS 1.0/RDF top-level items, gzip round-trip, malformed-XML error, empty feed → no URLs,
  and `bestLink` fallback to a non-alternate href. Existing `api`/`sitemap` tests still pass.
- **Verified live** (2026-08-19): `POST /internal/feeds/ingest` on the BBC News RSS →
  `{discovered:10, enqueued:10}`.

---

## 2026-08-19 — Phase 4: sitemap-based seed discovery (bulk breadth)

**What**
- New `crawler/internal/sitemap` package — discovers seed URLs from XML sitemaps and sitemap
  indexes, the highest-leverage breadth source on the open web (one document can list every URL a
  site wants crawled). Two pieces, both offline-testable:
  - `Parse(data)` — decodes a `<urlset>` (page URLs) or `<sitemapindex>` (child sitemaps) via
    `encoding/xml`, matching child elements by local-name so the sitemap XML namespace is a
    non-issue; **transparently gunzips** gzip payloads (`.xml.gz` is common); trims/drops empty locs.
  - `Discover(ctx, rootURL, fetch, limits)` — fetches the root, follows **one level** of
    sitemap-index nesting, de-dupes, and enforces `MaxURLs`/`MaxSitemaps` caps so a huge or hostile
    index can't fan out or exhaust memory. `fetch` is an injected `FetchFunc`, so the recursion is
    unit-tested with a fake (no network). Root fetch/parse failure is fatal; a bad **child** sitemap
    is skipped (best-effort, recall-first — one broken shard shouldn't lose the rest).
- Control endpoint `POST /internal/sitemap/ingest {campaign_id, url, max_urls?}` (`internal/api`):
  validates the campaign exists (404 otherwise), runs `Discover` with the crawler's HTTP fetcher
  (200-only, 20 MiB cap, 20 s timeout), and enqueues the discovered URLs at depth 0 through the
  **same** canonicalize→`EnsureSource`→`AddURL` path as campaign seeds (frontier unique constraint
  dedupes). Refactored the existing `seed()` to share a new `enqueueURLs` helper. Returns
  `{discovered, enqueued}`; new counter `crawler_sitemap_urls_total`. `Server` now holds a fetcher
  (default-initialized in `NewServer`, so the signature and existing callers/tests are unchanged).

**Why**
- Maximum recall is the north star (CLAUDE.md), and breadth of the frontier is the biggest lever:
  link-following only reaches pages already linked from crawled pages, while a sitemap enumerates a
  site's canonical set directly — thousands of URLs in one fetch. Sitemaps are a free/open source
  (rule 2). This is bulk discovery input to the existing frontier, not a new pipeline.

**Verification** (golang:1.25-alpine container; `go mod tidy` at build per the Dockerfile — the repo
keeps a minimal `go.mod` and no committed `go.sum`, restored after verifying):
- `go build ./...` clean; `go vet ./...` clean.
- `go test ./internal/sitemap/... ./internal/api/...` → both **ok**. New `sitemap_test.go` (10):
  parse urlset (trim + drop empty loc), parse index, **gzip** round-trip, malformed-XML error;
  `Discover` flat, index-follows-children-with-cross-child-dedup, root-fetch-error-fatal,
  bad-child-skipped, `MaxURLs` cap, `MaxSitemaps` cap (fetch-count bounded). Existing `internal/api`
  tests still pass (unchanged `NewServer` signature).
- **Verified live** (2026-08-19): `POST /internal/sitemap/ingest` on `cloudflare.com` and
  `wordpress.org` sitemaps → `{discovered:15, enqueued:15}` each; a missing sitemap returned a clean
  502 with the upstream 404 surfaced.

---

## 2026-08-19 — Phase 4: sentence-aware chunking (no mid-sentence/mid-word splits)

**What**
- `ai/app/chunking.py` — the hard-split path for over-target paragraphs previously cut at an
  arbitrary char offset (`min(i+target, e)`), i.e. mid-sentence and often mid-word, producing chunks
  that embed and match poorly. It now snaps each window end to the **nearest sentence boundary**
  (`. ! ?` + optional closing quote/bracket at a whitespace/end boundary), falling back to the
  nearest whitespace, and only hard-cutting when a run has no break at all (e.g. a long URL/token).
  New `_good_break(text, lo, hi)` helper; the split searches the back half of each window
  (`lo = i + max(min_chars, target//2)`) so a snapped piece is never tiny and stays within target.
- Behavior otherwise preserved: paragraph packing to `chunk_target_chars`, `chunk_overlap_chars`
  overlap between chunks, tiny-tail merge (< `chunk_min_chars`) into the previous chunk, and char
  offsets into the original text. `chunk_text` signature unchanged (indexer needs no change).
- docs/05 §7 updated with the implementation note.

**Why**
- Chunking is "a major recall/precision lever" (docs/05 §7), and the doc already specified "never
  split mid-sentence" — the code just wasn't honoring it. A chunk that ends mid-sentence gives the
  embedding model a truncated thought and splits a fact across a boundary, hurting both vector and
  lexical matching. Clean sentence boundaries improve match quality across every query (recall-first:
  better-formed chunks surface more of the right content).

**Verification**
- **Full offline suite: 75 tests pass** (`ai/tests/`, local `pytest`, no network). New
  `test_chunking.py` (7): empty/whitespace → none; short text → one chunk covering the span; two
  small paragraphs pack into one; a long multi-sentence paragraph splits into ≥2 chunks that each
  (but the last) **end on a sentence terminator, never mid-word**, cover the whole text with
  overlap, and stay within target(+tail slack); an unbroken 500-char token still splits, progresses,
  and covers fully (hard-cut fallback); and offsets map back to the source. Prior tests (68) green.
- Import smoke: `app.indexer` (the sole `chunk_text` caller) imports unchanged.

---

## 2026-08-19 — Phase 4: query intent classification (intent → freshness)

**What**
- New `ai/app/intent.py` — a lightweight, rule-based (no model, no network) query classifier, the
  last open piece of query understanding (docs/07 §2). `classify(query)` returns one of
  `news_fresh | broad_research | entity_lookup | navigational | factual` from ordered regex/heuristic
  signals (precedence: navigational URL/site → recency → breadth → named-entity → factual). Recency
  covers explicit words ("latest", "breaking", "this week") *and* a current/next-year mention.
- `resolve_freshness(requested, intent)` maps intent onto a freshness mode **only when the caller
  left it "auto"**: a `news_fresh` query upgrades to `"fresh"`; an explicit `fresh`/`any` always wins.
- Wired into `retrieval.retrieve()` as step 0: classify the query, resolve the effective freshness,
  and feed that into the existing freshness blend. `RetrievalResult` now carries `intent` +
  `freshness`; both are echoed on `/v1/retrieve` and in `/v1/search` payloads/SSE `meta` for
  observability. Intent only nudges ranking — it never filters or drops candidates (recall-first).

**Why**
- Completes query understanding and makes recency automatic where it matters: "latest X" now gets
  fresh ranking without the user setting `freshness=fresh`, while non-time-sensitive queries are
  unaffected. Rule-based (not an LLM call) keeps it zero-latency and dependency-free on the hot path;
  the extra labels are surfaced now for future hooks (filter derivation, per-intent k/rerank tuning).

**Verification**
- **Full offline suite: 68 tests pass** (`ai/tests/`, local `pytest`, no network). New
  `test_intent.py` (17): news/recency phrasings + current/next-year → `news_fresh`; an old year does
  not; navigational (site/URL/login), broad-research, entity-lookup ("Ada Lovelace", "who is …"),
  factual default, empty→factual; precedence (navigational beats news, news beats broad); and
  `resolve_freshness` (auto upgrades only for news; explicit `any`/`fresh` respected). Prior
  understand/fusion/freshness/dedup tests (51) still green.
- Import/route smoke: `app.main` builds; a `news_fresh` query resolves `auto`→`fresh` end to end.
  Live vs a running stack deferred (pure classification + ordering logic, unit-covered).

---

## 2026-08-19 — Phase 4: query-time near-duplicate dedup

**What**
- New `ai/app/dedup.py` — near-duplicate detection for result assembly. The old `_assemble` deduped
  only on an exact `text[:200]` prefix match, which misses the two common real cases: the same
  passage re-crawled with different whitespace/case, and chunks that overlap ~95%. Both waste scarce
  `max_sources` slots on redundant content (fewer distinct facts reach synthesis, duplicate citations).
  - `normalize` (lowercase + collapse whitespace), `shingles` (word k-grams; texts shorter than k
    fall back to a single whole-string shingle → exact-match only), `jaccard`, and a `DedupIndex`
    with **separate `is_duplicate` (query) and `add` (commit)** so a candidate can be tested,
    skipped for another reason (the domain cap), and reconsidered later without polluting the index.
- `retrieval._assemble` now uses `DedupIndex(k, threshold)` in place of the exact-prefix `seen_text`
  set — both the diversified pass and the backfill pass. Behavior preserved: only kept chunks enter
  the index; domain-capped candidates remain eligible for backfill; the budget still fills.
- Config knobs `dedup_shingle_k` (5) + `dedup_jaccard_threshold` (0.8).

**Why**
- Phase 4 "query-time dedupe/diversification tuning." Recall-first means recall of *distinct*
  information: collapsing near-identical passages frees slots for genuinely different sources, which
  improves both the synthesis context and citation quality. Kept deliberately conservative (high
  threshold, short-snippet exact-only fallback, never returns fewer results) so it can't drop a
  distinct-but-brief fact.

**Verification**
- **Full offline suite: 51 tests pass** (`ai/tests/`, local `pytest`, no network). New
  `test_dedup.py` (13): normalize case/whitespace, shingle k-grams + short-text fallback + empty,
  Jaccard identical/disjoint/empty; `DedupIndex` — exact + case/whitespace variant flagged,
  **one-word change in a realistic chunk-length passage** flagged, distinct text not flagged,
  `is_duplicate` doesn't mutate, empty-text handling; and two `_assemble` integration tests
  (near-dup skipped and a distinct doc backfilled to budget; distinct texts all kept). Prior
  understand/fusion/freshness tests (38) still green.
- **Test-fixture bug found:** the first draft used a ~35-word passage, where a single word edit
  swings shingle Jaccard below 0.8 (false negative). Real chunks are ~250+ words, where a few-word
  edit stays >0.9 — the regime the threshold targets — so the fixture was lengthened to ~130 words
  to model reality. No code change needed; the threshold behaves correctly at realistic lengths.
- Import smoke: `app.main` builds. Live end-to-end vs a running stack deferred (pure assembly logic,
  unit-covered).

---

## 2026-08-19 — Phase 4: freshness-aware ranking (wire the inert `freshness` option)

**What**
- New `ai/app/freshness.py` — blends a recency signal into the retrieval ordering. The
  `freshness=auto|fresh|any` request option had been accepted by the API since Phase 2 but was
  **never used**; retrieval ignored it. Now it drives ranking:
  - `recency_weight(published_at, now, half_life_days, undated_weight)` → a [0,1] score that is
    1.0 for brand-new content and halves every `half_life_days`. Tolerant ISO parsing (trailing
    `Z`, bare `YYYY-MM-DD`, naive→UTC). **Undated/unparseable docs return a neutral weight**
    (default 0.5) — a missing date must never bury an otherwise-strong match (recall-first).
  - `blend_weight(mode)` → `any`=0 (pure relevance), `auto`=0.15 (gentle nudge), `fresh`=0.45
    (strong recency pull).
  - `apply_freshness(cands, mode, …)` sets `Candidate.final_score = (1-w)·norm_relevance + w·recency`,
    where relevance is min-max normalized within the shortlist (scale-independent — rerank logits
    and RRF sums live on different scales). It only **reorders**, never drops candidates.
- `retrieval.py`: `Candidate` gains `final_score` + an `order_score` property (freshness-blended
  when set, else the plain relevance `score`); `_assemble` now sorts by `order_score`. `retrieve()`
  gains a `freshness` param and applies the blend after rerank, before assembly. The relevance
  `score` used for citations/confidence/UI is unchanged — freshness affects ordering only.
- API: `RetrieveRequest.freshness` (parity with `SearchOptions.freshness`); both `/v1/retrieve` and
  `/v1/search` pass it through. Config knobs (`freshness_half_life_days`, `freshness_undated_weight`,
  `freshness_auto_weight`, `freshness_fresh_weight`) + `.env.example`.

**Why**
- Phase 4 "freshness-aware ranking" and the second half of "intent-based freshness boosting". For
  time-sensitive questions, a newer document should outrank an equally-relevant stale one — but the
  product bias is recall, so freshness is a *reordering nudge* that never removes results and never
  penalizes undated content. Closes a live-but-inert API contract (the option was advertised).

**Verification**
- **Full offline suite: 38 tests pass** (`ai/tests/`, local `pytest`, no network). New
  `test_freshness.py` (12): recency decay (fresh≈1, half-life→0.5, 2×→0.25, future clamped),
  undated→neutral, `Z`/bare-date parsing; `blend_weight` mode mapping; and `apply_freshness` —
  `any` is a no-op (`final_score` stays None), `fresh` promotes a fresher near-relevance doc while
  `auto` keeps the stronger stale one, undated beats dated-old at equal relevance, single-candidate
  and empty-list edge cases. Existing understand/fusion tests (26) still green.
- Import/route smoke: `app.main` builds; `Candidate.order_score` falls back to `score` when no
  freshness applied. **Deferred:** live end-to-end vs a running stack (GPU stack not up this
  iteration; the change is pure ordering logic and unit-covered).

---

## 2026-08-19 — Phase 4: query understanding (LLM expansion + decomposition)

**What**
- New `ai/app/understand.py` — the query-understanding step (docs/07 §2) the pipeline was missing;
  previously retrieval ran the raw query only. Turns one question into a `QueryPlan`: the normalized
  original plus LLM-generated **paraphrases** and **decomposed sub-questions**. Pure and
  dependency-injected — the LLM is passed in as an async callable, so parsing/cleaning/planning are
  unit-testable offline with no model or network:
  - `normalize_query` (trim + collapse whitespace; conservative — never rewrites the user's terms).
  - `parse_expansions` (tolerant: prefers the JSON array the prompt asks for — even wrapped in
    prose or as objects — and falls back to line-splitting; strips numbering/bullets/quotes).
  - `clean_expansions` (case-insensitive dedupe, drops the original, drops empty/over-long, caps count).
  - `plan_query` (async): builds the plan, and returns **original-only** whenever expansion is off,
    no LLM is wired, the query is empty, or the model call throws — a search never fails on it.
- `clients.llm_complete` — one-shot (non-streaming) Ollama `/api/chat` used off the answer hot path.
- `retrieval.py` — generalized fusion: `_rrf` → **`_rrf_runs`**, which RRF-fuses one-or-many
  `(lexical, vector)` runs; a chunk surfaced by several phrasings sums score from each (agreement
  across paraphrases/sub-questions is a strong relevance signal). `_gather_runs` fires every planned
  query's hybrid retrieval concurrently (`asyncio.gather`). `retrieve()` now plans → gathers → fuses,
  **reranks against the user's original question** (not an expansion), and carries the `QueryPlan`
  on `RetrievalResult`. New `expand` param (defaults to `settings.query_expansion`).
- API: `SearchOptions.expand` / `RetrieveRequest.expand` (default on) toggle it per request;
  `/v1/retrieve` returns `expansions`, `/v1/search` echoes them in the payload/SSE `meta`.
- Config knobs (`query_expansion`, `max_query_expansions`, `expansion_temperature`,
  `expansion_timeout_s`) + `.env.example`. New `ai/pytest.ini` (`pythonpath=.`, `testpaths=tests`).

**Why**
- The north star is **maximum recall** (CLAUDE.md), and decomposition is precisely what makes
  "find everything about X" actually find everything — one literal query misses documents that a
  paraphrase or sub-question would surface. This was the top open backlog item and the first
  query-understanding piece of Phase 4. Kept strictly off-path/degradable so it only ever *adds*
  recall, never blocks a search.

**Verification**
- **26 offline unit tests pass** (`ai/tests/`, no network/LLM) via local `pytest`:
  `test_understand.py` (17) — normalize; JSON-array / prose-wrapped / object / line-fallback /
  numbered+bulleted+quoted parsing; dedupe-vs-original, count cap, empty/over-long drop;
  `QueryPlan.queries`/`expanded`; and `plan_query` across expand-on, model-echoes-original,
  **LLM-error fallback**, expand-disabled, no-LLM, and empty-query. `test_fusion.py` (9) — single-run
  rank/score bookkeeping, **multi-run boost of a chunk shared across queries**, best-rank kept across
  runs, vector-only payload, missing-chunk_id skip, empty runs. Found & fixed one bug during testing:
  `_strip_line` stripped wrapping quotes before removing numbering, leaving `1. "foo"` quoted.
- Import/route smoke: `app.main` builds; `/v1/retrieve` + `/v1/search` present and unchanged in shape.
- **Deferred:** live end-to-end against a running GPU stack (Ollama/TEI/Qdrant/OpenSearch) — not
  brought up this iteration; the feature degrades to original-only when the LLM is unavailable, and
  the offline suite covers the planning/fusion/fallback logic.

---

## 2026-08-05 — Phase 3: proxy pool (last anti-detection task → Phase 3 complete)

**What**
- New `browser-worker/src/proxies.ts` — the egress-distribution half of the docs/04 §5/§6
  anti-detection stack, the last open Phase 3 item. The pool is fed a list of the owner's **own**
  self-run proxies via config (`RENDER_PROXIES` inline and/or `RENDER_PROXY_FILE`) — **no paid
  provider** (CLAUDE.md rule 2). `parseProxies`/`parseProxyLine` accept `scheme://[user:pass@]host:port`
  (bare `host:port` ⇒ `http://`), pull credentials out into Playwright's separate `username`/`password`
  (they never ride in `server`), skip `#` comments/blank/junk lines, and dedupe by server.
- **Coherence with sessions is the design constraint:** a warm cookie jar + pinned fingerprint that
  suddenly speaks from a new IP is itself a bot tell. So a proxy is **pinned per host** (the same
  boundary `sessions.ts` uses) — a host keeps one egress IP while that proxy is healthy. Health is
  tracked per proxy: consecutive failures push it into a **capped exponential cooldown**
  (`RENDER_PROXY_COOLDOWN_MS` base, doubling, capped at `RENDER_PROXY_MAX_COOLDOWN_MS`); a success
  clears the streak + cooldown. Selection prefers the **least-loaded healthy** proxy (round-robin
  tiebreak) so assignments spread evenly; a host pinned to a proxy that enters cooldown is repinned
  to a healthy one, and if *every* proxy is benched the pool still returns the one recovering
  soonest rather than failing the render (recall-first). Empty pool ⇒ direct connection (unchanged).
- Wiring: `config.ts` parses the list (inline + optional file, synchronous, missing file non-fatal)
  and adds `proxies`/`proxyCooldownMs`/`proxyMaxCooldownMs`. `renderer.ts` builds the pool, launches
  Chromium with the **`per-context` proxy sentinel** (Chromium only honours a per-context proxy when
  the browser is launched with a proxy reserved), acquires a proxy per host keyed by
  `SessionStore.keyFor(url)`, passes `proxy` to `newContext`, and reports the outcome
  (success on a completed navigation, failure on a thrown nav/timeout → cooldown). New metrics
  `browserworker_proxy_{selected,failed}_total` + `browserworker_proxy_{healthy,pool_size}` gauges.
- `.env.example` documents the four `RENDER_PROXY*` knobs; `deploy/docker-compose.yml` notes the
  env-driven list and a commented `config/proxies.txt` mount for the file form.

**Why**
- Closes the anti-detection stack and the last Phase 3 exit criterion. A single machine's IP is a
  fingerprint every target shares across all renders; distributing egress across the owner's own
  proxies — while keeping each host's IP stable to stay coherent with its warm session — reduces
  IP-based blocks/geo-walls (recall-first, per the north star) without any paid dependency.

**Verification**
- `npm run typecheck` clean; production `npm run build` clean → `dist/proxies.js` ships (test excluded).
- `npm test` green — **21/21** (10 new proxy tests + the prior 11): line/list parsing (creds split
  out, scheme defaults, junk/comment drop, dedupe), empty pool ⇒ no lease, per-host pin stability
  across visits, least-loaded spread across hosts, failure → cooldown → recovery, exponential backoff
  capped, success clears the streak, reassign-off-a-benched-proxy, and the all-benched best-effort
  fallback.
- **Real-Chromium end-to-end:** drove the *actual* code path (`loadConfig` parses `RENDER_PROXIES`
  → `ProxyPool` → `Renderer` launches with the `per-context` sentinel → `newContext({ proxy })`)
  against a local forward proxy + a LAN-bound target (Chromium bypasses the proxy for loopback, so
  the target used the host's LAN IP). The proxy **recorded the target hit** (traffic provably
  transited the pool, not a direct connection), the rendered DOM came back (status 200, marker text
  present), and `browserworker_proxy_{selected=1,healthy=1,pool_size=1}` incremented. Scratch
  harness removed afterward.
- `docker compose --profile app config` valid with the new env/mount.

**Status:** **Phase 3 complete.** JS render+index, ≥3 social adapters with health monitoring,
freshness cadence, and the full anti-detection stack (fingerprints + pacing + sessions + proxy pool)
are all done and verified. Next: Phase 4 — Scale & quality. Credential-walled social adapters
(Reddit/Telegram) remain deferred on owner-provided creds.

---

## 2026-07-09 — Phase 3: browser fingerprint hardening + human-like pacing

**What**
- New `browser-worker/src/fingerprint.ts` — the fingerprint half of the anti-detection stack
  (docs/04 §5). It replaces the renderer's ad-hoc, *internally inconsistent* rotation (a spoofed
  `Chrome/126` UA with no client hints, missing `window.chrome`, empty plugins, a SwiftShader WebGL
  renderer) with one **coherent identity per render**. `buildIdentity()` (pure, no Playwright import)
  picks an OS profile (Windows/macOS/Linux), a region (locale + timezone + Accept-Language paired:
  en-US→America/\*, en-GB→Europe/London), a Chrome major version with its real GREASE brand, viewport,
  and hardware, then derives the UA string, the matching `Sec-CH-UA`/`-platform`/`-mobile` headers,
  and a `stealthInit` script that patches every remaining tell to agree with that identity:
  `navigator.webdriver`, `languages`, `platform`, `hardwareConcurrency`, `deviceMemory`,
  `navigator.userAgentData` (+ `getHighEntropyValues`), `window.chrome`, non-empty `plugins`, WebGL
  `UNMASKED_VENDOR/RENDERER` (masks the software rasterizer), and the `permissions.query`↔
  `Notification.permission` contradiction.
- `renderer.ts` now builds one identity per job → `contextOptions(id)` for `newContext` +
  `addInitScript(stealthInit, stealthPayload(id))`. Added **human-like pacing** (gated by
  `RENDER_HUMANIZE`, default on): a jittered post-load pause, a couple of stepped mouse moves, and
  jittered scroll-step dwell (replacing the fixed 300ms cadence) so a render isn't a dead-still,
  zero-interaction fetch with an identical timing signature.
- Test tooling: `node --test` unit tests (`fingerprint.test.ts`) driven by an injectable RNG, a
  `test` npm script + `tsconfig.test.json` (production build excludes `*.test.ts`; `dist-test/`
  git/docker-ignored). New `RENDER_HUMANIZE` in `.env.example`.

**Why**
- The last open Phase 3 item is the anti-detection stack (fingerprints, proxies, sessions, pacing).
  The prior lightweight rotation actually *created* headless tells: the classic giveaway is a
  **mismatch** (UA says Chrome 126 but Chromium sends its own real `Sec-CH-UA`; UA says Windows but
  `navigator.platform` reads the container's Linux; WebGL reads SwiftShader), not any single value.
  Deriving every surface from one identity makes the fingerprint self-consistent, which is what
  matters for recall against JS/social targets that gate on bot detection.

**Verification**
- `npm run typecheck` clean and all 4 unit tests green in a `node:20` container (identity
  consistency across 500 random draws: UA↔client-hint version, `navigator.platform`↔UA token↔
  CH-Platform, locale↔timezone, no software-rasterizer WebGL; client-hint mapping; GREASE parsing;
  RNG determinism). Production `npm run build` clean — `dist/` ships `fingerprint.js`, not the test.
- **Real-browser check** (Playwright v1.49.1 image, live Chromium): applied a built identity and read
  back the values a detector probes — `webdriver:false`, `platform:Win32` matching a Windows UA,
  `languages:[en-GB,en]`, `plugins:3`, `window.chrome` present, `hardwareConcurrency/deviceMemory`
  as chosen, `userAgentData.brands` with the GREASE brand filtered + `getHighEntropyValues`
  returning `platform:Windows`/`uaFullVersion:124.0.0.0`, WebGL vendor/renderer = Intel (not
  SwiftShader), notifications `prompt`. Confirmed the **outgoing request to example.com** carried the
  spoofed `user-agent` + `sec-ch-ua` + `sec-ch-ua-platform` — no field contradicted another.

---

## 2026-07-09 — Phase 3: freshness cadence for tracked social entities

**What**
- New durable `tracked_entities` registry (`db/migrations/0004_tracked_entities.sql` +
  `store.EnsureTrackedEntities` idempotent DDL): one row per `(adapter, seed)` carrying
  `cadence_seconds`, a per-entity `max_pages` cap, `enabled`, a self-advancing `next_due_at`
  timer, and `last_result`/`last_error`/`runs`/`last_ingested_at` for freshness-lag visibility.
  Store methods: `UpsertTrackedEntity`, `ListTrackedEntities`, `DeleteTrackedEntity`,
  `ClaimDueTracked` (advances `next_due_at` by one cadence as it claims under `FOR UPDATE SKIP
  LOCKED` — a slow run never double-schedules), `RecordTrackedRun`.
- New `crawler/internal/freshness` scheduler — a ticker loop that each tick claims every due
  entity and re-runs the **shared** social ingester (`api.NewSocialSink` exported so scheduled and
  manual `POST /internal/social/ingest` runs land through the identical store+blob path). One
  entity's failure never stops the rest; each run's summary/error is recorded. Wired in `main.go`
  (off when `CRAWLER_FRESHNESS_TICK_S=0`); counted by `crawler_freshness_runs_total{adapter,result}`.
- New control endpoints `GET·POST·DELETE /internal/social/tracked` (register/list/remove) with
  validation: unknown adapter and sub-10s cadence are rejected up front. Config knobs
  `CRAWLER_FRESHNESS_TICK_S|BATCH`, `CRAWLER_SOCIAL_PACE_MS` (`.env.example`, compose `env_file`).

**Why**
- Last unmet Phase 3 exit criterion ("freshness cadence for tracked entities", docs/08 §7). Social
  content is time-sensitive; a watched hashtag/profile/instance must be re-ingested on a short
  per-entity cadence, not just on a one-shot operator trigger. Content-hash dedupe already makes
  re-runs idempotent, so the scheduler only needs to re-drive the existing ingest path — no second,
  drifting copy of the pipeline, and only genuinely new posts land.

**Verification**
- `go build ./... && go vet ./...` clean; unit tests pass (`internal/freshness` double-schedule
  guard + failure-isolation; `internal/api`, `internal/social` unaffected) in a `golang:1.25`
  container via the same `go mod tidy` path as the Dockerfile.
- Store integration test (`tracked_integration_test.go`, `-tags integration`) green against the
  **live** Postgres: upsert round-trip + idempotent re-upsert, due-claim returns both entities and
  advances `next_due_at`, immediate re-claim returns nothing (double-schedule guard), run recording,
  delete/second-delete.
- End-to-end against the running stack (rebuilt crawler): scheduler logged `freshness scheduler
  started (tick 30s, batch 16)`. Registered `hackernews/top` (600s) and a `mastodon` hashtag
  (900s); within one tick the scheduler ran both — `hackernews` landed **30 inserted**, both
  `next_due_at` advanced by their cadence, `runs=1`, `crawler_freshness_runs_total` incremented.
  Validation verified live: unknown adapter → 400, sub-10s cadence → 400, DELETE → `removed`, second
  DELETE → 404. Test entities removed afterward (registry back to `count:0`).

---

## 2026-07-09 — Phase 3: Playwright browser-worker (render queue consumer)

**What**
- New `browser-worker/` service — the headless-browser render pool that consumes the crawler's
  `render_queue` over the control API. A single long-lived Chromium (`src/renderer.ts`) serves a
  bounded pool of concurrent renders; the worker loop (`src/worker.ts`) does `claim → render →
  ingest`, forever: it leases a batch from `POST /internal/render/claim`, renders each job in a
  fresh isolated browser context, and POSTs the resolved DOM to `POST /internal/render/ingest`
  (which runs the crawler's shared extract→chunk→index path). Empty/errored renders report a
  retryable failure via `POST /internal/render/complete` so the crawler's cap/reaper reschedules.
- Renderer basics: per-job fresh context (isolated cookies/storage), rotated
  viewport/locale/timezone/UA pools, `navigator.webdriver` masking, `--disable-blink-features=
  AutomationControlled`, image/font/media blocking, capped auto-scroll to fire lazy/infinite
  loaders, then a settle delay for late hydration. Deliberately lightweight (docs/04 §5) — the
  full fingerprint/proxy stack is a later Phase 3 task.
- `src/crawlerClient.ts` mirrors the crawler contracts (`RenderJob`/`RenderItem`, the 422 empty
  signal, the ingest result shape); `src/metrics.ts` exposes dependency-free Prometheus
  `browserworker_*` counters/gauges (claimed/indexed/duplicate/empty/failed/inflight/claim_errors)
  on a `/metrics` + `/healthz` HTTP server.
- Wiring: `deploy/docker-compose.yml` adds the `browser-worker` service to the `app` profile
  (Playwright base image, `CRAWLER_URL=http://crawler:8090`, health port 8091);
  `deploy/prometheus.yml` scrapes it; `.env.example` documents the `RENDER_*` knobs.

**Why**
- This was the last open piece of Phase 3's browser half: the queue + ingest boundary existed but
  had no process actually driving a real browser. Keeping the worker a thin renderer (all
  extraction/indexing stays in the crawler) avoids a second, drifting copy of the pipeline.
  Bias-to-recall: JS-locked content the static fetch can't see now reaches the index.

**Verification**
- `npm run build` (tsc, strict) clean in a `node:20-alpine` container → `dist/*.js` emitted.
- `docker compose --profile app config` valid; `browser-worker` present in the `app` profile.
- Contracts cross-checked against `crawler/internal/api` (claim/ingest/complete) and
  `store.RenderItem` — fields, the 422 empty-render path, and the ingest result shape all match.
- Live against the running stack: `browser-worker` container up, `/healthz` ok. It claimed and
  rendered real pages — `https://en.wikipedia.org/wiki/Headless_browser` → `rendered → indexed`
  (`text_len=7286`), example.com/.net → `duplicate`. Postgres confirms the row: `id=56`,
  `meta.rendered_by=browser`, `text_len=7286`. Transient postgres restart mid-run surfaced as
  `claim failed` warnings and self-recovered (the queue's lease/retry owns correctness). Metrics:
  `browserworker_{claimed=3,rendered_indexed=1,rendered_duplicate=2}`. After a Prometheus config
  reload the `browser-worker` scrape target reads `health=up`, `up=1`.

---

## 2026-07-09 — Phase 3: render-ingest boundary (browser DOM → RAG pipeline)

**What**
- New `POST /internal/render/ingest` (`crawler/internal/api/renderingest.go`) — the crawler-owned
  boundary a (separate-language, docs/02) Playwright worker POSTs to after rendering a claimed job:
  `{id, url, final_url?, status?, html}`. The handler runs the rendered DOM through the **same**
  `extract.FromHTML → blob.PutRaw/PutText → store.InsertDocument` path as a static fetch (so
  rendered content is chunked/embedded identically), stamps `meta.rendered_by="browser"`, and marks
  the `render_queue` job RENDERED. Deliberately does **not** re-run the escalation gate or link
  discovery (the page is already rendered; outlinks belong to the static frontier that fed it).
- Empty renders (no extractable text) return `422` and do **not** mark the job RENDERED, so the
  worker retries rather than the queue silently swallowing a failed render.
- New metric `crawler_render_ingested_total{result=indexed|duplicate|empty}`; reuses
  `crawler_render_queue_completed_total{rendered}` + `crawler_documents_total`.
- Previously `render/complete` only flipped job state — a claimed render had nowhere to land its
  content. This closes that loop; `render/complete` now covers only the failure/no-content path.

**Why**
- Phase 3's browser half needs the rendered DOM to actually reach the index, not just a state flip.
  Keeping extraction/indexing in one place (Go) means the browser worker stays a thin renderer
  instead of a second, drifting copy of the pipeline — the same rationale as the `render_queue`
  producer→consumer split. Bias-to-recall: JS-locked content the static fetch missed now lands.

**Verification**
- `go build ./...`, `go vet ./...`, `go test ./...` clean in a `golang:1.25-alpine` container.
- Live end-to-end against the running stack (crawler rebuilt): created a `render_js=always` campaign
  → static crawl escalated and enqueued example.com (`render_queue PENDING:1`); `claim {n:5}` leased
  job id=7 (`RENDERING`); `POST /internal/render/ingest` with a rendered SPA DOM →
  `{inserted:true, state:RENDERED, lang:en, text_len:475}` and queue → `RENDERED:1`; re-POST →
  `{inserted:false}` (exact-dedup duplicate); empty-DOM POST → `422` (job untouched). Metrics:
  `render_ingested{indexed=1,duplicate=1,empty=1}`, `render_queue_completed{rendered=2}`,
  `documents_total +1`. Postgres confirmed the row: `id=50 url=https://example.com/ status=200`
  `content_type=text/html lang=en meta.rendered_by=browser text_len=475` with a blob key set.

---

## 2026-07-09 — Phase 3: durable browser render queue (escalation gate → pool hand-off)

**What**
- New `render_queue` table (`db/migrations/0003_render_queue.sql`) — a frontier-shaped work queue
  for the headless-browser path: `PENDING → RENDERING → RENDERED | FAILED`, with `priority`,
  `attempts`, `next_attempt`, a `claimed_at` lease, JSONB escalation `reasons`, and
  `UNIQUE (url_hash, campaign_id)`. The escalation gate previously only stamped
  `documents.meta.needs_render`, which is an observability flag with no claim/lease/retry semantics;
  this is the actual queue the (separate-language) Playwright pool consumes.
- New `crawler/internal/store/render.go` — `EnsureRenderQueue` (idempotent DDL run at startup so the
  feature works on volumes predating the migration), `EnqueueRender` (dedup via ON CONFLICT DO
  NOTHING), `ClaimNextRender` (atomic `FOR UPDATE SKIP LOCKED` claim of the top-N by priority),
  `MarkRendered`, `MarkRenderFailed` (retry→PENDING under the cap, else FAILED — mirrors the
  frontier), `RequeueStuckRendering` (reaper for crashed workers), and `RenderQueueStats`
  (per-campaign or global).
- Producer wiring: `crawl/scheduler.go` now enqueues escalated URLs (priority `1/(depth+1)`) and
  increments `crawler_render_queue_enqueued_total`. `main.go` calls `EnsureRenderQueue` at boot and
  the existing reaper now also requeues stuck `RENDERING` jobs.
- Consumer/observability API (`internal/api/api.go`): `GET /internal/render/queue[?campaign=…]`,
  `POST /internal/render/claim` (`{n?}` → leased batch), `POST /internal/render/complete`
  (`{id, ok, retry?}`). New metrics `crawler_render_queue_{enqueued,claimed,completed}_total`.
  `POST /internal/campaigns` now accepts `render_js` so the escalation policy is operator-settable.

**Why**
- Phase 3's browser half needs a durable producer→consumer boundary before the (expensive,
  separate-language — docs/02) Playwright worker exists. Building the queue first means the worker
  becomes a thin consumer of a proven, observable signal rather than a big-bang addition, and the
  crawler owns the state machine (claim/lease/retry/crash-recovery) in one place. Bias-to-recall:
  every escalated URL is durably captured for later rendering.

**Verification**
- `go build ./...`, `go vet ./...`, `go test ./...` all clean in a `golang:1.25-alpine` container
  (same toolchain as the crawler Dockerfile).
- New build-tagged integration test (`store/render_integration_test.go`, `-tags integration`) run
  against the **live Postgres**: enqueue + dedup, priority-ordered top-N claim, reasons round-trip,
  RENDERED/FAILED transitions, drained-queue stats → `PASS`.
- Live end-to-end against the running stack: rebuilt the crawler, then
  (a) **producer** — created a campaign with `render_js=always` seeded at `https://example.com/`;
  the crawl escalated (`crawler_render_escalations_total{reason="mode=always"} 1`) and enqueued
  (`crawler_render_queue_enqueued_total 1`, queue `PENDING:1` for the campaign);
  (b) **consumer** — `claim {n:2}` → `RENDERING:2`; `complete ok=true` → `RENDERED`; `complete
  ok=false` → retried back to `PENDING`; metrics `claimed_total 2`, `completed{rendered=1,failed=1}`.
- **Bug found & fixed during verification:** `ClaimNextRender` scanned the nullable `campaign_id`
  into `*int64` and errored on NULL rows (leaving them stuck in `RENDERING`); fixed with
  `COALESCE(campaign_id, 0)` and re-verified.

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

## 2026-07-09 — Phase 3: per-host session/cookie persistence (anti-detection)

**What**
- New `browser-worker/src/sessions.ts` — a `SessionStore` that gives the render pool coherent,
  reusable sessions instead of a cold fresh context per job:
  - **Per-host key** (`SessionStore.keyFor` = lowercased hostname). Coherent within a site,
    isolated across sites (sessions never bleed between targets).
  - **Pinned identity + persisted cookie jar.** Each host keeps ONE `Identity` (from
    `fingerprint.ts`) plus its accumulated Playwright `storageState` (cookies + localStorage).
    Return visits resume both — presenting a fresh fingerprint to a warm cookie jar is itself a
    tell, so identity is pinned to the jar.
  - **Disk-durable** via atomic write-then-rename to a mounted volume (`sessionsdata`), so
    warm/authenticated sessions survive worker restarts. Memory-only fallback when no dir is set;
    disk errors are non-fatal (logged, render continues).
  - **Per-key async lock** serializes same-host renders so two contexts can't race one cookie jar
    (also more human — one session, one activity stream); different hosts still render in parallel.
  - **TTL rotation** (`RENDER_SESSION_TTL_MS`, default 24h): past the TTL a host gets a new
    identity + empty jar, so no identity/jar pair becomes a permanent tracking signal.
- Wired into `renderer.ts`: seeds `newContext({ storageState })` from the session, persists the
  post-render jar back via `handle.release(state)`; failed renders release without persisting a
  half-baked jar. Gated by `RENDER_SESSIONS` (default on).
- Config knobs (`config.ts`) + `.env.example`; three Prometheus counters
  (`browserworker_session_{created,resumed,rotated}_total`) in `metrics.ts`.
- `deploy/docker-compose.yml`: `sessionsdata` volume mounted at `/data/sessions` on browser-worker.
- Fixed `npm test` to glob `dist-test/**/*.test.js` (it was running the whole dir, which booted
  `index.js`/the Worker and bound port 8091).

**Why**
- Advances the Phase 3 anti-detection stack: session/cookie reuse cuts re-auth/challenge frequency
  and is the substrate authenticated targets need (an injected login cookie lands in this jar and
  is reused). Recall-first: fewer challenges → more reachable content.

**Verification**
- `npm run typecheck` clean; **`npm test` green — 11/11** (7 new session tests: host keying,
  first-visit empty jar, resume-with-pinned-identity, cross-host isolation, disk round-trip across
  a fresh store, same-host serialization, TTL rotation).
- **End-to-end in a real Chromium**: a local server sets `sid=abc123` on the first render; the
  second render of the same host **replayed the cookie** (`hit=2 cookie=sid=abc123`) — proving the
  jar persisted to disk and re-seeded the new context. First render correctly sent no cookie.
- `docker compose --profile app config` valid with the new volume/mount.

**Status:** Phase 3 anti-detection: fingerprints + pacing + session/cookie persistence done.
**Last open Phase 3 task: proxy pool.**

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
