// The worker process: claim → render → ingest, forever. A single long-lived
// Chromium serves a bounded pool of concurrent renders. The crawler owns the
// queue's retry/lease semantics, so failures here just report back and move on.

import { createServer, type Server } from "node:http";
import { loadConfig, type Config } from "./config.js";
import { CrawlerClient, type RenderJob } from "./crawlerClient.js";
import { metrics, renderMetrics } from "./metrics.js";
import { Renderer } from "./renderer.js";

function log(level: "info" | "warn" | "error", msg: string, extra?: Record<string, unknown>): void {
  const line = { ts: new Date().toISOString(), level, msg, ...extra };
  console.log(JSON.stringify(line));
}

export class Worker {
  private readonly cfg: Config;
  private readonly client: CrawlerClient;
  private readonly renderer: Renderer;
  private health: Server | null = null;
  private stopping = false;

  constructor(cfg: Config = loadConfig()) {
    this.cfg = cfg;
    this.client = new CrawlerClient(cfg.crawlerURL);
    this.renderer = new Renderer(cfg);
  }

  async run(): Promise<void> {
    this.startHealthServer();
    await this.renderer.start();
    log("info", "browser-worker started", {
      crawler: this.cfg.crawlerURL,
      batch: this.cfg.batch,
      concurrency: this.cfg.concurrency,
    });

    this.installSignals();

    while (!this.stopping) {
      let jobs: RenderJob[] = [];
      try {
        jobs = await this.client.claim(this.cfg.batch);
      } catch (err) {
        metrics.claimErrors.inc();
        log("warn", "claim failed", { error: String(err) });
        await sleep(this.cfg.idlePollMs);
        continue;
      }

      if (jobs.length === 0) {
        await sleep(this.cfg.idlePollMs);
        continue;
      }

      metrics.claimed.inc(jobs.length);
      await this.processBatch(jobs);
    }

    await this.shutdown();
  }

  // Render a claimed batch with bounded parallelism (a shared cursor over the
  // job list keeps exactly `concurrency` renders in flight).
  private async processBatch(jobs: RenderJob[]): Promise<void> {
    let cursor = 0;
    const worker = async (): Promise<void> => {
      while (cursor < jobs.length && !this.stopping) {
        const job = jobs[cursor++];
        await this.handleJob(job);
      }
    };
    const lanes = Math.min(this.cfg.concurrency, jobs.length);
    await Promise.all(Array.from({ length: lanes }, () => worker()));
  }

  private async handleJob(job: RenderJob): Promise<void> {
    metrics.inflight.inc();
    try {
      const out = await this.renderer.render(job.url);
      const outcome = await this.client.ingest(job, out.finalURL, out.status, out.html);
      switch (outcome.kind) {
        case "indexed":
          metrics.indexed.inc();
          log("info", "rendered → indexed", { id: job.id, url: job.url, text_len: outcome.result.text_len });
          break;
        case "duplicate":
          metrics.duplicate.inc();
          log("info", "rendered → duplicate", { id: job.id, url: job.url });
          break;
        case "empty":
          // No extractable text: crawler left the job un-marked. Report a
          // retryable failure so attempts advance instead of relying on the reaper.
          metrics.empty.inc();
          log("warn", "rendered → empty", { id: job.id, url: job.url });
          await this.reportFailure(job);
          break;
        case "error":
          metrics.failed.inc();
          log("error", "ingest error", { id: job.id, url: job.url, status: outcome.status, message: outcome.message });
          await this.reportFailure(job);
          break;
      }
    } catch (err) {
      // Navigation/timeout/browser error — never rendered.
      metrics.failed.inc();
      log("error", "render failed", { id: job.id, url: job.url, error: String(err) });
      await this.reportFailure(job);
    } finally {
      metrics.inflight.dec();
    }
  }

  private async reportFailure(job: RenderJob): Promise<void> {
    try {
      await this.client.complete(job.id, false, this.cfg.retryOnFailure);
    } catch (err) {
      // Lease will expire and the crawler's reaper will requeue it anyway.
      log("warn", "complete(failure) call failed", { id: job.id, error: String(err) });
    }
  }

  private startHealthServer(): void {
    this.health = createServer((req, res) => {
      if (req.url === "/metrics") {
        res.writeHead(200, { "content-type": "text/plain; version=0.0.4" });
        res.end(renderMetrics());
        return;
      }
      if (req.url === "/healthz" || req.url === "/readyz") {
        res.writeHead(200, { "content-type": "application/json" });
        res.end(JSON.stringify({ status: "ok", service: "browser-worker" }));
        return;
      }
      res.writeHead(404);
      res.end();
    });
    this.health.listen(this.cfg.healthPort, () => {
      log("info", "health server listening", { port: this.cfg.healthPort });
    });
  }

  private installSignals(): void {
    const onSignal = (sig: string) => {
      log("info", "shutdown signal received", { signal: sig });
      this.stopping = true;
    };
    process.on("SIGTERM", () => onSignal("SIGTERM"));
    process.on("SIGINT", () => onSignal("SIGINT"));
  }

  private async shutdown(): Promise<void> {
    log("info", "shutting down");
    await this.renderer.stop().catch(() => {});
    await new Promise<void>((resolve) => {
      if (!this.health) return resolve();
      this.health.close(() => resolve());
    });
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
