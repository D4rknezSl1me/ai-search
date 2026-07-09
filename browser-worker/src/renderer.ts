// Headless Chromium renderer. Owns one long-lived browser; each job runs in its
// own fresh context (isolated cookies/storage) so sessions never bleed between
// targets. Anti-detection here is deliberately lightweight (docs/04 §5) — the
// full fingerprint/proxy stack is a later Phase 3 task; this covers the basics
// that stop trivial headless detection.

import { type Browser, type BrowserContext, chromium } from "playwright";
import type { Config } from "./config.js";

export interface RenderOutput {
  html: string;
  finalURL: string;
  status: number;
}

// Small rotation pools so successive contexts don't look identical. Kept modest
// and realistic rather than exotic.
const VIEWPORTS = [
  { width: 1366, height: 768 },
  { width: 1440, height: 900 },
  { width: 1536, height: 864 },
  { width: 1920, height: 1080 },
];
const LOCALES = ["en-US", "en-GB"];
const TIMEZONES = ["America/New_York", "Europe/London", "America/Chicago"];
const UA_CHROME_VERSIONS = ["124.0.0.0", "125.0.0.0", "126.0.0.0"];

function pick<T>(arr: T[]): T {
  return arr[Math.floor(Math.random() * arr.length)];
}

const BLOCKED_RESOURCE_TYPES = new Set(["image", "media", "font"]);

export class Renderer {
  private browser: Browser | null = null;

  constructor(private readonly cfg: Config) {}

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
    const uaVersion = pick(UA_CHROME_VERSIONS);
    const context: BrowserContext = await this.browser.newContext({
      viewport: pick(VIEWPORTS),
      locale: pick(LOCALES),
      timezoneId: pick(TIMEZONES),
      userAgent:
        `Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 ` +
        `(KHTML, like Gecko) Chrome/${uaVersion} Safari/537.36`,
    });

    // navigator.webdriver === true is the canonical headless giveaway.
    await context.addInitScript(() => {
      Object.defineProperty(navigator, "webdriver", { get: () => undefined });
    });

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

      // Trigger lazy/infinite content, then let late hydration settle.
      await this.autoScroll(page);
      if (this.cfg.settleMs > 0) {
        await page.waitForTimeout(this.cfg.settleMs);
      }

      const html = await page.content();
      return {
        html,
        finalURL: page.url() || url,
        status: response?.status() ?? 0,
      };
    } finally {
      await context.close();
    }
  }

  // Scroll to the bottom in steps to fire IntersectionObserver-driven loaders,
  // capped by scrollPasses so a genuinely infinite feed can't hang the render.
  private async autoScroll(page: import("playwright").Page): Promise<void> {
    for (let i = 0; i < this.cfg.scrollPasses; i++) {
      const grew = await page.evaluate(() => {
        const before = document.body ? document.body.scrollHeight : 0;
        window.scrollTo(0, document.body ? document.body.scrollHeight : 0);
        return before;
      });
      await page.waitForTimeout(300);
      const after = await page.evaluate(() =>
        document.body ? document.body.scrollHeight : 0,
      );
      if (after <= grew) break; // page stopped growing
    }
    await page.evaluate(() => window.scrollTo(0, 0));
  }
}
