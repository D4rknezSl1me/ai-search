"""Configuration for the ai-search intelligence service.

Values come from environment variables (see .env.example). Phase 0 only needs
the addresses of the backing services it checks in /readyz.
"""

from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    model_config = SettingsConfigDict(env_file=None, extra="ignore")

    # Vector DB
    qdrant_host: str = "qdrant"
    qdrant_http_port: int = 6333

    # Keyword index
    opensearch_host: str = "opensearch"
    opensearch_port: int = 9200

    # Embeddings (GPU, 'gpu' profile — may be absent in Phase 0)
    tei_host: str = "tei"
    tei_port: int = 8080

    # Local synthesis LLM (Ollama, GPU, 'gpu' profile — no paid API). Phase 2.
    llm_host: str = "llm"
    llm_port: int = 11434
    llm_model: str = "llama3.1:8b"

    ai_api_port: int = 8000

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
    def llm_url(self) -> str:
        return f"http://{self.llm_host}:{self.llm_port}"


settings = Settings()
