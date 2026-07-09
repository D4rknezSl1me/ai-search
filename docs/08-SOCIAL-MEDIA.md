# 08 — Social Media Ingestion

Social is the highest-value and highest-difficulty source. This document describes the technical
approach. **Legal/ToS considerations are deferred by explicit owner decision** and tracked in
[11-SECURITY-LEGAL.md](11-SECURITY-LEGAL.md); this doc is purely about *how* it works.

## 1. Why it's hard (engineering reality)

- Content is behind authentication and heavy client-side rendering.
- Aggressive anti-bot: rate limits, device/browser fingerprinting, behavioral analysis,
  challenge/CAPTCHA walls, and account/IP bans.
- Frequent, unannounced changes to page structure and defenses → adapters break often.
- Official APIs are restricted, paid, and rate-limited.

Expect social ingestion to be a **continuously maintained** subsystem with a per-platform
adapter, not a set-and-forget crawler. Design for graceful breakage and fast repair.

## 2. Architecture: per-platform adapters

Each platform gets an adapter implementing a common interface:

```
interface SocialAdapter {
  discover(seed): URLs         // profiles, hashtags, search results, threads
  fetch(target): RawContent    // via API tier, or browser worker
  parse(raw): Document[]        // posts/comments → normalized documents
  paginate(state): next         // cursors / scroll / since-id
  health(): status             // detect breakage / blocks
}
```

Adapters plug into the same frontier → fetch → extract → index pipeline, but route through a
dedicated **social fetch queue** (lower concurrency, session-aware, proxy-backed).

## 3. Acquisition tiers

**No paid services** (see `CLAUDE.md`): use only free/open APIs and self-run browser scraping.
Paid platform API tiers and third-party data vendors are **out of scope**.

1. **Free/open official APIs** (where they exist within free limits) — most stable; e.g.
   Mastodon/Fediverse, Reddit and Telegram public within free rate limits, YouTube transcripts.
2. **Authenticated browser sessions** (Playwright) — render as a logged-in client; handle
   infinite scroll, lazy-loading, and cursor pagination. The primary tier for hostile platforms.
3. **Undocumented/internal JSON endpoints** the web client calls — parsed from network traffic;
   more efficient than DOM scraping but brittle.

> Paid API tiers (e.g. X's paid API) and commercial scraping/data vendors are explicitly
> excluded. Coverage of the most hostile platforms is therefore best-effort via browser scraping.

## 4. Anti-detection stack (browser tier)

- **Self-run** proxy rotation (owner's IPs / free proxies); sticky sessions per account. No paid
  residential-proxy providers.
- Realistic fingerprints: UA, viewport, timezone, locale, fonts, WebGL/canvas noise.
- Human-like behavior: randomized delays, scroll patterns, mouse movement.
- Account pool management: multiple sessions, rotation, cool-down on challenge, ban detection.
- Request pacing tuned per platform's tolerance; back off hard on soft-blocks.
- Cookie/localStorage persistence to minimize re-auth and challenge frequency.

## 5. Per-platform notes (capabilities vary and change)

| Platform | Primary tier | Notes |
|----------|--------------|-------|
| X/Twitter | browser only | Paid API excluded; browser scraping only. Strong anti-bot. |
| Reddit | free API | Well-structured API; free rate limits; good ROI. |
| Instagram | browser + internal JSON | Heavy fingerprinting; media-centric; login required. |
| Facebook | browser | Very hostile; public pages/groups only realistically. |
| TikTok | browser + internal JSON | Aggressive defenses; region-sensitive. |
| YouTube | API + transcript scrape | API for metadata; transcripts for searchable text. |
| LinkedIn | browser | Extremely hostile to automation; high ban risk. |
| Telegram | API (Bot/MTProto) | Public channels accessible via API. |
| Mastodon/Fediverse | API | Open APIs; easy, high-quality. |
| Hacker News | free API | Official Firebase API; no auth; stories + full comment threads. |
| Lemmy | free API | Open v3 REST API; no auth; Reddit-shaped posts + comment threads. |

Start with the **easy, open, high-ROI** platforms (Reddit, Mastodon, Telegram public, YouTube
transcripts, Hacker News, Lemmy) to build the pipeline, then invest in the hostile ones.

## 6. Normalization

Social content maps to the same `Document`/`Chunk` model with extra fields in `meta`:
- `platform`, `post_id`, `author_handle`, `posted_at`, `engagement` (likes/shares/replies),
  `parent_id` (for threads/comments), `media_urls`, `permalink`.
- Threads/comment trees flattened into linked documents preserving reply structure.
- Engagement metrics feed the priority/authority signals.

## 7. Freshness & monitoring

- Social is time-sensitive → short recrawl cadence for tracked entities/hashtags.
- Incremental fetch via `since_id`/cursors to pull only new content.
- Feeds the monitoring/alerts feature (saved queries notify on new matches).

## 8. Health & metrics

- Per-adapter: success rate, block/challenge rate, items/hour, freshness lag, breakage alerts.
- Automatic disable + alert when an adapter's error rate crosses a threshold (fail loud, don't
  silently under-collect).
- **Live status endpoint (implemented):** the crawler registers adapters in a `social.Registry`
  and exposes `GET /internal/social/adapters` (all, with an aggregate `summary`) and
  `GET /internal/social/adapters/{name}` (one). Each snapshot reports `enabled`, `fetches`,
  `errors`, `items`, `error_rate`, `last_error`, `last_activity`; `enabled=false` reflects the
  auto-disable above. Counters also feed the `crawler_social_{fetch,items}_total` Prometheus
  metrics. Contract described in [13-API.md](13-API.md) §2.

## 9. Maintenance expectation

Budget ongoing engineering time for adapter repair. Version adapters, keep fixtures of expected
page/JSON shapes, and add contract tests that alert when a platform changes structure.
