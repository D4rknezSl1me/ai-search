// Headless Chromium renderer. Owns one long-lived browser; each job runs in its
// own context so sessions never bleed between *targets* (contexts are keyed and
// isolated per host). Anti-detection is the fingerprint half of docs/04 §5: every
// context gets one internally-consistent identity (see fingerprint.ts) plus
// human-like pacing. When sessions are enabled, that identity and the cookie jar
// are pinned per host and persisted (see sessions.ts) so return visits resume a
// warm, coherent session — the substrate authenticated targets need. Proxies
// remain the last open Phase 3 task.

import { type Browser, type BrowserContext, type Page, chromium } from "playwright";
import type { Config } from "./config.js";
import { buildIdentity, contextOptions, stealthInit, stealthPayload } from "./fingerprint.js";
import { metrics } from "./metrics.js";
import { SessionStore, type SessionHandle, type StorageState } from "./sessions.js";

export interface RenderOutput {
  html: string;
  finalURL: string;
  status: number;
}

const BLOCKED_RESOURCE_TYPES = new Set(["image", "media", "font"]);

// Random integer in [min, max] — used to jitter human-like pauses so successive
// renders never share an identical timing signature.
function jitter(min: number, max: number): number {
  return min + Math.floor(Math.random() * (max - min + 1));
}

export class Renderer {
  private browser: Browser | null = null;
  private readonly sessions: SessionStore | null;

  constructor(private readonly cfg: Config) {
    this.sessions = cfg.sessions
      ? new SessionStore({ dir: cfg.sessionDir, ttlMs: cfg.sessionTtlMs })
      : null;
  }

  async start(): Promise<void> {
    this.browser = await chromium.launch({
      headless: true,
      // Drop the most obvious automation tells.
      args: [
        "--disable-blink-features=AutomationControlled",
        "--no-sandbox",
        "--disable-dev-shm-usage",
      ],
    });
  }

  async stop(): Promise<void> {
    await this.browser?.close();
    this.browser = null;
  }

  async render(url: string): Promise<RenderOutput> {
    if (!this.browser) throw new Error("renderer not started");

    // Resolve the identity + cookie jar for this render. With sessions on, both
    // are pinned per host and resumed from prior visits; off, we mint a fresh
    // coherent identity with an empty jar (the original per-job behaviour).
    const handle = this.sessions ? await this.sessions.acquire(url) : null;
    if (handle) this.recordSession(handle);
    const identity = handle ? handle.identity : buildIdentity();
    const seedState = handle ? handle.storageState : undefined;

    const context: BrowserContext = await this.browser.newContext({
      ...contextOptions(identity),
      // storageState seeds cookies + localStorage so the site sees a returning
      // browser. Empty ⇒ a clean first visit.
      ...(seedState ? { storageState: seedState as never } : {}),
    });
    await context.addInitScript(stealthInit, stealthPayload(identity));

    if (this.cfg.blockResources) {
      await context.route("**/*", (route) => {
        if (BLOCKED_RESOURCE_TYPES.has(route.request().resourceType())) {
          return route.abort();
        }
        return route.continue();
      });
    }

    const page = await context.newPage();
    try {
      const response = await page.goto(url, {
        waitUntil: this.cfg.waitUntil,
        timeout: this.cfg.navTimeoutMs,
      });

      // Human-like pacing: a brief post-load pause and a small mouse move so the
      // session isn't a dead-still, zero-interaction fetch. Behavioural tells are
      // cheap to add and gated off (RENDER_HUMANIZE=false) when latency matters.
      if (this.cfg.humanize) {
        await this.humanize(page);
      }

      // Trigger lazy/infinite content, then let late hydration settle.
      await this.autoScroll(page);
      if (this.cfg.settleMs > 0) {
        await page.waitForTimeout(this.cfg.settleMs);
      }

      const html = await page.content();
      // Persist the cookie jar as the site left it, so the next visit resumes it.
      if (handle) {
        const nextState = (await context.storageState()) as unknown as StorageState;
        await handle.release(nextState);
      }
      return {
        html,
        finalURL: page.url() || url,
        status: response?.status() ?? 0,
      };
    } catch (err) {
      // Failed render: unlock the host without persisting a half-baked jar.
      if (handle) await handle.release();
      throw err;
    } finally {
      await context.close();
    }
  }

  // Translate a leased session's origin into the corresponding metric.
  private recordSession(handle: SessionHandle): void {
    if (handle.origin === "created") metrics.sessionCreated.inc();
    else if (handle.origin === "rotated") metrics.sessionRotated.inc();
    else metrics.sessionResumed.inc();
  }

  // A short randomized settle plus a couple of mouse moves — enough to register
  // as "some interaction" without materially slowing the render.
  private async humanize(page: Page): Promise<void> {
    await page.waitForTimeout(jitter(120, 450));
    const { width, height } = page.viewportSize() ?? { width: 1366, height: 768 };
    for (let i = 0; i < 2; i++) {
      await page.mouse.move(jitter(0, width), jitter(0, height), { steps: jitter(3, 8) });
      await page.waitForTimeout(jitter(40, 160));
    }
  }

  // Scroll to the bottom in steps to fire IntersectionObserver-driven loaders,
  // capped by scrollPasses so a genuinely infinite feed can't hang the render.
  private async autoScroll(page: Page): Promise<void> {
    for (let i = 0; i < this.cfg.scrollPasses; i++) {
      const grew = await page.evaluate(() => {
        const before = document.body ? document.body.scrollHeight : 0;
        window.scrollTo(0, document.body ? document.body.scrollHeight : 0);
        return before;
      });
      // Jittered dwell between scroll steps rather than a fixed 300ms cadence.
      await page.waitForTimeout(jitter(220, 480));
      const after = await page.evaluate(() =>
        document.body ? document.body.scrollHeight : 0,
      );
      if (after <= grew) break; // page stopped growing
    }
    await page.evaluate(() => window.scrollTo(0, 0));
  }
}
