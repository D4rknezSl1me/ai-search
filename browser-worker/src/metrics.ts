// Minimal Prometheus text-format metrics — no dependency needed. The crawler
// scrapes /metrics; this worker exposes the same content type so Prometheus can
// treat it as one more target (mirrors crawler_render_* naming with a
// browserworker_ prefix so the two lanes are distinguishable in Grafana).

class Counter {
  private value = 0;
  constructor(
    readonly name: string,
    readonly help: string,
  ) {}
  inc(by = 1): void {
    this.value += by;
  }
  render(): string {
    return (
      `# HELP ${this.name} ${this.help}\n` +
      `# TYPE ${this.name} counter\n` +
      `${this.name} ${this.value}\n`
    );
  }
}

class Gauge {
  private value = 0;
  constructor(
    readonly name: string,
    readonly help: string,
  ) {}
  set(v: number): void {
    this.value = v;
  }
  inc(by = 1): void {
    this.value += by;
  }
  dec(by = 1): void {
    this.value -= by;
  }
  render(): string {
    return (
      `# HELP ${this.name} ${this.help}\n` +
      `# TYPE ${this.name} gauge\n` +
      `${this.name} ${this.value}\n`
    );
  }
}

export const metrics = {
  claimed: new Counter("browserworker_claimed_total", "Render jobs leased from the crawler."),
  indexed: new Counter("browserworker_rendered_indexed_total", "Rendered pages that landed a new document."),
  duplicate: new Counter("browserworker_rendered_duplicate_total", "Rendered pages that deduped against an existing document."),
  empty: new Counter("browserworker_rendered_empty_total", "Renders that produced no extractable text (retryable)."),
  failed: new Counter("browserworker_render_failed_total", "Renders that errored (navigation/timeout/etc)."),
  inflight: new Gauge("browserworker_inflight", "Pages currently being rendered."),
  claimErrors: new Counter("browserworker_claim_errors_total", "Failed claim calls against the crawler."),
  sessionCreated: new Counter("browserworker_session_created_total", "New per-host sessions minted (no prior state)."),
  sessionResumed: new Counter("browserworker_session_resumed_total", "Renders that resumed an existing per-host session."),
  sessionRotated: new Counter("browserworker_session_rotated_total", "Sessions rotated after exceeding their TTL."),
};

export function renderMetrics(): string {
  return [
    metrics.claimed,
    metrics.indexed,
    metrics.duplicate,
    metrics.empty,
    metrics.failed,
    metrics.inflight,
    metrics.claimErrors,
    metrics.sessionCreated,
    metrics.sessionResumed,
    metrics.sessionRotated,
  ]
    .map((m) => m.render())
    .join("\n");
}
