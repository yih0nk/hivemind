"""Tests for the metrics registry and /metrics endpoint."""

from __future__ import annotations

import os

os.environ.setdefault("HIVEMIND_LLM_PROVIDER", "mock")

from fastapi.testclient import TestClient  # noqa: E402

from hivemind_brain.metrics import Metrics  # noqa: E402
from hivemind_brain.server import app  # noqa: E402

client = TestClient(app)


def test_registry_renders_counter_with_labels():
    m = Metrics()
    m.counter("hivemind_triage_total", "runs")
    m.inc("hivemind_triage_total", {"status": "completed"})
    m.inc("hivemind_triage_total", {"status": "completed"})
    m.inc("hivemind_triage_total", {"status": "awaiting_approval"})
    out = m.render()
    assert "# TYPE hivemind_triage_total counter" in out
    assert 'hivemind_triage_total{status="completed"} 2' in out
    assert 'hivemind_triage_total{status="awaiting_approval"} 1' in out


def test_registry_gauge_sampled_at_render():
    m = Metrics()
    value = {"n": 3}
    m.gauge("hivemind_memory_size", "size", lambda: value["n"])
    assert "hivemind_memory_size 3" in m.render()
    value["n"] = 5
    assert "hivemind_memory_size 5" in m.render()


def test_metrics_endpoint_counts_a_triage():
    before = client.get("/metrics").text
    client.post("/triage", json={"alert": "OOMKilled", "logs": "OOMKilled"})
    after = client.get("/metrics").text

    assert "hivemind_triage_total" in after
    # the completed counter advanced by at least one after a triage
    def completed(text):
        for line in text.splitlines():
            if line.startswith('hivemind_triage_total{status="completed"}'):
                return float(line.rsplit(" ", 1)[1])
        return 0.0
    assert completed(after) >= completed(before) + 1
