# 10 — Operations & Observability

## 1. Metrics (Prometheus)

**Crawler**
- `fetch_total{status,host}`, `fetch_bytes_total`, `fetch_duration_seconds`
- `frontier_size{state}`, `block_rate{host,platform}`
- `browser_fetch_ratio`, `render_duration_seconds`

**Pipeline**
- `extract_total{result}`, `dedup_dropped_total`
- `embed_batch_duration_seconds`, `embed_queue_depth`, `gpu_utilization`, `gpu_mem_used`
- `index_upsert_total{store}`, `reconcile_repairs_total`

**Search API**
- `search_requests_total{intent}`, `search_latency_seconds{stage}` (understand/retrieve/rerank/synthesize)
- `recall_at_k` (from eval runs), `groundedness_score`
- `llm_tokens_total{dir}`, `llm_gpu_ms_total`, `llm_queue_depth` (local LLM throughput/util)

**Infra**
- disk usage per store, queue depth, service up/down, container restarts.

## 2. Dashboards (Grafana)

- **Crawl health:** throughput, error/block rates, frontier size, static vs browser mix.
- **Pipeline:** extract rate, dedupe ratio, embed throughput, GPU util/mem, index lag.
- **Search:** QPS, latency by stage, local-LLM throughput/GPU time per query, confidence dist.
- **Capacity:** disk by store & growth trend, RAM/CPU, VRAM.

## 3. Logging

- Structured JSON logs (level, service, trace_id, entity ids).
- Aggregated in Loki (or OpenSearch). Correlate with traces via `trace_id`.
- Sampling on high-volume fetch logs; full logs on errors.

## 4. Tracing

- OpenTelemetry spans across planes: fetch → extract → embed → index, and
  query → retrieve → rerank → synthesize. Essential for latency debugging.

## 5. Alerting

**Implemented (Phase 4):** `deploy/alerts.yml` — Prometheus alerting rules, loaded via `rule_files`
in `deploy/prometheus.yml` and mounted into the Prometheus container. They evaluate against the
metrics the services already export and surface on the Prometheus `/alerts` page (Alertmanager
routing/paging is deferred). Groups:
- **service_health** — `CrawlerDown`, `AiApiDown` (critical), `BrowserWorkerDown` (warning), from `up`.
- **crawl_health** — `HighFetchErrorRate`, `HighHTTP5xxRate` (target throttling), `HighHTTP4xxRate`
  (anti-bot blocking), as ratios over `crawler_fetch_*` (NaN-safe: a quiet system never fires).
- **pipeline_health** — `RenderQueueFailing`, `BrowserWorkerRenderFailing`,
  `BrowserWorkerProxyPoolExhausted` (all proxies in cooldown), `IndexingBacklogGrowing`
  (`aisearch_documents_pending > 1000`).
- **discovery_health** — `SocialAdapterHighErrorRate` (per-adapter, likely blocked / API change).

Still to add: disk high-water mark, GPU OOM / LLM saturation, search-latency P95 regression, and
Alertmanager routing for actual paging.

## 6. Backups & recovery

**Implemented (Phase 4):** `deploy/backup.ps1` — one-command self-hosted backup to a timestamped
folder under `./backups/` (all local; no cloud, CLAUDE.md rule 2):

| Store | Method (script) | Notes |
|-------|-----------------|-------|
| Postgres | hot logical `pg_dump -Fc` → `postgres.dump` | consistent; restore with `pg_restore` |
| Postgres (raw) | volume tar `pgdata.tgz` | fallback |
| Qdrant | volume tar `qdrantdata.tgz` | vectors |
| OpenSearch | volume tar `osdata.tgz` | lexical index |
| MinIO | volume tar `miniodata.tgz` | clean-text blobs |
| Redis/NATS/Prom/Grafana/sessions | volume tar (with `-IncludeExtras`) | rebuildable / optional |

```powershell
./deploy/backup.ps1                       # hot backup of the data stores
./deploy/backup.ps1 -Cold -IncludeExtras  # stop stores first (fully consistent) + extras
```

Run it nightly (Task Scheduler / cron). Restore (see the runbook below): recreate the volumes, untar
each `*.tgz` back into its volume, and `pg_restore` the dump. Test restores quarterly.

**Restore runbook.** With the stack down: for each store, `docker run --rm -v ai-search_<vol>:/dst
-v <backupdir>:/backup alpine sh -c "rm -rf /dst/* && tar xzf /backup/<vol>.tgz -C /dst"`; bring the
stack up; for Postgres prefer the logical dump — `docker exec -i ai-search-postgres-1 pg_restore -U
<user> -d <db> --clean --if-exists < postgres.dump`. Verify with `/v1/coverage` (doc/chunk counts)
and a smoke query.

## 7. Runbooks (index)

- **Restart a plane** without data loss (drain queue, stop consumers, restart).
- **Reindex** from stored text (re-embed after model change) without re-crawl.
- **Recover** a downed datastore from snapshot (see §6 restore runbook: untar the volume backup /
  `pg_restore` the dump, then verify via `/v1/coverage`).
- **Scale** fetchers/embedders (add workers, rebalance).
- **Repair** a broken social adapter (fixtures, contract tests, redeploy).
- **Disk pressure** response (extend, tier, prune raw, tighten dedupe).

## 8. Eval & quality gates

- Nightly eval run on the labeled set; publish recall@k, groundedness, citation accuracy to a
  dashboard and alert on regression beyond a tolerance.
- Any change to embedding model, chunker, fusion weights, or prompt must pass the eval gate.

## 9. Resource management (no monetary cost — self-hosted)

- There is **no paid API bill**; the only running cost is the owner's electricity/hardware. So
  "cost management" here means managing **GPU time, VRAM, and disk**, not dollars.
- Track LLM tokens + GPU time per query (`usage` in API responses) to watch throughput, not spend.
- Share the single GPU deliberately (embeddings vs re-ranker vs synthesis) — see
  [09-INFRASTRUCTURE.md](09-INFRASTRUCTURE.md) §3; throttle embed workers if query latency suffers.
- Cache aggressively (query, embedding, LLM prefix) to save GPU cycles.

## 10. Capacity planning

- Track growth curves for disk, vector count, index size, and GPU throughput.
- Forecast when the single-node setup saturates → triggers the scale-out steps in
  [09-INFRASTRUCTURE.md](09-INFRASTRUCTURE.md).
