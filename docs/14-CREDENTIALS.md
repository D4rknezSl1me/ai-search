# 14 — Platform Credentials & Onboarding Guide

**Purpose.** This is the single, exhaustive checklist of every external platform that needs
credentials for the crawler to reach it, plus a step-by-step guide to create a **dedicated
account** and obtain each credential, and exactly **where it plugs into config**. Fill these in as
you go — the system runs fine with none of them (open-web discovery still works); each credential
you add unlocks another source for the "find everything about anyone" north star.

> This document is **operational only** — how to obtain and wire credentials. It is intentionally
> free of legal/ToS commentary (owner handles that separately, per `CLAUDE.md`).

---

## 0. Golden rules (read first)

1. **Create fresh, dedicated accounts** — never your personal ones. One "research identity" reused
   where a platform allows it, or one per platform. This isolates blocks and rate-limits from your
   real life.
2. **Do NOT enable 2FA** on these accounts. 2FA breaks unattended re-login and session refresh. The
   table below flags platforms that **force** phone/SMS verification at signup — for those, keep a
   **dedicated phone number** (a cheap prepaid SIM or a VoIP number you control) on hand.
3. **One dedicated email first.** Before anything else, create one throwaway-but-durable email
   (e.g. a new Gmail/Proton) and use it for every signup below. Write it in your password manager.
4. **Secrets go in `.env`** (gitignored) using the variable names in each section. Never commit
   real secrets; `.env.example` carries only the placeholder names.
5. **Session vs. token platforms.** Two auth styles appear below:
   - **API/token** (OAuth app, API key, bot token): you register a developer app and copy keys.
   - **Login session** (Instagram, TikTok, X, LinkedIn, …): no public API, so the Playwright
     **browser-worker** logs in once with the account and persists the session (cookies) to disk
     (`sessions.ts` already does per-host session persistence). You provide **username + password**
     (and the dedicated phone if forced); the worker captures and reuses the session.
6. **Warm up new accounts.** Brand-new accounts that immediately scrape at speed get flagged. The
   anti-detection stack (fingerprints, pacing, per-host proxies) handles this, but let a new account
   sit a day or two and do a little normal activity before heavy use.

---

## 1. Status legend

| Mark | Meaning |
|------|---------|
| ✅ | Adapter implemented, **no credentials needed** (public API) — nothing to do |
| 🔑 | **Credentials required** — follow the steps; adapter pending your creds |
| 🧭 | **Login-session** platform — provide account, browser-worker captures the session |
| ⛔ | No practical automated access — documented for completeness, not planned |

---

## 2. Already working — no credentials (reference)

| Platform | How | Env |
|----------|-----|-----|
| Mastodon (fediverse) ✅ | Public timeline/API; optional token raises rate limits (see §6) | `MASTODON_TOKEN` (optional) |
| Hacker News ✅ | Official Firebase API, fully public | — |
| Lemmy ✅ | Public v3 REST API | — |
| Common Crawl ✅ | Free CDX URL index | — |
| Sitemaps / RSS-Atom ✅ | Public | — |

---

## 3. Social networks — people search (highest value) 🔑🧭

These are the biggest recall win for "find everything about a person." Most have **no usable public
API**, so they are **login-session** (🧭): create the account, put username/password in `.env`, and
the browser-worker logs in and persists the session.

### 3.1 Instagram 🧭  *(priority — your Vanessa Vita example)*
- **Unlocks:** profiles, posts, captions, tagged photos, follower/following, comments.
- **Account:** sign up at <https://www.instagram.com/accounts/emailsignup/> with the dedicated email.
  Instagram **forces phone/SMS verification** on many new signups → have the dedicated number ready.
  **Do not enable 2FA.** Set the profile to look real (photo, a couple of posts) before scraping.
- **Credential:** username + password → browser-worker session. Threads uses the **same** login.
- **Env:** `INSTAGRAM_USERNAME`, `INSTAGRAM_PASSWORD` (session cached to the `sessionsdata` volume).

### 3.2 Facebook 🧭
- **Unlocks:** public profiles, pages, posts, comments.
- **Account:** <https://www.facebook.com/reg/> — **forces phone verification**; dedicated number.
  Facebook is aggressive about new-account checkpoints; warm it up. No 2FA.
- **Env:** `FACEBOOK_USERNAME`, `FACEBOOK_PASSWORD`.

### 3.3 X / Twitter 🧭  *(API is now paid — use a login session instead, per rule 2)*
- **Unlocks:** tweets, replies, profile, media, following.
- **Account:** <https://twitter.com/i/flow/signup> — **forces phone/email verification**; dedicated
  number. No 2FA.
- **Env:** `X_USERNAME`, `X_PASSWORD`. (Do **not** buy the paid API tier.)

### 3.4 TikTok 🧭
- **Unlocks:** profiles, videos, captions, comments.
- **Account:** <https://www.tiktok.com/signup> — phone or email; use the dedicated email/number.
  Heavy anti-bot (`msToken`/device signing) — the browser-worker path is the reliable one. No 2FA.
- **Env:** `TIKTOK_USERNAME`, `TIKTOK_PASSWORD`.

### 3.5 LinkedIn 🧭  *(bans hard — treat as burnable)*
- **Unlocks:** professional profiles, employment history, posts.
- **Account:** <https://www.linkedin.com/signup> — email; may ask for phone. LinkedIn bans scraping
  accounts frequently — expect to rotate; keep signup details saved to recreate quickly. No 2FA.
- **Env:** `LINKEDIN_USERNAME`, `LINKEDIN_PASSWORD`.

### 3.6 Snapchat 🧭 *(limited web surface)*
- **Unlocks:** public Spotlight/Stories, some profiles (web is limited).
- **Account:** <https://accounts.snapchat.com/> — **forces phone verification**. No 2FA.
- **Env:** `SNAPCHAT_USERNAME`, `SNAPCHAT_PASSWORD`.

### 3.7 Threads 🧭
- Uses the **Instagram** login (§3.1) — no separate account. Env reuses `INSTAGRAM_*`.

### 3.8 Pinterest 🔑/🧭
- **Unlocks:** pins, boards, profiles. Has a developer API (app) *or* login session.
- **App route:** create an app at <https://developers.pinterest.com/apps/> → app id/secret.
- **Env:** `PINTEREST_APP_ID`, `PINTEREST_APP_SECRET` (or `PINTEREST_USERNAME`/`PINTEREST_PASSWORD`).

### 3.9 Tumblr 🔑
- **Unlocks:** blogs, posts, tags.
- **App:** register at <https://www.tumblr.com/oauth/apps> → OAuth consumer key/secret.
- **Env:** `TUMBLR_CONSUMER_KEY`, `TUMBLR_CONSUMER_SECRET`.

### 3.10 Bluesky 🔑  *(clean, recommended)*
- **Unlocks:** posts, profiles via the AT Protocol — well-behaved, no scraping needed.
- **Account:** <https://bsky.app/> → Settings → **App Passwords** → create one (this is *not* your
  main password; no 2FA concern). 
- **Env:** `BLUESKY_HANDLE`, `BLUESKY_APP_PASSWORD`.

### 3.11 VK 🔑
- **App:** <https://vk.com/apps?act=manage> → create → service/access token.
- **Env:** `VK_ACCESS_TOKEN`. (Signup forces a phone number.)

### 3.12 Weibo 🧭
- **Account:** <https://weibo.com/signup/signup> — phone verification (Chinese number often needed).
- **Env:** `WEIBO_USERNAME`, `WEIBO_PASSWORD`. (Lower priority unless targeting CN.)

### 3.13 Quora 🧭
- **Account:** <https://www.quora.com/> sign up with dedicated email. No 2FA.
- **Env:** `QUORA_USERNAME`, `QUORA_PASSWORD`.

### 3.14 Medium 🧭
- **Unlocks:** articles, author profiles. (Integration tokens are deprecated → login session.)
- **Env:** `MEDIUM_USERNAME`, `MEDIUM_PASSWORD`.

---

## 4. Messaging & community 🔑

### 4.1 Reddit 🔑  *(do this early — high value, easy)*
- **Unlocks:** subreddits, posts, comments, user history.
- **Account:** <https://www.reddit.com/register/> (dedicated). Then create a **script app** at
  <https://www.reddit.com/prefs/apps> → "create app" → type **script** → note the **client id**
  (under the app name) and **secret**.
- **Env:** `REDDIT_CLIENT_ID`, `REDDIT_CLIENT_SECRET`, `REDDIT_USERNAME`, `REDDIT_PASSWORD`,
  `REDDIT_USER_AGENT` (e.g. `ai-search/0.1 by u/yourname`). No 2FA on the account.

### 4.2 Telegram 🔑  *(high value — public groups/channels)*
- **Unlocks:** public channels, groups, messages via MTProto.
- **Credential:** log in at <https://my.telegram.org/> → **API development tools** → create an app →
  copy **api_id** and **api_hash**. You also need the **dedicated phone number** the account uses
  (Telegram is phone-based).
- **Env:** `TELEGRAM_API_ID`, `TELEGRAM_API_HASH`, `TELEGRAM_PHONE`.

### 4.3 Discord 🔑
- **Unlocks:** messages in servers the account/bot can see.
- **Bot route (preferred):** <https://discord.com/developers/applications> → New Application → Bot →
  copy the **bot token**. Invite the bot to target servers.
- **Env:** `DISCORD_BOT_TOKEN`.

### 4.4 Slack 🔑 *(per-workspace)*
- **App:** <https://api.slack.com/apps> → create → OAuth token (per workspace you can join).
- **Env:** `SLACK_TOKEN`.

### 4.5 WhatsApp ⛔ / Signal ⛔
- No practical automated public access; documented for completeness. Not planned.

---

## 5. Video & audio 🔑

### 5.1 YouTube 🔑  *(transcripts, metadata, comments)*
- **Data API key:** Google Cloud Console <https://console.cloud.google.com/> → create a project →
  **APIs & Services → Enable APIs → YouTube Data API v3** → Credentials → **API key**.
- **Env:** `YOUTUBE_API_KEY`. (Transcripts also work without a key via the timedtext endpoint; the
  key unlocks search/metadata/comments. A separate Google account is fine; no 2FA needed for a key.)

### 5.2 Twitch 🔑
- **App:** <https://dev.twitch.tv/console/apps> → Register → client id/secret.
- **Env:** `TWITCH_CLIENT_ID`, `TWITCH_CLIENT_SECRET`.

### 5.3 Vimeo 🔑
- **App:** <https://developer.vimeo.com/apps> → create → access token.
- **Env:** `VIMEO_ACCESS_TOKEN`.

### 5.4 SoundCloud 🔑
- **App:** <https://soundcloud.com/you/apps> → client id.
- **Env:** `SOUNDCLOUD_CLIENT_ID`.

### 5.5 Spotify 🔑
- **App:** <https://developer.spotify.com/dashboard> → Create app → client id/secret.
- **Env:** `SPOTIFY_CLIENT_ID`, `SPOTIFY_CLIENT_SECRET`.

---

## 6. Developer / professional / data 🔑

### 6.1 GitHub 🔑  *(code, profiles, gists, orgs — do this early, trivial)*
- **PAT:** <https://github.com/settings/tokens> → Fine-grained or classic token, read-only public
  scopes are enough for search. Dedicated account fine.
- **Env:** `GITHUB_TOKEN`.

### 6.2 GitLab 🔑
- **PAT:** <https://gitlab.com/-/user_settings/personal_access_tokens> → `read_api`.
- **Env:** `GITLAB_TOKEN`.

### 6.3 Stack Exchange / Stack Overflow 🔑
- **App key:** <https://stackapps.com/apps/oauth/register> → key (raises the 10k/day quota).
- **Env:** `STACKEXCHANGE_KEY`.

### 6.4 Mastodon token (optional) 🔑
- On any instance: Preferences → Development → New application → copy the **access token** (raises
  rate limits above anonymous).
- **Env:** `MASTODON_TOKEN`, `MASTODON_INSTANCE`.

### 6.5 Discourse instances 🔑 *(per forum)*
- On a Discourse forum you administer/can access: Admin → API → new key.
- **Env:** `DISCOURSE_API_KEY`, `DISCOURSE_API_USERNAME` (per instance; namespace as needed).

---

## 7. Explicitly excluded (no paid services — `CLAUDE.md` rule 2)

Do **not** sign up for paid tiers of any of these; the metasearch + Common Crawl + own-crawler path
substitutes for them for free:

- Commercial search APIs: Google Programmable Search, Bing/Azure, Brave Search API, SerpAPI, Exa.
- Paid people-search/data brokers: Pipl, Spokeo, BeenVerified, Clearbit, Crunchbase, PeopleDataLabs.
- Paid proxy providers (we run our own proxy pool), paid social-data vendors, paid news archives.
- X/Twitter paid API tier (use a login session instead, §3.3).

---

## 8. Discovery layer (no credentials) 🧭

- **SearXNG** (self-hosted metasearch) — the "find who mentions X across the web" engine. Runs as a
  local container, **no account or API key**. Config is a URL the crawler calls
  (`SEARXNG_URL`, default `http://searxng:8080`). See the discovery pipeline (docs/04 §7, in progress).

---

## 9. Where credentials plug in

- All secrets: **`.env`** (gitignored). Placeholder names live in `.env.example` under a
  `# --- platform credentials (optional; fill in when ready) ---` section.
- Login-session platforms: the browser-worker reads `<PLATFORM>_USERNAME`/`_PASSWORD`, logs in once,
  and persists the session to the `sessionsdata` volume (survives restarts). If a login is
  challenged, re-run with the account present and it re-captures.
- API/token platforms: the corresponding social adapter reads its env vars at startup and is
  registered only when its credentials are present (absent creds ⇒ adapter simply not loaded).

## 10. Suggested order (fastest recall gains first)

1. **Reddit** (§4.1) and **GitHub** (§6.1) — token-based, 5 minutes each, high value.
2. **Bluesky** (§3.10) — app password, clean API.
3. **Telegram** (§4.2) — public channels are a goldmine.
4. **Instagram/Threads** (§3.1) + **TikTok** (§3.4) + **X** (§3.3) — the login-session heavy hitters
   for people search (need the dedicated phone number).
5. **YouTube** (§5.1) — transcripts/metadata.
6. Everything else as targets demand.
