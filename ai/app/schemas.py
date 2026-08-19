"""Pydantic request/response models for the Search API (docs/13-API.md §1)."""

from __future__ import annotations

from pydantic import BaseModel, Field


class SearchFilters(BaseModel):
    date_from: str | None = None
    date_to: str | None = None
    source_types: list[str] = Field(default_factory=list)
    languages: list[str] = Field(default_factory=list)
    domains_include: list[str] = Field(default_factory=list)
    domains_exclude: list[str] = Field(default_factory=list)


class SearchOptions(BaseModel):
    stream: bool = True
    max_sources: int = 12
    freshness: str = "auto"       # auto | fresh | any
    synthesize: bool = True       # false → retrieve-only
    expand: bool = True           # LLM query expansion/decomposition (recall)


class SearchRequest(BaseModel):
    query: str
    filters: SearchFilters = Field(default_factory=SearchFilters)
    options: SearchOptions = Field(default_factory=SearchOptions)


class RetrieveRequest(BaseModel):
    query: str
    filters: SearchFilters = Field(default_factory=SearchFilters)
    max_sources: int = 20
    expand: bool = True           # LLM query expansion/decomposition (recall)
    freshness: str = "auto"       # auto | fresh | any — recency weighting


class ResultItem(BaseModel):
    chunk_id: str
    document_id: int
    url: str
    domain: str | None = None
    title: str | None = None
    text: str | None = None
    published_at: str | None = None
    source_type: str | None = None
    score: float
    lexical_rank: int | None = None
    vector_rank: int | None = None


class RetrieveResponse(BaseModel):
    query: str
    results: list[ResultItem]
    coverage: dict
    reranked: bool
    degraded: dict
    expansions: list[str] = Field(default_factory=list)
    intent: str = "factual"
    freshness: str = "auto"
