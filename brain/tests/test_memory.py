"""Tests for the incident-memory vector store."""

from __future__ import annotations

from hivemind_brain.memory import HashingEmbeddings, IncidentMemory


def test_hashing_embeddings_are_deterministic_and_normalized():
    emb = HashingEmbeddings(dim=64)
    v1 = emb.embed_query("OOMKilled checkout pod")
    v2 = emb.embed_query("OOMKilled checkout pod")
    assert v1 == v2  # deterministic
    assert len(v1) == 64
    norm = sum(x * x for x in v1) ** 0.5
    assert abs(norm - 1.0) < 1e-9  # L2-normalized


def test_empty_memory_recalls_nothing():
    mem = IncidentMemory()
    assert mem.recall("anything") == []
    assert mem.size() == 0


def test_recall_returns_most_similar_incident():
    mem = IncidentMemory(k=1)
    mem.remember("OOMKilled: checkout ran out of memory; raised the limit")
    mem.remember("HighErrorRate: payments 5xx from a bad deploy; rolled back")
    assert mem.size() == 2

    hits = mem.recall("checkout pod OOMKilled out of memory")
    assert len(hits) == 1
    assert "OOMKilled" in hits[0]


def test_recall_caps_at_k():
    mem = IncidentMemory(k=2)
    for i in range(5):
        mem.remember(f"incident number {i} with OOMKilled memory pressure")
    assert len(mem.recall("OOMKilled memory")) == 2


def test_remember_ignores_empty_text():
    mem = IncidentMemory()
    mem.remember("")
    assert mem.size() == 0
