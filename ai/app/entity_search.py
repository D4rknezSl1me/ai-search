"""Search adapters for the discovery orchestrator (docs/15-DISCOVERY-AGENT.md §4).

The concrete ACT step: turn a planned query into `Candidate`s. Two doc *sources*
feed it, and a run can use either or both:

- **retrieval-backed** — run the query through the existing hybrid retrieval over
  the already-indexed corpus (everything the crawler ingested is instantly
  searchable by the agent, zero new fetch code); and
- **live discovery** — query the self-hosted SearXNG metasearch directly to reach
  pages **not yet in the index** (the core "find everything" lever): its result
  snippets are extracted inline, so a targeted lookup isn't limited to what's been
  crawled. No paid API, no key (CLAUDE.md rule 2).

Both are just async `query → [doc]` callables (a `doc` exposes `.text`/`.url`/
`.title`), **dependency-injected**, so the adapters — and the whole orchestrator —
stay unit-testable offline. `make_search` unions any number of sources (dedup by
URL) and extracts candidates from every doc. The FastAPI endpoint supplies the
real retrieval + SearXNG sources.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any, Awaitable, Callable

from app.entity_brief import TargetBrief
from app.entity_extract import extract_candidates
from app.entity_orchestrator import SearchFn
from app.entity_queries import GeneratedQuery
from app.entity_resolve import Candidate

# A doc source: given a query string, return matched documents. Each doc is any
# object exposing `.text` / `.url` / `.title` (retrieval candidates, ResultItem,
# and DiscoveredDoc all do); read via getattr so tests can pass stand-ins.
RetrieveFn = Callable[[str], Awaitable[list[Any]]]


def _attr(doc: Any, name: str) -> str:
    val = getattr(doc, name, "") if not isinstance(doc, dict) else doc.get(name, "")
    return val or ""


def make_search(
    brief: TargetBrief,
    *sources: RetrieveFn,
    max_candidates_per_doc: int = 3,
) -> SearchFn:
    """Build a `SearchFn` that queries every source, unions the docs (dedup by
    URL), and extracts candidates from each.

    Extraction anchors on the (stable) surname from `brief`; the orchestrator
    re-scores every candidate against the *current* enriched brief, so learning a
    given name still tightens resolution even though extraction uses the seed name.
    A failing source is skipped (recall-first — one dead source never blanks a hop).
    """
    async def search(q: GeneratedQuery) -> list[Candidate]:
        seen_urls: set[str] = set()
        out: list[Candidate] = []
        for source in sources:
            try:
                docs = await source(q.text)
            except Exception:
                continue
            for doc in docs:
                url = _attr(doc, "url")
                if url and url in seen_urls:
                    continue
                if url:
                    seen_urls.add(url)
                out.extend(extract_candidates(
                    brief,
                    text=_attr(doc, "text"),
                    url=url,
                    title=_attr(doc, "title"),
                    platform=q.platform,
                    max_candidates=max_candidates_per_doc,
                ))
        return out

    return search


# Back-compat alias: the retrieval-backed single-source form.
def make_retrieval_search(brief: TargetBrief, retrieve: RetrieveFn, **kw) -> SearchFn:
    return make_search(brief, retrieve, **kw)


# --------------------------------------------------------- live SearXNG source ---

@dataclass
class DiscoveredDoc:
    url: str = ""
    title: str = ""
    text: str = ""


# An async HTTP getter: (url, params) → response with `.json()` (httpx.AsyncClient
# fits directly). Injected so this stays testable without a live SearXNG.
HttpGet = Callable[..., Awaitable[Any]]


def make_searxng_discover(base_url: str, http_get: HttpGet, *, max_urls: int = 10) -> RetrieveFn:
    """A live-discovery source: query SearXNG's JSON API and return result snippets
    as docs. Reaches pages not yet indexed. Degrades to [] on any error."""
    base = base_url.rstrip("/")

    async def discover(query: str) -> list[DiscoveredDoc]:
        try:
            resp = await http_get(f"{base}/search", params={"q": query, "format": "json"})
            data = resp.json()
        except Exception:
            return []
        out: list[DiscoveredDoc] = []
        for r in (data.get("results") or [])[:max_urls]:
            url = r.get("url") or ""
            if not url:
                continue
            out.append(DiscoveredDoc(
                url=url,
                title=r.get("title") or "",
                text=r.get("content") or "",   # SearXNG result snippet
            ))
        return out

    return discover
