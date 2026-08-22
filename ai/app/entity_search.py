"""Search adapter for the discovery orchestrator (docs/15-DISCOVERY-AGENT.md §4).

The concrete ACT step: turn a planned query into `Candidate`s. This first
adapter is **retrieval-backed** — it runs each query through the existing hybrid
retrieval over the already-indexed corpus, then extracts candidates from the
returned documents (`entity_extract`). It's the fastest path to a working
end-to-end lookup: everything the crawler has already ingested becomes
searchable by the agent with zero new fetch code.

`retrieve` is **dependency-injected** (an async `query → [doc]` callable), so this
adapter — and the whole orchestrator on top of it — stays unit-testable offline
without importing the heavy retrieval/client stack. The FastAPI endpoint supplies
the real adapter over `app.retrieval.retrieve`; a later adapter can additionally
drive live discovery (`POST /internal/discover` → crawl → extract) for URLs not
yet in the index.
"""

from __future__ import annotations

from typing import Any, Awaitable, Callable

from app.entity_brief import TargetBrief
from app.entity_extract import extract_candidates
from app.entity_orchestrator import SearchFn
from app.entity_queries import GeneratedQuery
from app.entity_resolve import Candidate

# A retrieval callable: given a query string, return the matched documents. Each
# doc is any object exposing `.text` / `.url` / `.title` (retrieval candidates and
# ResultItem both do); accessed via getattr so tests can pass lightweight stand-ins.
RetrieveFn = Callable[[str], Awaitable[list[Any]]]


def _attr(doc: Any, name: str) -> str:
    val = getattr(doc, name, "") if not isinstance(doc, dict) else doc.get(name, "")
    return val or ""


def make_retrieval_search(
    brief: TargetBrief,
    retrieve: RetrieveFn,
    *,
    max_candidates_per_doc: int = 3,
) -> SearchFn:
    """Build a `SearchFn` that retrieves for a query and extracts candidates.

    Extraction is anchored on the (stable) surname from `brief`; the orchestrator
    re-scores every candidate against the *current* enriched brief, so learning a
    given name still tightens resolution even though extraction uses the seed name.
    """
    async def search(q: GeneratedQuery) -> list[Candidate]:
        docs = await retrieve(q.text)
        out: list[Candidate] = []
        for doc in docs:
            out.extend(extract_candidates(
                brief,
                text=_attr(doc, "text"),
                url=_attr(doc, "url"),
                title=_attr(doc, "title"),
                platform=q.platform,
                max_candidates=max_candidates_per_doc,
            ))
        return out

    return search
