"""Query understanding (docs/07-RAG-SEARCH.md §2).

Turn one natural-language question into a small set of retrieval queries: the
original (normalized) plus LLM-generated paraphrases and decomposed
sub-questions. Recall-first (CLAUDE.md north star): more phrasings and
sub-questions surface documents that a single literal query misses —
decomposition is precisely what makes "find everything about X" actually find
everything. Every step degrades to "just the original query", so query
understanding can never fail a search.

Pure and dependency-injected: the LLM is passed in as an async callable, so the
parsing/cleaning/planning logic is unit-testable offline with no model or
network. The retrieval layer supplies a real adapter over the local Ollama
client; tests supply a fake.
"""

from __future__ import annotations

import json
import logging
import re
from dataclasses import dataclass, field
from typing import Awaitable, Callable

log = logging.getLogger("understand")

# An LLM adapter: given chat messages + a temperature, return the raw assistant
# text. Kept abstract so this module never imports the concrete client.
LLMFn = Callable[[list[dict[str, str]], float], Awaitable[str]]

EXPANSION_SYSTEM = (
    "You rewrite a user's search query to maximize retrieval recall over a large "
    "document index. Given the question, produce diverse alternative phrasings and "
    "synonyms, and — when the question has multiple parts — break it into focused "
    "sub-questions that can be searched independently. "
    "Return ONLY a compact JSON array of short search-query strings and nothing "
    "else: no prose, no numbering, no keys. Each string is a standalone query. Do "
    "not repeat the original query verbatim. Keep each under 20 words."
)

_BULLET = re.compile(r"^\s*(?:[-*•]|\d+[.)]|\(\d+\)|q\d+[:.)]?)\s+", re.IGNORECASE)
_WS = re.compile(r"\s+")
_MAX_QUERY_CHARS = 200


def normalize_query(query: str) -> str:
    """The §2 "normalize" step: trim and collapse internal whitespace.

    Kept deliberately conservative — spell-correction / language detection are
    future work and must never silently change the user's terms (recall-first).
    """
    return _WS.sub(" ", (query or "").strip())


def _strip_line(s: str) -> str:
    s = s.strip().strip("`")
    # Remove list numbering/bullets first ("1.", "-", "2)") so any wrapping
    # quote it precedes ('1. "foo"') is exposed for the next step.
    s = _BULLET.sub("", s).strip()
    # Strip a symmetric wrapping quote (but not an internal apostrophe).
    if len(s) >= 2 and s[0] in "\"'" and s[-1] == s[0]:
        s = s[1:-1].strip()
    return _WS.sub(" ", s).strip()


def parse_expansions(text: str) -> list[str]:
    """Parse the model's reply into candidate query strings.

    Tolerant by design: prefer a JSON array (what the prompt asks for), but fall
    back to line-splitting when the model wraps the array in prose or emits a
    plain list — the model output is untrusted and we must not throw here.
    """
    if not text:
        return []
    text = text.strip()

    # 1) Try the largest JSON-array slice we can find.
    start, end = text.find("["), text.rfind("]")
    if 0 <= start < end:
        try:
            data = json.loads(text[start : end + 1])
        except (json.JSONDecodeError, ValueError):
            data = None
        if isinstance(data, list):
            out: list[str] = []
            for item in data:
                if isinstance(item, str):
                    out.append(item)
                elif isinstance(item, dict):
                    # Be lenient about a model that returns objects.
                    for key in ("query", "q", "text", "question"):
                        if isinstance(item.get(key), str):
                            out.append(item[key])
                            break
            cleaned = [s for s in (_strip_line(x) for x in out) if s]
            if cleaned:
                return cleaned

    # 2) Fallback: one query per line.
    return [s for s in (_strip_line(ln) for ln in text.splitlines()) if s]


def clean_expansions(raw: list[str], original: str, max_n: int) -> list[str]:
    """Dedupe and sanitize candidate expansions relative to the original query."""
    if max_n <= 0:
        return []
    seen: set[str] = {normalize_query(original).casefold()}
    out: list[str] = []
    for cand in raw:
        q = normalize_query(cand)
        if not q or len(q) > _MAX_QUERY_CHARS:
            continue
        key = q.casefold()
        if key in seen:
            continue
        seen.add(key)
        out.append(q)
        if len(out) >= max_n:
            break
    return out


@dataclass
class QueryPlan:
    """The set of queries retrieval should run for one user question."""

    original: str
    expansions: list[str] = field(default_factory=list)

    @property
    def queries(self) -> list[str]:
        """Every query to retrieve for — the original first, then expansions."""
        return [self.original, *self.expansions]

    @property
    def expanded(self) -> bool:
        return bool(self.expansions)


def _build_messages(original: str, max_n: int) -> list[dict[str, str]]:
    user = (
        f"Question: {original}\n\n"
        f"Return up to {max_n} diverse search queries as a JSON array of strings."
    )
    return [
        {"role": "system", "content": EXPANSION_SYSTEM},
        {"role": "user", "content": user},
    ]


async def plan_query(
    query: str,
    *,
    expand: bool,
    max_expansions: int,
    llm: LLMFn | None,
    temperature: float = 0.3,
) -> QueryPlan:
    """Build the retrieval plan for a user query.

    Returns just the normalized original when expansion is disabled, no LLM is
    wired, or the model call fails/returns nothing — the search always proceeds.
    """
    original = normalize_query(query)
    if not original or not expand or max_expansions <= 0 or llm is None:
        return QueryPlan(original)
    try:
        content = await llm(_build_messages(original, max_expansions), temperature)
    except Exception:
        log.exception("query expansion LLM call failed; using original query only")
        return QueryPlan(original)
    expansions = clean_expansions(parse_expansions(content), original, max_expansions)
    if expansions:
        log.info("expanded query into %d additional sub-quer(y|ies)", len(expansions))
    return QueryPlan(original, expansions)
