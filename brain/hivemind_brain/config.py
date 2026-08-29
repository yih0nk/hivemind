"""Runtime configuration, sourced from environment variables.

Kept dependency-light on purpose: a plain dataclass over ``os.getenv`` so the
service has no opinion about how env vars get populated (``.env`` in dev, a
Kubernetes Secret in the Helm chart, plain exports in CI).
"""

from __future__ import annotations

import os
from dataclasses import dataclass


def _get_bool(name: str, default: bool) -> bool:
    raw = os.getenv(name)
    if raw is None:
        return default
    return raw.strip().lower() in {"1", "true", "yes", "on"}


@dataclass(frozen=True)
class Settings:
    """Immutable snapshot of the service configuration."""

    # Provider selection. "auto" resolves to "groq" when a key is present,
    # otherwise falls back to the deterministic mock so the graph always runs.
    provider: str = "auto"
    groq_api_key: str | None = None
    groq_model: str = "openai/gpt-oss-20b"
    request_timeout_s: float = 30.0

    # Reflection-loop defaults (overridable per-request).
    max_iterations: int = 3
    confidence_threshold: float = 0.75

    # Evidence gathering: "summary" (one LLM pass) or "react" (a tool-choosing
    # agent that investigates the evidence bundle over several steps).
    gather_mode: str = "summary"
    react_max_steps: int = 4

    # Incident memory: recall similar past incidents to prime synthesis, and
    # remember finalized ones for next time.
    memory_enabled: bool = True
    memory_k: int = 3
    memory_dim: int = 256

    @property
    def resolved_provider(self) -> str:
        """Concrete provider after resolving "auto"."""
        if self.provider != "auto":
            return self.provider
        return "groq" if self.groq_api_key else "mock"

    @classmethod
    def from_env(cls) -> "Settings":
        return cls(
            provider=os.getenv("HIVEMIND_LLM_PROVIDER", "auto").strip().lower(),
            groq_api_key=os.getenv("GROQ_API_KEY") or None,
            groq_model=os.getenv("GROQ_MODEL", "openai/gpt-oss-20b"),
            request_timeout_s=float(os.getenv("HIVEMIND_LLM_TIMEOUT_S", "30")),
            max_iterations=int(os.getenv("HIVEMIND_MAX_ITERATIONS", "3")),
            confidence_threshold=float(
                os.getenv("HIVEMIND_CONFIDENCE_THRESHOLD", "0.75")
            ),
            memory_enabled=_get_bool("HIVEMIND_MEMORY_ENABLED", True),
            memory_k=int(os.getenv("HIVEMIND_MEMORY_K", "3")),
            memory_dim=int(os.getenv("HIVEMIND_MEMORY_DIM", "256")),
            gather_mode=os.getenv("HIVEMIND_GATHER_MODE", "summary").strip().lower(),
            react_max_steps=int(os.getenv("HIVEMIND_REACT_MAX_STEPS", "4")),
        )
