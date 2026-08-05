// Proxy pool for the render pool — the last open piece of the docs/04 §5/§6
// anti-detection stack. Its job is to distribute the worker's egress across a set
// of self-run proxies (CLAUDE.md rule 2: NO paid proxy providers — the pool is
// fed a list of the owner's own proxies via config), so a single machine's IP
// isn't the fingerprint every target sees.
//
// The one hard constraint is coherence with sessions.ts: a warm cookie jar +
// pinned fingerprint that suddenly speaks from a new IP is itself a bot tell.
// So the proxy is *pinned per host* (the same boundary sessions use): a host
// keeps one egress IP for as long as that proxy is healthy. The pin is only
// broken when the proxy fails enough to enter cooldown — at which point reaching
// the content at all (recall-first, per CLAUDE.md) beats holding a dead IP.
//
// Health is tracked per proxy: consecutive failures push a proxy into a capped
// exponential cooldown; a success clears it. Selection prefers the least-loaded
// healthy proxy so assignments spread evenly. If every proxy is cooling down we
// still hand back the one closest to recovery rather than failing the render.
//
// The module imports nothing from Playwright: it deals only in the proxy option
// shape Playwright's newContext expects, which keeps the selection/health logic
// pure and unit-testable without launching a browser.

/** A parsed proxy endpoint. `label` is the credential-free form for logs/metrics. */
export interface ProxyEntry {
  /** `scheme://host:port` — what Playwright's proxy.server wants. */
  server: string;
  username?: string;
  password?: string;
  /** server with credentials stripped (they never appear in `server` anyway). */
  label: string;
}

/** The subset of Playwright's `proxy` context option we populate. */
export interface ProxyOption {
  server: string;
  username?: string;
  password?: string;
}

/**
 * A pinned proxy for one render. The caller passes `option` to `newContext`,
 * then MUST call `report(ok)` once so the pool can track that proxy's health.
 */
export interface ProxyLease {
  option: ProxyOption;
  /** Credential-free label of the chosen proxy (for logs/metrics). */
  label: string;
  report(ok: boolean): void;
}

interface ProxyState {
  entry: ProxyEntry;
  consecutiveFailures: number;
  /** Epoch ms until which this proxy is benched; 0 (or past) ⇒ healthy. */
  cooldownUntil: number;
  /** Hosts currently pinned to this proxy — drives least-loaded selection. */
  pinnedHosts: Set<string>;
  totalUses: number;
}

export interface ProxyPoolOptions {
  entries: ProxyEntry[];
  /** Base cooldown after the first failure (ms). Doubles per consecutive fail. */
  cooldownMs: number;
  /** Cap on the exponential cooldown (ms). */
  maxCooldownMs: number;
  /** Injectable clock (tests). */
  now?: () => number;
  /** Sink for pool events (cooldown/reassign); defaults to a console.warn line. */
  onWarn?: (msg: string, extra: Record<string, unknown>) => void;
}

/**
 * Split a proxy spec into a `server` + optional credentials, matching what
 * Playwright expects. Accepts `scheme://[user:pass@]host:port`; a bare
 * `host:port` is treated as `http://host:port`. Returns null for junk lines so
 * a typo in the list can't crash the worker.
 */
export function parseProxyLine(raw: string): ProxyEntry | null {
  const trimmed = raw.trim();
  if (!trimmed || trimmed.startsWith("#")) return null;
  const withScheme = /^[a-z0-9]+:\/\//i.test(trimmed) ? trimmed : `http://${trimmed}`;
  let url: URL;
  try {
    url = new URL(withScheme);
  } catch {
    return null;
  }
  if (!url.hostname || !url.port) return null;
  // Playwright's server must not carry credentials; it takes them separately.
  const server = `${url.protocol}//${url.host}`;
  const username = url.username ? decodeURIComponent(url.username) : undefined;
  const password = url.password ? decodeURIComponent(url.password) : undefined;
  return { server, username, password, label: server };
}

/**
 * Parse a whole proxy list — one entry per line and/or comma-separated, with
 * `#` comments and blank lines ignored. Deduplicates by server so the same proxy
 * listed twice doesn't skew load balancing.
 */
export function parseProxies(raw: string): ProxyEntry[] {
  const out: ProxyEntry[] = [];
  const seen = new Set<string>();
  for (const token of raw.split(/[\r\n,]+/)) {
    const entry = parseProxyLine(token);
    if (!entry || seen.has(entry.server)) continue;
    seen.add(entry.server);
    out.push(entry);
  }
  return out;
}

export class ProxyPool {
  private readonly states: ProxyState[];
  private readonly assignments = new Map<string, ProxyState>();
  private readonly now: () => number;
  private roundRobin = 0;

  constructor(private readonly opts: ProxyPoolOptions) {
    this.now = opts.now ?? Date.now;
    this.states = opts.entries.map((entry) => ({
      entry,
      consecutiveFailures: 0,
      cooldownUntil: 0,
      pinnedHosts: new Set<string>(),
      totalUses: 0,
    }));
  }

  /** Number of configured proxies. */
  get size(): number {
    return this.states.length;
  }

  /** Whether routing through a proxy is on at all (empty pool ⇒ direct). */
  get enabled(): boolean {
    return this.states.length > 0;
  }

  /** Healthy = not currently in cooldown. */
  healthyCount(): number {
    const now = this.now();
    return this.states.reduce((n, s) => n + (s.cooldownUntil <= now ? 1 : 0), 0);
  }

  /**
   * Pin (or resume) a proxy for `host`. Returns null when the pool is empty, so
   * the caller connects directly. A host keeps its proxy while that proxy is
   * healthy; if the pin has gone into cooldown, it is reassigned to a healthy one.
   */
  acquire(host: string): ProxyLease | null {
    if (this.states.length === 0) return null;
    const now = this.now();

    let state = this.assignments.get(host);
    if (state && state.cooldownUntil > now) {
      // The host's pinned proxy is benched — move it to a live one. Egress IP
      // changes, but an unreachable host helps nobody (recall-first).
      const replacement = this.select(now);
      this.warn("proxy reassigned (pinned proxy in cooldown)", {
        host,
        from: state.entry.label,
        to: replacement.entry.label,
      });
      this.repin(host, state, replacement);
      state = replacement;
    } else if (!state) {
      state = this.select(now);
      state.pinnedHosts.add(host);
      this.assignments.set(host, state);
    }

    state.totalUses += 1;
    const chosen = state;
    return {
      option: this.optionFor(chosen.entry),
      label: chosen.entry.label,
      report: (ok: boolean) => this.report(chosen, ok),
    };
  }

  /** Record a render outcome for a proxy: success clears cooldown, failure grows it. */
  private report(state: ProxyState, ok: boolean): void {
    if (ok) {
      state.consecutiveFailures = 0;
      state.cooldownUntil = 0;
      return;
    }
    state.consecutiveFailures += 1;
    const backoff = Math.min(
      this.opts.maxCooldownMs,
      this.opts.cooldownMs * 2 ** (state.consecutiveFailures - 1),
    );
    state.cooldownUntil = this.now() + backoff;
    this.warn("proxy in cooldown", {
      proxy: state.entry.label,
      failures: state.consecutiveFailures,
      cooldown_ms: backoff,
    });
  }

  // Choose the best proxy for a new assignment: fewest pinned hosts among the
  // healthy set, round-robin to break ties so load spreads evenly. If none are
  // healthy, fall back to the one recovering soonest (best-effort, never fail).
  private select(now: number): ProxyState {
    const healthy = this.states.filter((s) => s.cooldownUntil <= now);
    const pool = healthy.length > 0 ? healthy : this.states;
    if (healthy.length === 0) {
      // All cooling down — pick the soonest to recover.
      return pool.reduce((best, s) => (s.cooldownUntil < best.cooldownUntil ? s : best));
    }
    let minLoad = Infinity;
    for (const s of pool) minLoad = Math.min(minLoad, s.pinnedHosts.size);
    const leastLoaded = pool.filter((s) => s.pinnedHosts.size === minLoad);
    const chosen = leastLoaded[this.roundRobin % leastLoaded.length];
    this.roundRobin += 1;
    return chosen;
  }

  private repin(host: string, from: ProxyState, to: ProxyState): void {
    from.pinnedHosts.delete(host);
    to.pinnedHosts.add(host);
    this.assignments.set(host, to);
  }

  private optionFor(entry: ProxyEntry): ProxyOption {
    const opt: ProxyOption = { server: entry.server };
    if (entry.username !== undefined) opt.username = entry.username;
    if (entry.password !== undefined) opt.password = entry.password;
    return opt;
  }

  private warn(msg: string, extra: Record<string, unknown>): void {
    if (this.opts.onWarn) return this.opts.onWarn(msg, extra);
    console.warn(JSON.stringify({ ts: new Date().toISOString(), level: "warn", msg, ...extra }));
  }
}
