// Thin HTTP client for the crawler's render control API. The crawler owns the
// queue and the ingestion pipeline; this worker only claims jobs, submits
// rendered DOM, and reports failures. Contracts mirror crawler/internal/api.

export interface RenderJob {
  id: number;
  url: string;
  host: string;
  campaign_id: number;
  reasons: string[] | null;
  attempts: number;
}

interface ClaimResponse {
  claimed: number;
  items: RenderJob[];
}

export interface IngestResult {
  id: number;
  state: string;
  inserted: boolean;
  text_len: number;
  lang: string;
  title: string;
}

/** Outcome of submitting a rendered page. `empty` is the crawler's 422 signal
 * that the DOM produced no text — the job is left un-marked for a retry. */
export type IngestOutcome =
  | { kind: "indexed"; result: IngestResult }
  | { kind: "duplicate"; result: IngestResult }
  | { kind: "empty" }
  | { kind: "error"; status: number; message: string };

export class CrawlerClient {
  constructor(private readonly baseURL: string) {}

  async claim(n: number): Promise<RenderJob[]> {
    const res = await fetch(`${this.baseURL}/internal/render/claim`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ n }),
    });
    if (!res.ok) {
      throw new Error(`claim failed: ${res.status} ${await safeText(res)}`);
    }
    const body = (await res.json()) as ClaimResponse;
    return body.items ?? [];
  }

  async ingest(job: RenderJob, finalURL: string, status: number, html: string): Promise<IngestOutcome> {
    const res = await fetch(`${this.baseURL}/internal/render/ingest`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ id: job.id, url: job.url, final_url: finalURL, status, html }),
    });
    // 422 with the empty-render body is an expected, retryable outcome.
    if (res.status === 422) {
      return { kind: "empty" };
    }
    if (!res.ok) {
      return { kind: "error", status: res.status, message: await safeText(res) };
    }
    const result = (await res.json()) as IngestResult;
    return { kind: result.inserted ? "indexed" : "duplicate", result };
  }

  /** Report a render that never produced usable HTML (navigation error,
   * timeout, or empty DOM). retry lets the crawler reschedule under its cap. */
  async complete(id: number, ok: boolean, retry: boolean): Promise<void> {
    const res = await fetch(`${this.baseURL}/internal/render/complete`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ id, ok, retry }),
    });
    if (!res.ok) {
      throw new Error(`complete failed: ${res.status} ${await safeText(res)}`);
    }
  }
}

async function safeText(res: Response): Promise<string> {
  try {
    return (await res.text()).slice(0, 500);
  } catch {
    return "<no body>";
  }
}
