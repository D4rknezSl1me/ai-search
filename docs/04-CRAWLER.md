# 04 — Crawler Subsystem

The crawler is the recall engine. Its job: fetch as much relevant content as possible, quickly,
without getting blocked, and without losing state on crash.

## 1. Subcomponents

```
Seeds ─▶ Frontier ─▶ Scheduler ─▶ Fetch dispatcher ─┬─▶ Static fetchers (Go)
                                                     └─▶ Browser workers (Playwright)
                                                            │
Extractor ◀── raw content ◀───────────────────────────────┘
   │
   ├─▶ new links ─▶ Frontier (with dedupe)
   └─▶ Document ─▶ Blob store + "document ready" event
```

## 2. Frontier

The frontier is the set of URLs to crawl, with ordering.

- **Durable store:** PostgreSQL table `frontier_urls` (survives restarts).
- **Hot layer:** Redis sorted set keyed by priority for fast pop; Redis bloom filter for
  "already seen" URL dedupe (canonicalized URL hash).
- **URL canonicalization:** lowercase host, strip fragments, sort query params, remove tracking
  params, resolve relative → absolute. Dedupe on the canonical form's hash.
- **Priority score** (higher = sooner):
  `priority = w1·source_authority + w2·freshness_need + w3·(1/depth) + w4·value_estimate − w5·host_backpressure`.
  Weights configurable; tuned per campaign.
- **Politeness state:** per-host next-allowed-fetch timestamp and in-flight counter.

### Frontier lifecycle states
`PENDING → SCHEDULED → FETCHING → FETCHED | FAILED | SKIPPED`, plus `RETRY` with backoff.

## 3. Scheduler & politeness

- **Per-host concurrency cap** and **min delay** (defaults conservative; tunable aggressive).
- **Adaptive throttling:** on 429/503/timeouts, exponentially back off that host; on sustained
  success, ramp concurrency up to the cap.
- **Global rate budget** to protect your uplink and the GPU/index pipeline downstream.
- **DNS + connection caching** to cut latency.
- **robots.txt / crawl-delay:** parser wired but **disabled by default** per owner decision;
  a single config flag re-enables it later (see [11](11-SECURITY-LEGAL.md)).

## 4. Fetchers (static, Go)

- Built on `net/http` + `colly` with:
  - Connection pooling, HTTP/2, gzip/br decompression.
  - Configurable timeouts (connect/read/total), max response size, redirect policy.
  - Cookie jar per host, reton transient errors with jittered backoff.
  - User-agent rotation and realistic header sets.
- Emits raw bytes + response metadata (status, headers, final URL, timing) to the blob store.
- Content-type routing: HTML → extractor; PDF/doc → format handlers; non-target MIME → skip.
- **Escalation rule:** if a static fetch yields near-empty content but the page references heavy
  JS (heuristics: low text/HTML ratio, known SPA frameworks), re-route the URL to a browser worker.

## 5. Browser workers (dynamic, Playwright)

For JS-rendered and social content ([08-SOCIAL-MEDIA.md](08-SOCIAL-MEDIA.md)).

- Pool of headless Chromium contexts; each context keyed and isolated **per host** for session
  mgmt — coherent within a site, never bleeding across sites.
- Wait strategies: network-idle, selector-present, scroll-to-load (infinite scroll), timeouts.
- Anti-detection: stealth plugin, randomized viewport/UA/timezone/locale, human-like delays,
  WebGL/canvas noise, disable automation flags.
- Resource blocking (images/fonts/ads) when only text is needed → faster, cheaper.
- Session/cookie persistence for authenticated targets (credentials injected from config/secret).
  **Implemented** (`browser-worker/src/sessions.ts`): a per-host store pins ONE coherent identity
  plus its accumulated Playwright `storageState` (cookies + localStorage), persisted to a mounted
  volume so warm sessions survive worker restarts. Return visits resume the same identity + jar
  (presenting a fresh fingerprint to a warm cookie jar is itself a tell); same-host renders are
  serialized through a per-key lock so two contexts can't race one jar; sessions rotate (new
  identity + empty jar) past a configurable TTL so no pair becomes a permanent tracking signal.
  An injected login cookie for a host simply lands in this jar and is reused thereafter — the
  substrate authenticated targets need. Metrics: `browserworker_session_{created,resumed,rotated}_total`.
- Proxy pool for egress distribution. **Implemented** (`browser-worker/src/proxies.ts`): the pool
  is fed a list of the owner's **own** self-run proxies via config (`RENDER_PROXIES` inline and/or
  `RENDER_PROXY_FILE`) — no paid provider (CLAUDE.md rule 2). A proxy is **pinned per host** (the
  same boundary sessions use) so a warm cookie jar + fingerprint keeps a **stable egress IP** —
  changing IP under a warm session is itself a tell. Health is tracked per proxy: consecutive
  failures push it into a **capped exponential cooldown** (a success clears it); selection prefers
  the least-loaded healthy proxy so assignments spread evenly, and a host pinned to a proxy that
  enters cooldown is repinned to a healthy one (recall-first: reaching content beats holding a dead
  IP). Empty pool ⇒ direct connection. Chromium is launched with the `per-context` proxy sentinel so
  each context binds its own egress. Metrics: `browserworker_proxy_{selected,failed}_total`,
  `browserworker_proxy_{healthy,pool_size}`.
- Screenshot/DOM capture for audit.
- Browser workers are **expensive** (CPU/RAM) → separate queue, lower concurrency, only for URLs
  that require it.

**Render queue (implemented).** The escalation gate enqueues flagged URLs into a durable
`render_queue` table (frontier-shaped: `PENDING → RENDERING → RENDERED | FAILED`, priority,
attempts, `claimed_at` lease). The browser pool is a separate-language service that consumes it
over the control API — `POST /internal/render/claim` (lease a batch), `POST /internal/render/ingest`
(submit the rendered DOM; the crawler extracts + indexes it through the same pipeline as a static
fetch and marks the job `RENDERED`), `POST /internal/render/complete` (report a retryable/terminal
failure), `GET /internal/render/queue` (depth by state). Jobs orphaned in `RENDERING` (crashed
worker) are requeued by the same reaper that guards the frontier. The worker *process* itself
(headless fetch that drives claim → render → ingest) is the remaining piece.

## 6. Anti-blocking toolkit

| Technique | Purpose |
|-----------|---------|
| User-agent & header rotation | Avoid trivial UA-based blocks |
| Proxy pool rotation ✅ | Distribute IPs, bypass IP bans/geo (self-run proxies, pinned per host, health-tracked cooldown) |
| Request pacing & jitter | Mimic human timing |
| Browser fingerprint mitigation | Defeat JS-based bot detection |
| Session/cookie reuse ✅ | Reduce re-auth and challenge frequency (per-host pinned identity + persisted jar) |
| CAPTCHA handling hooks | Pluggable solver integration (later) |
| Backoff on soft-blocks | Detect block pages/redirects and cool down |

Blocking is expected and continuous, especially for social. Treat evasion as an ongoing
maintenance surface, monitored via metrics (block rate per host/platform).

## 7. Discovery sources (to maximize breadth)

All discovery uses **free/open** sources only (no paid search APIs — see `CLAUDE.md`):

- Seed lists & sitemaps (`/sitemap.xml`, sitemap indexes). **Implemented:**
  `crawler/internal/sitemap` (gzip-aware parse; follows one sitemap-index level with URL/sitemap
  caps) behind `POST /internal/sitemap/ingest {campaign_id, url}`, which bulk-enqueues the listed
  URLs into the campaign frontier via the same canonicalize→`AddURL` path as seeds.
- Outlinks from crawled pages (recursive, scope-bounded) — the primary breadth engine.
- RSS/Atom feeds for freshness.
- **Common Crawl URL indexes** (free) for massive cold-start breadth — a free substitute for
  commercial search APIs to widen the mouth of the funnel.
- Public/free URL datasets, open directories, and web archives.
- Platform-specific discovery (hashtags, profiles, public search endpoints) for social — via
  free/open APIs or browser scraping, never paid data feeds.

## 8. Scope & campaign configuration

A **crawl campaign** config defines:
```yaml
name: news-tech
seeds: [ "https://example.com" ]
include: [ "*.example.com/*" ]        # allow rules
exclude: [ "*/tag/*", "*.pdf$" ]      # deny rules
max_depth: 5
max_pages: 1_000_000
render_js: auto                        # never | auto | always
priority_weights: { authority: 0.4, freshness: 0.3, depth: 0.2, value: 0.1 }
politeness: { per_host_concurrency: 4, min_delay_ms: 500, adaptive: true }
recrawl: { cadence: "24h", strategy: conditional_get }
```

## 9. Reliability

- **Idempotency:** fetching a URL twice is safe; extraction keyed by content hash.
- **Crash recovery:** frontier is durable; in-flight URLs time out back to PENDING.
- **Backpressure:** if the queue/index lags, fetchers slow down (bounded queue depth).
- **Dead-letter queue** for URLs failing after max retries, with reason codes.

## 10. Metrics (exported to Prometheus)

- Pages fetched/sec, bytes/sec, per-host and global.
- Frontier size, by state.
- Fetch error rate by status code, block rate per host/platform.
- Static vs browser fetch ratio and per-fetch latency.
- Queue depth to the extractor and downstream lag.
