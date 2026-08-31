"""Incident memory: a vector store of past incidents that primes synthesis.

The graph recalls similar past incidents before synthesizing and remembers each
finalized one afterward, so the brain gets better at recurring failure modes
over time ("we have seen this OOM pattern before, here is what worked").

Embeddings are deterministic **feature hashing** rather than a hosted model:
tokens are hashed into a fixed-dimensional space and L2-normalized, so cosine
similarity reflects token overlap with no external embedding API (Groq offers
none) and no heavy local model. It is a real vector store, just lexical rather
than semantic -- swap HashingEmbeddings for a semantic model when one is
available. State lives in-process (single-replica, like the checkpointer); a
shared/persistent store is the multi-replica upgrade.
"""

from __future__ import annotations

import hashlib
import json
import math
import os
import re
from typing import Any

from langchain_core.embeddings import Embeddings
from langchain_core.vectorstores import InMemoryVectorStore

_TOKEN = re.compile(r"[a-z0-9]+")


def _tokenize(text: str) -> list[str]:
    return _TOKEN.findall(text.lower())


class HashingEmbeddings(Embeddings):
    """Deterministic feature-hashing embeddings (no external model/API).

    Each token is hashed once; the low bit picks a sign and the rest picks a
    bucket, giving signed accumulation into a fixed-dim vector, then L2
    normalization so cosine similarity is meaningful.
    """

    def __init__(self, dim: int = 256) -> None:
        self.dim = dim

    def _embed(self, text: str) -> list[float]:
        vec = [0.0] * self.dim
        for tok in _tokenize(text):
            h = int.from_bytes(hashlib.md5(tok.encode()).digest()[:8], "big")
            sign = 1.0 if (h & 1) else -1.0
            vec[(h >> 1) % self.dim] += sign
        norm = math.sqrt(sum(v * v for v in vec))
        if norm > 0.0:
            vec = [v / norm for v in vec]
        return vec

    def embed_documents(self, texts: list[str]) -> list[list[float]]:
        return [self._embed(t) for t in texts]

    def embed_query(self, text: str) -> list[float]:
        return self._embed(text)


class IncidentMemory:
    """A small in-memory vector store of past-incident summaries."""

    def __init__(
        self,
        embeddings: Embeddings | None = None,
        k: int = 3,
        path: str | None = None,
    ) -> None:
        self._store = InMemoryVectorStore(embeddings or HashingEmbeddings())
        self.k = k
        self.path = path
        # Keep the raw records too: the vector store isn't serialized directly,
        # so persistence dumps these and rebuilds the store on load.
        self._records: list[dict[str, Any]] = []
        self._load()

    def remember(self, text: str, metadata: dict[str, Any] | None = None) -> None:
        """Add one incident summary to the store (and persist, if configured)."""
        if not text:
            return
        meta = metadata or {}
        self._store.add_texts([text], metadatas=[meta])
        self._records.append({"text": text, "metadata": meta})
        self._save()

    def recall(self, query: str, k: int | None = None) -> list[str]:
        """Return up to k past-incident summaries most similar to query."""
        if not self._records or not query:
            return []
        docs = self._store.similarity_search(query, k=k or self.k)
        return [d.page_content for d in docs]

    def size(self) -> int:
        return len(self._records)

    def persisted(self) -> bool:
        return self.path is not None

    def _load(self) -> None:
        """Rebuild the store from a prior snapshot when a path is configured."""
        if not self.path or not os.path.exists(self.path):
            return
        try:
            with open(self.path, encoding="utf-8") as f:
                records = json.load(f)
        except (json.JSONDecodeError, OSError):
            return  # a corrupt/unreadable snapshot must not sink startup
        for rec in records:
            text = rec.get("text", "")
            if text:
                self._store.add_texts([text], metadatas=[rec.get("metadata", {})])
                self._records.append({"text": text, "metadata": rec.get("metadata", {})})

    def _save(self) -> None:
        if not self.path:
            return
        try:
            with open(self.path, "w", encoding="utf-8") as f:
                json.dump(self._records, f)
        except OSError:
            pass  # best-effort; losing durability must not fail a triage
