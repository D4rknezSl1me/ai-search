"""Offline tests for the retrieval-backed search adapter + a full end-to-end run
of the discovery loop over a fake corpus (docs/15 §4)."""

from __future__ import annotations

import asyncio
from dataclasses import dataclass

from app.entity_brief import Budget, Relationship, TargetBrief
from app.entity_orchestrator import RunConfig, discover_entity
from app.entity_queries import generate_queries
from app.entity_search import make_search, make_retrieval_search, make_searxng_discover


@dataclass
class Doc:
    text: str = ""
    url: str = ""
    title: str = ""


def _corpus_retrieve(docs_for):
    """Fake retrieve: returns docs whose predicate matches the query text."""
    async def retrieve(query: str):
        return [d for pred, d in docs_for if pred(query)]
    return retrieve


# ---------------------------------------------------------------- adapter -----

def test_adapter_extracts_candidates_from_retrieved_docs():
    b = TargetBrief(surname="Rossi", known_attributes={"city": "Como"})
    retrieve = _corpus_retrieve([
        (lambda q: True, Doc(text="Giulia Rossi lives in Como.", url="https://ex.com/1")),
    ])
    search = make_retrieval_search(b, retrieve)
    from app.entity_queries import generate_queries
    q = generate_queries(b)[0]
    cands = asyncio.run(search(q))
    assert cands and "Rossi" in cands[0].name
    assert cands[0].attributes.get("city") == "Como"


# ---------------------------------------------- full end-to-end integration ---

def test_end_to_end_resolves_target_over_fake_corpus():
    # The whole loop: brief → generate_queries → retrieval-backed search →
    # extract → resolve → refine, with no network/model. The corpus contains the
    # target's page (name + school + city + the sibling co-mention) plus a
    # same-surname distractor lacking the relationship.
    brief = TargetBrief(
        surname="Rossi",
        known_attributes={"school": "Liceo Volta", "city": "Como"},
        relationships=[Relationship("sibling_of", "Marco Rossi")],
        budget=Budget(max_hops=3, max_fetches=20),
    )
    target_page = Doc(
        text=("Giulia Rossi, del Liceo Volta di Como, con il fratello Marco Rossi. "
              "Instagram: @giulia.rossi"),
        url="https://news.local/giulia",
    )
    distractor = Doc(text="Giulia Rossi, avvocato a Milano, Liceo Manzoni.",
                     url="https://other/giulia")
    retrieve = _corpus_retrieve([
        (lambda q: "Marco Rossi" in q or "Volta" in q or "Como" in q, target_page),
        (lambda q: "Rossi" in q, distractor),
    ])
    search = make_retrieval_search(brief, retrieve)
    res = asyncio.run(discover_entity(brief, search, config=RunConfig()))

    assert res.best is not None
    assert res.best.match.is_match()
    assert "Marco Rossi" in " ".join(res.best.candidate.co_mentions) \
        or "relationship" in res.best.match.signals
    # The target (with the sibling tie + school) outranks the Milano distractor.
    assert res.best.candidate.source_url == "https://news.local/giulia"


# -------------------------------------------------- multi-source union / searxng ---

def test_make_search_unions_sources_and_dedupes_by_url():
    b = TargetBrief(surname="Rossi", known_attributes={"city": "Como"})
    # Same URL from two sources → extracted once; distinct URL → also included.
    src_a = _corpus_retrieve([(lambda q: True, Doc(text="Giulia Rossi, Como.", url="u1"))])
    src_b = _corpus_retrieve([
        (lambda q: True, Doc(text="Giulia Rossi, Como.", url="u1")),   # dup URL
        (lambda q: True, Doc(text="Anna Rossi, Como.", url="u2")),
    ])
    search = make_search(b, src_a, src_b)
    q = generate_queries(b)[0]
    cands = asyncio.run(search(q))
    urls = sorted({c.source_url for c in cands})
    assert urls == ["u1", "u2"]


def test_make_search_skips_a_failing_source():
    b = TargetBrief(surname="Rossi", known_attributes={"city": "Como"})
    async def broken(query):
        raise RuntimeError("source down")
    good = _corpus_retrieve([(lambda q: True, Doc(text="Giulia Rossi, Como.", url="u1"))])
    search = make_search(b, broken, good)
    cands = asyncio.run(search(generate_queries(b)[0]))
    assert cands and cands[0].source_url == "u1"   # the dead source didn't blank it


def test_searxng_discover_parses_results():
    class _Resp:
        def json(self):
            return {"results": [
                {"url": "https://a/1", "title": "Giulia Rossi", "content": "Como snippet"},
                {"url": "", "title": "skip", "content": "no url"},   # dropped
            ]}
    async def http_get(url, **kw):
        assert "format=json" in url or kw.get("params", {}).get("format") == "json"
        return _Resp()
    discover = make_searxng_discover("http://searxng:8080/", http_get, max_urls=5)
    docs = asyncio.run(discover("Rossi Como"))
    assert len(docs) == 1
    assert docs[0].url == "https://a/1" and docs[0].text == "Como snippet"


def test_searxng_discover_degrades_on_error():
    async def http_get(url, **kw):
        raise RuntimeError("searxng down")
    discover = make_searxng_discover("http://searxng:8080", http_get)
    assert asyncio.run(discover("q")) == []


def test_end_to_end_no_match_returns_gracefully():
    brief = TargetBrief(surname="Rossi", known_attributes={"school": "Liceo Volta"})
    retrieve = _corpus_retrieve([
        (lambda q: True, Doc(text="Marco Bianchi studies at Liceo Manzoni.",
                             url="https://x/1")),
    ])
    search = make_retrieval_search(brief, retrieve)
    res = asyncio.run(discover_entity(brief, search, config=RunConfig()))
    assert res.status == "no_match"
    assert res.best is None or res.best.score < 0.6
