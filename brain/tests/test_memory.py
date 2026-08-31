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


def test_memory_persists_and_reloads(tmp_path):
    path = str(tmp_path / "memory.json")

    mem = IncidentMemory(k=3, path=path)
    assert mem.persisted() is True
    mem.remember("Alert: OOMKilled\nFix: raise the memory limit")
    mem.remember("Alert: HighErrorRate\nFix: rolled back the deploy")

    # A fresh instance on the same path recovers the records and can recall them.
    reloaded = IncidentMemory(k=3, path=path)
    assert reloaded.size() == 2
    hits = reloaded.recall("OOMKilled out of memory")
    assert any("OOMKilled" in h for h in hits)


def test_in_memory_has_no_persistence():
    mem = IncidentMemory()
    assert mem.persisted() is False


def test_corrupt_snapshot_does_not_crash(tmp_path):
    path = tmp_path / "memory.json"
    path.write_text("not valid json{{{")
    mem = IncidentMemory(path=str(path))  # must not raise
    assert mem.size() == 0
