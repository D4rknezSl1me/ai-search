# 09 — Infrastructure & Deployment

## 1. Hardware inventory (current)

| Machine | Role | Notes |
|---------|------|-------|
| **Workstation (RTX 5070, 12 GB VRAM)** | GPU services: embeddings (TEI), re-ranker, optional local LLM | The only GPU; pin GPU workloads here. |
| **Home server** | Datastores + queue + crawler + API | Runs Postgres, Qdrant, OpenSearch, MinIO, NATS, fetchers. |
| **(Powerful PC)** | Overflow crawler/browser workers, dev | Browser workers are CPU/RAM heavy — good fit here. |

Networking: services communicate over the LAN; a single `docker-compose`/overlay network in dev.

## 2. Service placement

```
Workstation (GPU)                 Home server                        Powerful PC
─────────────────                 ───────────                        ───────────
 TEI (embeddings)                  PostgreSQL                         Browser workers (Playwright)
 Re-ranker                         Qdrant                             Extra Go fetchers
 Local LLM (Ollama/vLLM)           OpenSearch                         Dev environment
                                   MinIO
                                   NATS (JetStream)
                                   Redis
                                   Go fetchers
                                   FastAPI (Search API)
                                   Prometheus + Grafana + Loki
```

GPU services expose HTTP/gRPC over the LAN so the Search API and embed workers call them
regardless of host.

## 3. GPU allocation (RTX 5070, 12 GB) — what actually fits

All AI runs locally — **no paid API** (per `CLAUDE.md`). The 12 GB budget must hold embeddings,
re-ranker, and the synthesis LLM.

| Workload | VRAM (approx) | Notes |
|----------|---------------|-------|
| Embedding model (bge-large/m3, fp16, batched via TEI) | ~2–4 GB | Primary GPU job at index time. |
| Cross-encoder re-ranker (fp16) | ~1–2 GB | Runs on query shortlists only. |
| Local synthesis LLM (8B, 4-bit quantized, Ollama/vLLM) | ~5–6 GB | Default. Fits alongside the above. |

**Fitting it in 12 GB — two workable modes:**

1. **Co-resident (recommended default):** TEI embeddings (~3 GB) + re-ranker (~1.5 GB) + an
   **8B 4-bit** LLM (~5–6 GB) ≈ ~10–11 GB. All stay loaded; querying and light indexing coexist.
2. **Time-shared (for a bigger 14B model):** a 14B 4-bit model is ~9 GB. Run it when embeddings
   aren't saturating the GPU (Ollama auto-loads/unloads models on demand; bulk embedding jobs are
   scheduled in windows separate from heavy query load).

Bootstrap recommendation: **co-resident mode with an 8B-class instruct model via Ollama** for
synthesis, embeddings + re-ranker on the same card. Move the LLM to a second GPU / vLLM when the
project scales. Model choice is config-driven (`EMBED_MODEL_ID`, `LLM_MODEL`) and swappable.

> Note: bulk embedding (index time) and synthesis (query time) mostly happen at different times,
> so contention is manageable on one card. If query latency suffers during large crawls, throttle
> the embed workers (backpressure) or run embeddings in off-peak windows.

## 4. Containerization

- All services shipped as Docker images; orchestrated with `docker-compose` in dev/bootstrap.
- **NVIDIA Container Toolkit** for GPU passthrough to TEI/re-ranker/vLLM containers.
- One `.env` for config/secrets (DB creds, service passwords, model ids); no third-party API
  keys needed. Never commit real secrets.
- Named volumes for stateful services (Postgres, Qdrant, OpenSearch, MinIO) so data survives
  container recreation.

## 5. `docker-compose` services (Phase 0)

| Service | Image | Ports | Volume |
|---------|-------|-------|--------|
| postgres | postgres:16 | 5432 | pgdata |
| redis | redis:7 | 6379 | — |
| qdrant | qdrant/qdrant | 6333/6334 | qdrant_data |
| opensearch | opensearchproject/opensearch:2 | 9200 | os_data |
| minio | minio/minio | 9000/9001 | minio_data |
| nats | nats:2 (JetStream) | 4222/8222 | nats_data |
| tei | ghcr.io/huggingface/text-embeddings-inference | 8080 | teicache | (GPU profile)
| llm | ollama/ollama | 11434 | ollamadata | (GPU profile) — local synthesis LLM
| prometheus | prom/prometheus | 9090 | prom_data |
| grafana | grafana/grafana | 3000 | grafana_data |
| crawler | ./crawler (Go, built) | 8090 | — | (app profile)
| ai-api | ./ai (Python, built) | 8000 | — | (app profile)

## 6. Configuration management

- `config/` holds default YAML; `.env` overrides per environment.
- Campaign configs in Postgres (`campaigns.config`) or `config/campaigns/*.yaml`.
- Model ids/revisions pinned in config; changing them is a documented migration.

## 7. Resource limits & backpressure

- Bounded queues (NATS max in-flight) so the crawler can't outrun indexing.
- Per-service CPU/RAM limits in compose to protect the host.
- Disk watermark alerts (see storage budgeting in [06](06-DATA-MODEL.md)); crawler pauses new
  fetches above a high-water mark.

## 8. Scale-out path (more owned hardware — no paid services)

1. Move datastores to dedicated nodes; enable Qdrant/OpenSearch sharding & replicas.
2. Add crawler/browser worker nodes (self-hosted machines / cheap owned hardware) behind a
   **self-run** proxy pool.
3. Add GPU node(s) for embeddings/inference and the local LLM; put TEI/vLLM behind a load balancer.
4. Promote orchestration from compose to **Kubernetes/Nomad**; NATS/Postgres clustered.

No component requires redesign to scale — only replication and more workers. Scaling stays within
the **no-paid-services** policy: more of the owner's own hardware, not managed SaaS or paid feeds.

## 9. Networking & security (baseline)

- Services bound to the LAN / private network; only the Search API (and later UI) exposed.
- Reverse proxy (Caddy/Traefik) with TLS for any public endpoint.
- Secrets via `.env` now, Vault/SM later. Firewall datastore ports off the public internet.
