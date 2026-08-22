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


class DiscoverRelationship(BaseModel):
    type: str
    of: str


class DiscoverSubject(BaseModel):
    surname: str = ""
    given_name: str | None = None
    known_attributes: dict[str, str] = Field(default_factory=dict)
    relationships: list[DiscoverRelationship] = Field(default_factory=list)
    seed_handles: list[str] = Field(default_factory=list)


class DiscoverBudget(BaseModel):
    max_hops: int = 4
    max_fetches: int = 200
    max_wall_s: int = 900


class DiscoverConstraints(BaseModel):
    platforms: list[str] = Field(default_factory=list)
    budget: DiscoverBudget = Field(default_factory=DiscoverBudget)


class DiscoverEntityRequest(BaseModel):
    """Targeted entity-discovery brief (docs/15 §3). Shaped to feed
    TargetBrief.from_dict directly via model_dump()."""

    goal: str = "any_info"        # social_handle | real_name | contact | photos | any_info
    subject: DiscoverSubject = Field(default_factory=DiscoverSubject)
    constraints: DiscoverConstraints = Field(default_factory=DiscoverConstraints)
    discover: bool = True         # also query live SearXNG (reach un-indexed pages)
    max_candidates: int = 20      # retrieval breadth per planned query
    max_results: int = 10         # ranked candidates returned


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
