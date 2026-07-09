# 11 — Security, Legal & Compliance

> **Owner decision:** legal/compliance work is **deferred** until explicitly requested. This
> document is a **risk register + design-hooks list** so the work can be done later without a
> rewrite. Nothing here blocks Phases 0–4. Read it before going to production or taking clients.

## 1. Deferred legal/compliance items (must be addressed before commercial launch)

| Area | Risk | Design hook already in place |
|------|------|------------------------------|
| **robots.txt / crawl-delay** | Ignoring may breach site terms / norms | Parser wired in crawler, disabled by config flag; flip to enable. |
| **Website Terms of Service** | Scraping may violate ToS (esp. social) | Per-source policy field in `sources`; can gate campaigns. |
| **Copyright** | Storing/serving full content may infringe | Store provenance; can switch to snippet-only serving. |
| **GDPR / privacy** | Indexing personal data has obligations (esp. EU, social) | PII tagging hook in enrichment; deletion/erasure API stub. |
| **DMCA / takedowns** | Need a removal process | `documents.dup_of`/soft-delete flag; takedown workflow stub. |
| **Platform API ToS** | Paid API terms restrict use/redistribution | Per-adapter tier config; can restrict to compliant tiers. |
| **Rate/abuse** | Aggressive crawling → complaints/bans/legal notices | Politeness engine present; can tighten globally. |
| **Data retention** | Obligations to delete on request | Retention policy config + erasure job stub. |

**Action when un-deferred:** engage a lawyer for jurisdiction-specific advice (US CFAA/scraping
case law, EU GDPR, platform ToS), then flip the relevant hooks on and implement the stubbed
workflows (erasure, takedown, robots enforcement, snippet-only serving).

## 2. Security (not deferred — baseline from the start)

Even in bootstrap, protect the system itself:

- **Secrets:** `.env` out of git (`.gitignore`); no credentials in code or images. Move to a
  secret manager before multi-host/production.
- **Network:** datastore ports firewalled off the public internet; only the Search API/UI
  exposed, behind TLS (Caddy/Traefik).
- **Auth:** API keys + rate limiting on public endpoints (P4); admin/control APIs on private net.
- **Input validation:** validate/normalize all query and crawl-submit inputs (Pydantic);
  guard against SSRF via the crawl-submit endpoint (block internal IP ranges/metadata endpoints).
- **Dependency hygiene:** pin versions; scan images; update regularly.
- **Isolation:** browser workers run untrusted web content → sandbox containers, no host mounts,
  least privilege, resource caps (a hostile page shouldn't be able to escape or exhaust the host).
- **LLM prompt-injection:** treat retrieved web text as untrusted; the synthesis prompt must
  isolate instructions from content and never execute instructions found in sources.
- **PII in logs:** avoid logging full personal data; scrub query logs per policy.
- **Backups:** encrypted at rest; access-controlled.

## 3. Safety / content

- Pipeline hook to filter or flag illegal/harmful content categories (implement before any
  public-facing product).
- Abuse monitoring on the search API (usage patterns, scraping of *your* API).

## 4. Multi-tenancy (when clients arrive)

- Per-tenant data isolation, API keys, quotas, and audit logs.
- Usage metering for billing.
- Per-tenant data deletion (ties into GDPR erasure).

## 5. Checklist before taking real clients

- [ ] Legal review of crawling/scraping posture for target jurisdictions.
- [ ] Robots/ToS policy decided and enforced per source.
- [ ] Copyright posture (snippet vs full) decided.
- [ ] GDPR: lawful basis, privacy policy, erasure workflow live.
- [ ] Takedown/DMCA process live.
- [ ] Secrets in a manager; datastores firewalled; TLS everywhere public.
- [ ] Auth, rate limiting, quotas, audit logs on the API.
- [ ] Content-safety filtering active.
- [ ] Incident response + backup restore tested.
