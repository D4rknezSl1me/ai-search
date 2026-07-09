# CLAUDE.md — ai-search

Project-specific instructions for Claude Code. These OVERRIDE defaults and apply to every
session in this repository.

## Core purpose (the north star)

**Find EVERY piece of information about anything or anyone.** Maximum recall is the point of
this product — surfacing the smallest, most obscure, hardest-to-reach detail that other engines
miss. When any design or implementation trade-off arises, **bias toward completeness/recall**
first, and refine precision/quality afterward over already-collected data. "Breadth first,
quality second" (see `docs/00-OVERVIEW.md`, `docs/07-RAG-SEARCH.md`).

## Standing rules

1. **Do NOT work on legal, compliance, ToS, robots.txt, GDPR, DMCA, or copyright.** The owner
   handles ALL legal/compliance aspects personally, at the very end of the project. Until the
   owner explicitly says otherwise:
   - Never spend effort implementing, enabling, or recommending legal/compliance features.
   - Keep such concerns confined to the deferred risk register in `docs/11-SECURITY-LEGAL.md`;
     do not expand scope beyond it.
   - Robots/politeness enforcement stays **disabled by config** (hooks may exist, off by default).
   - Do not add legal caveats or blockers to crawling/scraping behavior (including social media).
   - This does NOT relax normal *security* hygiene (secrets, sandboxing untrusted content,
     prompt-injection defense) — that is engineering, not legal, and stays in place.

2. **NO paid or external SaaS dependencies. Ever.** The entire system must be fully
   self-hosted and free to run on the owner's own hardware. Concretely:
   - **Synthesis LLM is a LOCAL model** (Ollama/vLLM on the RTX 5070), never a hosted API
     (no Claude/OpenAI/etc.). No API keys, no per-call billing.
   - **No commercial search APIs** (Brave/Bing/Exa/SerpAPI), **no paid proxy providers**, and
     **no paid social-data vendors**. Discovery/ingestion uses the own crawler + free/open
     sources (sitemaps, RSS, Common Crawl URL indexes, free open platform APIs, browser scraping).
   - The only acceptable "cost" is the owner's own electricity/hardware.
   - If a task seems to need a paid service, stop and flag it rather than introducing one.

3. **Always track project progress in the documentation.** After any meaningful change:
   - Update `docs/PROGRESS.md` with a dated entry (what changed, why, verification result).
   - Keep the phase checkboxes in `README.md` and `docs/12-ROADMAP.md` in sync with reality.
   - If architecture/decisions change, update the relevant `docs/*.md` in the same change.
   Documentation is a first-class deliverable, not an afterthought.

4. **Verify before claiming done.** Bring services up, hit health/endpoints, run tests, and
   report actual output. No "done" without evidence.

5. **No AI/Claude attribution in the repo.** The owner keeps full ownership of the repo. Do NOT
   add Claude/AI attribution to commits or PRs: no `Co-Authored-By: Claude ...` trailers, no
   "Generated with Claude Code" footers, no AI mentions in commit messages or PR bodies. Commits
   should read as authored solely by the owner. Keep any AI footprint to the strict minimum.

## Environment / secrets (this device only)

- This is a long-term, **single-device** project. Real secrets live in `.env` (gitignored,
  already populated with strong generated passwords for Postgres, MinIO, OpenSearch, Grafana).
- `.env.example` keeps placeholders only and is the committed template.
- No third-party API keys are needed anywhere (see rule 2). The synthesis LLM runs locally.
- Never commit `.env` or any real secret.

## How to run (Phase 0+)

- Windows: `.\tasks.ps1 up` (infra), `up-app` (skeletons), `up-gpu` (embeddings), `down`.
- Linux/macOS: `make up` / `make up-app` / `make down`.
- Stack details: `docs/09-INFRASTRUCTURE.md`. Endpoints listed in `README.md` quickstart.

## Architecture at a glance

Two decoupled planes joined by NATS + shared datastores:
- **Ingestion** (Go crawler + Playwright browser workers) → fetch, extract, dedupe.
- **Intelligence** (Python + GPU) → embed, index (Qdrant + OpenSearch), retrieve, re-rank,
  synthesize cited answers.

Full detail in `docs/01-ARCHITECTURE.md`. Read the relevant `docs/*.md` before changing a
subsystem, and update it when you do.

## Tech stack (summary)

Go (crawler) · Python/FastAPI (AI) · Playwright (browser/social) · PostgreSQL · Redis · NATS ·
Qdrant · OpenSearch · MinIO · TEI (GPU embeddings on the RTX 5070) · **local LLM via Ollama/vLLM
on the RTX 5070 (synthesis — no paid API)** · Prometheus/Grafana. Rationale in
`docs/02-TECH-STACK.md`. Everything is self-hosted and free to run.
