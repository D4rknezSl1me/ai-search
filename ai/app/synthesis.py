"""Grounded synthesis (docs/07-RAG-SEARCH.md §7–8).

Builds a numbered-source prompt, streams an answer from the local LLM (Ollama),
and verifies that inline [n] citations map to real provided sources. The prompt
forbids ungrounded claims and asks the model to flag insufficient evidence and
surface conflicts — honesty over false certainty.
"""

from __future__ import annotations

import json
import re
from dataclasses import dataclass

from . import clients
from .retrieval import Candidate

SYSTEM_PROMPT = (
    "You are a precise research assistant. Answer the user's question using ONLY "
    "the numbered sources provided. Cite every claim inline with bracketed numbers "
    "like [1] or [2][3] that refer to those sources. Do not use outside knowledge. "
    "If the sources do not contain enough information to answer, say so plainly "
    "instead of guessing. When sources disagree, present the differing accounts and "
    "cite each. Be concise and factual."
)

_CITE = re.compile(r"\[(\d+)\]")


def build_context(candidates: list[Candidate]) -> str:
    blocks = []
    for i, c in enumerate(candidates, start=1):
        meta = []
        if c.title:
            meta.append(c.title)
        if c.domain:
            meta.append(c.domain)
        if c.published_at:
            meta.append(str(c.published_at)[:10])
        header = f"[{i}] " + " | ".join(meta) if meta else f"[{i}]"
        text = (c.text or "").strip()
        blocks.append(f"{header}\nURL: {c.url}\n{text}")
    return "\n\n".join(blocks)


def build_messages(query: str, candidates: list[Candidate]) -> list[dict[str, str]]:
    context = build_context(candidates)
    user = (
        f"Sources:\n{context}\n\n"
        f"Question: {query}\n\n"
        "Answer using only the sources above, with inline [n] citations."
    )
    return [
        {"role": "system", "content": SYSTEM_PROMPT},
        {"role": "user", "content": user},
    ]


async def stream_tokens(query: str, candidates: list[Candidate]):
    """Yield answer text deltas from the local LLM."""
    messages = build_messages(query, candidates)
    async for line in clients.llm_chat_stream(messages, options={"temperature": 0.2}):
        try:
            obj = json.loads(line)
        except json.JSONDecodeError:
            continue
        delta = obj.get("message", {}).get("content", "")
        if delta:
            yield delta


@dataclass
class Citation:
    n: int
    url: str
    title: str | None
    published_at: str | None
    snippet: str
    score: float
    source_type: str | None


def build_citations(answer: str, candidates: list[Candidate]) -> tuple[list[Citation], list[int]]:
    """Return citations for the source numbers actually referenced in the answer,
    dropping any [n] that points outside the provided set (verification)."""
    used = sorted({int(m) for m in _CITE.findall(answer)})
    citations: list[Citation] = []
    valid: list[int] = []
    for n in used:
        if 1 <= n <= len(candidates):
            c = candidates[n - 1]
            snippet = (c.text or "").strip().replace("\n", " ")
            citations.append(Citation(
                n=n, url=c.url, title=c.title, published_at=c.published_at,
                snippet=snippet[:280], score=round(c.score, 4), source_type=c.source_type,
            ))
            valid.append(n)
    return citations, valid


def confidence(candidates: list[Candidate], used_ns: list[int], reranked: bool) -> float:
    """Coarse confidence: more grounded citations + strong top score → higher."""
    if not candidates or not used_ns:
        return 0.0
    coverage = min(len(used_ns) / max(len(candidates), 1), 1.0)
    top = candidates[0].score
    top_norm = 1 / (1 + pow(2.718281828, -top)) if reranked else min(max(top * 20, 0.0), 1.0)
    return round(0.5 * coverage + 0.5 * top_norm, 3)
