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
    groq_model: str = "llama-3.3-70b-versatile"
    request_timeout_s: float = 30.0

    # Reflection-loop defaults (overridable per-request).
    max_iterations: int = 3
    confidence_threshold: float = 0.75

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
            groq_model=os.getenv("GROQ_MODEL", "llama-3.3-70b-versatile"),
            request_timeout_s=float(os.getenv("HIVEMIND_LLM_TIMEOUT_S", "30")),
            max_iterations=int(os.getenv("HIVEMIND_MAX_ITERATIONS", "3")),
            confidence_threshold=float(
                os.getenv("HIVEMIND_CONFIDENCE_THRESHOLD", "0.75")
            ),
        )
