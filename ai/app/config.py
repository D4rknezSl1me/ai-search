"""Configuration for the ai-search intelligence service.

Values come from environment variables (see .env.example). Phase 2 adds the
datastores the indexer/retrieval pipeline touches (Postgres, MinIO), the GPU
model endpoints (TEI embeddings, TEI reranker, Ollama LLM), and pipeline tuning
knobs. Everything is local/self-hosted — no paid APIs (CLAUDE.md rule 2).
"""

from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    model_config = SettingsConfigDict(env_file=None, extra="ignore")

    # --- Vector DB (Qdrant) ---
    qdrant_host: str = "qdrant"
    qdrant_http_port: int = 6333

    # --- Keyword index (OpenSearch) ---
    opensearch_host: str = "opensearch"
    opensearch_port: int = 9200

    # --- Embeddings (TEI, GPU) ---
    tei_host: str = "tei"
    tei_port: int = 8080
    embed_model_id: str = "BAAI/bge-large-en-v1.5"
    embed_dim: int = 1024

    # --- Cross-encoder re-ranker (TEI, GPU; optional — degrades to RRF order) ---
    rerank_host: str = "reranker"
    rerank_port: int = 8085

    # --- Local synthesis LLM (Ollama, GPU; no paid API) ---
    llm_host: str = "llm"
    llm_port: int = 11434
    llm_model: str = "llama3.1:8b"

    # --- Postgres (document metadata; source of un-indexed docs) ---
    postgres_host: str = "postgres"
    postgres_port: int = 5432
    postgres_user: str = "aisearch"
    postgres_password: str = "aisearch"
    postgres_db: str = "aisearch"

    # --- MinIO (clean extracted text blobs) ---
    minio_host: str = "minio"
    minio_port: int = 9000
    minio_root_user: str = "aisearch"
    minio_root_password: str = "aisearch"
    minio_bucket: str = "raw"

    ai_api_port: int = 8000

    # --- Index names ---
    qdrant_collection: str = "chunks"
    opensearch_index: str = "chunks"

    # --- Chunking (structure-aware; see docs/05-EXTRACTION.md §7) ---
    chunk_target_chars: int = 1600   # ~400 tokens at ~4 chars/token
    chunk_overlap_chars: int = 200   # ~12% overlap
    chunk_min_chars: int = 200       # drop trailing crumbs smaller than this

    # --- Indexer ---
    indexer_batch: int = 32          # docs per poll
    embed_batch: int = 32            # chunks per TEI call
    indexer_poll_seconds: float = 5.0

    # --- Retrieval / fusion / rerank ---
    retrieve_k_lexical: int = 100
    retrieve_k_vector: int = 100
    rrf_k: int = 60
    rerank_candidates: int = 50      # shortlist size fed to the cross-encoder
    max_sources: int = 12            # chunks kept for synthesis
    max_per_domain: int = 3          # diversity cap during assembly

    @property
    def qdrant_url(self) -> str:
        return f"http://{self.qdrant_host}:{self.qdrant_http_port}"

    @property
    def opensearch_url(self) -> str:
        return f"http://{self.opensearch_host}:{self.opensearch_port}"

    @property
    def tei_url(self) -> str:
        return f"http://{self.tei_host}:{self.tei_port}"

    @property
    def rerank_url(self) -> str:
        return f"http://{self.rerank_host}:{self.rerank_port}"

    @property
    def llm_url(self) -> str:
        return f"http://{self.llm_host}:{self.llm_port}"

    @property
    def pg_dsn(self) -> str:
        return (
            f"postgresql://{self.postgres_user}:{self.postgres_password}"
            f"@{self.postgres_host}:{self.postgres_port}/{self.postgres_db}"
        )

    @property
    def minio_endpoint(self) -> str:
        return f"{self.minio_host}:{self.minio_port}"


settings = Settings()
