# 15 — Agentic Entity Discovery (targeted deep lookups)

The discovery sources in [04-CRAWLER §7](04-CRAWLER.md) widen the *mouth of the funnel* for
breadth. This document specifies the complement: a **targeted, agentic discovery loop** for
ultra-specific lookups — *"find the social/real name of the sister of a friend; I know her
surname and her school."* One shallow fan-out won't find that. A planned, multi-hop search will.

This is the concrete design for the **entity-centric discovery pipeline** already named as the
next major discovery build (see [PROGRESS.md](PROGRESS.md), 2026-08-20 owner direction).

## 1. Why an agent, not a bigger crawl

Breadth discovery (Common Crawl, sitemaps, metasearch on a single query) is **fan-out**: cast wide,
index everything, answer later. It fails on needle-in-a-haystack targets for three reasons:

1. **The right query isn't known up front.** You have *attributes* (surname, school, city, a
   friend's name), not a query. The winning query — `"Rossi" "Liceo Volta" Como` — has to be
   *composed* from those attributes, and the next query depends on what the last one returned.
2. **Finding the target is multi-hop.** School directory → sports roster → a name → a linked
   social handle. No single page has the answer; the path is discovered as you go.
3. **Most candidates are the wrong person.** "M. Rossi" matches thousands of people. Recall-first
   still means we must *resolve* which candidate is the target before we chase their profiles,
   or the hop explodes into noise.

The fix is the standard **plan → act → observe → refine** agent loop, with the **local LLM as the
reasoner** and the **existing crawler/discovery tools as the actor**. No paid API, no new
framework — it orchestrates services we already run (CLAUDE.md rule 2).

## 2. The loop

```
Target brief (structured attributes + goal)
   │
   ▼
┌─ PLAN ─────────────────────────────────────────────┐
│ Local LLM proposes the next best action(s):        │
│  • generate dorking-style queries from attributes   │
│  • pick a tool: metasearch / social adapter / render│
│  • decide which candidate/link to expand next       │
└────────────────────────────────────────────────────┘
   │  (structured action, validated against a tool schema)
   ▼
┌─ ACT ──────────────────────────────────────────────┐
│ Execute via EXISTING endpoints (no new fetch code): │
│  • POST /internal/discover      (SearXNG metasearch)│
│  • frontier enqueue + crawl     (fetch/extract)     │
│  • render_queue                 (JS pages)          │
│  • social adapters              (profile/handle)    │
│  • /v1/retrieve                 (search what we have)│
└────────────────────────────────────────────────────┘
   │  fetched + extracted + indexed content (provenance stored)
   ▼
┌─ OBSERVE ──────────────────────────────────────────┐
│ Entity resolution + scoring:                        │
│  • score each candidate against the brief           │
│    (attribute overlap: school, city, surname, ties) │
│  • extract NEW attributes (a handle, a full name,   │
│    an employer) → enrich the brief                  │
│  • extract NEW leads (linked profiles, roster URLs) │
└────────────────────────────────────────────────────┘
   │
   ▼
REFINE brief + frontier of leads ──▶ back to PLAN
   │
   ▼ (stop condition: confident match, budget spent, or no new leads)
Answer: best candidate(s), each with the evidence trail (cited URLs + fetch time)
```

The loop is **bounded** (§5) — recall-first does not mean unbounded. It means: exhaust the leads
that are *plausibly the target* before giving up, not chase every namesake to infinity.

## 3. The target brief (structured input)

The agent is driven by a structured brief, not a free-text sentence. The UI/API collects it; the
LLM may also parse it out of a natural-language request.

```yaml
goal: "social_handle"            # social_handle | real_name | any_info | contact | photos
subject:
  surname: "Rossi"
  given_name: null               # unknown — a thing to FIND
  known_attributes:
    school: "Liceo A. Volta"
    city: "Como, IT"
    approx_age: 22
    language: "it"
  relationships:
    - type: "sibling_of"
      of: "Marco Rossi"          # the friend — a strong disambiguator
  seed_handles: []               # any known handle to expand from
constraints:
  platforms: ["instagram","tiktok","linkedin","facebook","open_web"]
  budget: { max_hops: 4, max_fetches: 200, max_wall_s: 900 }
```

Every field is either a **filter** (narrow candidates) or a **lead** (something to expand). Fields
marked unknown (`given_name: null`) are the *objective*. The `relationships` block is the highest-
signal disambiguator — "sibling of a person we can already find" collapses thousands of namesakes
to a handful.

## 4. Stages in detail

### 4.1 Query generation (PLAN)
The LLM turns attributes into a ranked batch of queries, most-specific first:
- Combinatorial dorks: `"Rossi" "Liceo Volta" Como`, `Rossi Como classe 2003`,
  `site:instagram.com Rossi Como`, `"Marco Rossi" sorella Como`.
- Platform-scoped variants for each allowed platform (open web via metasearch; social via adapter
  search endpoints).
- These run through the **existing** `POST /internal/discover` (metasearch → frontier) and social
  adapter discovery — no new fetch path. Queries and their yield are logged for the eval set.

### 4.2 Candidate extraction + entity resolution (OBSERVE)
This is the new intelligence and the crux of precision. For each fetched page/profile:
- Pull candidate entities (name + surrounding attributes) — NER (Phase 4 enrichment) + LLM read.
- Score `candidate ↔ brief` on **attribute overlap**: matching school, city, age band, surname
  spelling, and — heaviest — a corroborated **relationship** ("appears alongside Marco Rossi").
- A candidate above a confidence floor becomes a **tracked entity** whose profile links are new
  leads; below the floor it's parked (not discarded — recall-first; a later hop may corroborate it).
- Newly learned attributes (a discovered `given_name`, a handle, an employer) are **merged back
  into the brief**, sharpening the next round of queries.

### 4.3 Multi-hop expansion (ACT→OBSERVE, repeated)
Leads are a small internal frontier ordered by candidate confidence × attribute yield. Typical
chains: `school page → roster/yearbook → a full name → metasearch that name → social profile →
followers/tagged posts confirming the sibling tie`. Each hop reuses render + social adapters +
anti-detection already in place. Depth is capped by `budget.max_hops`.

### 4.4 Resolution + answer
Stop when a candidate clears the **match threshold**, the budget is exhausted, or leads dry up.
Return the ranked candidate(s), each with:
- the resolved attributes (handle, real name, platform),
- a **confidence** with the contributing signals spelled out, and
- the **evidence trail**: every source URL + fetch timestamp that supports the match (same
  provenance/citation contract as [07 §8](07-RAG-SEARCH.md) — no uncited claim).

Ambiguity is reported honestly (top-N candidates), never collapsed to a false single answer.

## 5. Guardrails (what keeps recall-first from becoming infinite)

- **Explicit budget** per run (`max_hops`, `max_fetches`, `max_wall_s`) — the loop always
  terminates; partial results are returned, never nothing.
- **Confidence floor to expand**: only candidates plausibly-the-target spawn new hops, so one
  common surname can't fan the frontier into millions of pages.
- **Attribute-anchored queries**: every generated query carries ≥1 discriminating attribute
  (school/city/relationship), so metasearch load stays proportional to the target, not the name.
- **Provenance always** (guiding principle 1): each candidate/attribute records where it came from,
  so a wrong turn is auditable and the eval set can measure precision of resolution.
- **Reuses the anti-detection stack** ([04 §5–6](04-CRAWLER.md)) as-is: per-host pinned identity +
  cookie jar + self-run proxy pool. The agent adds *no* new evasion surface; it only sequences
  existing tools. Blocking/backoff is handled where it already is.

## 6. What to build (delta over today)

Almost every *actor* exists. The new pieces are the orchestration and the resolution intelligence:

1. **Orchestrator service** (`ai/`, drives the loop; local LLM as planner via structured/JSON tool
   calls validated against a schema — degrades to a fixed query-generation heuristic if the LLM is
   down, recall-first).
2. **Target-brief model + API** (`POST /v1/discover/entity` taking the §3 brief; the UI grows a
   "targeted lookup" form alongside the search box).
3. **Entity-resolution scorer** (`candidate ↔ brief` attribute overlap + relationship
   corroboration; reuses Phase 4 NER + the reranker).
4. **Lead frontier + brief-enrichment state** (small per-run store; distinct from the global crawl
   frontier so a targeted run is isolated and resumable).
5. **Eval extension**: a labeled set of solvable targets to measure *resolution* precision/recall,
   not just retrieval (extends the [00 §5](00-OVERVIEW.md) metrics with a "target found @ hop-k").

Items 1–4 are net-new; the fetch/render/social/anti-detection/index substrate underneath them is
already done and verified (Phases 1–3).

## 7. Non-goals / honest limits

- **Login-walled platforms still need credentials** ([14-CREDENTIALS.md](14-CREDENTIALS.md)). The
  agent can *plan* an Instagram/LinkedIn hop, but executing it waits on owner-supplied accounts.
  Until then those hops degrade to open-web + metasearch evidence.
- **Not every target is findable.** If no attribute the subject shares is ever published, no amount
  of planning surfaces it. The agent reports "no confident match within budget" — an honest null,
  consistent with the [00 §3](00-OVERVIEW.md) non-goals.
- **Legal/compliance is out of scope here** (CLAUDE.md rule 1) — deferred to the owner, untouched
  by this design.
