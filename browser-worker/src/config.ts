// Runtime configuration for the browser-worker, sourced from the environment so
// it slots into the same .env-driven compose stack as the crawler. Every value
// has a safe default; nothing here is a secret.

function num(name: string, def: number): number {
  const raw = process.env[name];
  if (raw === undefined || raw.trim() === "") return def;
  const v = Number(raw);
  return Number.isFinite(v) ? v : def;
}

function str(name: string, def: string): string {
  const raw = process.env[name];
  return raw === undefined || raw.trim() === "" ? def : raw;
}

function bool(name: string, def: boolean): boolean {
  const raw = process.env[name];
  if (raw === undefined || raw.trim() === "") return def;
  return /^(1|true|yes|on)$/i.test(raw.trim());
}

export interface Config {
  /** Base URL of the crawler control API (claim/ingest/complete). */
  crawlerURL: string;
  /** Health + metrics port this worker exposes. */
  healthPort: number;
  /** How many jobs to lease per claim call. */
  batch: number;
  /** How many pages to render in parallel. */
  concurrency: number;
  /** Idle sleep between claims when the queue is empty (ms). */
  idlePollMs: number;
  /** Per-page navigation budget (ms). */
  navTimeoutMs: number;
  /** Playwright load state to wait for. */
  waitUntil: "load" | "domcontentloaded" | "networkidle" | "commit";
  /** Extra settle time after the wait state, for late hydration (ms). */
  settleMs: number;
  /** Number of scroll passes to trigger lazy/infinite content. */
  scrollPasses: number;
  /** Block images/fonts/media — we only need the text-bearing DOM. */
  blockResources: boolean;
  /** Add human-like pacing (post-load pause, mouse moves, jittered scroll). */
  humanize: boolean;
  /** Retry a failed render (else mark terminal). */
  retryOnFailure: boolean;
  /** Persist & reuse one identity + cookie jar per host across renders. */
  sessions: boolean;
  /** Directory for persisted session state (mounted volume). */
  sessionDir: string;
  /** Rotate a host's identity + jar once it is older than this (ms). */
  sessionTtlMs: number;
}

export function loadConfig(): Config {
  return {
    crawlerURL: str("CRAWLER_URL", "http://crawler:8090").replace(/\/+$/, ""),
    healthPort: num("WORKER_HEALTH_PORT", 8091),
    batch: Math.max(1, num("RENDER_BATCH", 4)),
    concurrency: Math.max(1, num("RENDER_CONCURRENCY", 2)),
    idlePollMs: Math.max(250, num("RENDER_IDLE_POLL_MS", 2000)),
    navTimeoutMs: Math.max(1000, num("RENDER_NAV_TIMEOUT_MS", 20000)),
    waitUntil: str("RENDER_WAIT_UNTIL", "networkidle") as Config["waitUntil"],
    settleMs: Math.max(0, num("RENDER_SETTLE_MS", 500)),
    scrollPasses: Math.max(0, num("RENDER_SCROLL_PASSES", 3)),
    blockResources: bool("RENDER_BLOCK_RESOURCES", true),
    humanize: bool("RENDER_HUMANIZE", true),
    retryOnFailure: bool("RENDER_RETRY_ON_FAILURE", true),
    sessions: bool("RENDER_SESSIONS", true),
    sessionDir: str("RENDER_SESSION_DIR", "/data/sessions"),
    // Default: rotate a host's identity + cookie jar once a day.
    sessionTtlMs: Math.max(0, num("RENDER_SESSION_TTL_MS", 86_400_000)),
  };
}
