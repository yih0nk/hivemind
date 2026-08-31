"""Tests for the SSE streaming endpoint."""

from __future__ import annotations

import os

os.environ.setdefault("HIVEMIND_LLM_PROVIDER", "mock")

from fastapi.testclient import TestClient  # noqa: E402

from hivemind_brain.server import app  # noqa: E402

client = TestClient(app)


def _parse_events(text: str) -> list[tuple[str, str]]:
    events = []
    event = None
    for line in text.splitlines():
        if line.startswith("event: "):
            event = line[len("event: "):]
        elif line.startswith("data: ") and event is not None:
            events.append((event, line[len("data: "):]))
    return events


def test_stream_emits_node_events_then_report():
    resp = client.post("/triage/stream", json={"alert": "OOMKilled", "logs": "OOMKilled"})
    assert resp.status_code == 200
    assert resp.headers["content-type"].startswith("text/event-stream")

    events = _parse_events(resp.text)
    kinds = [e for e, _ in events]
    assert "node" in kinds
    assert kinds[-1] == "report"  # terminal event is the report
    # node events cover the graph steps (gather/synthesize/critique/finalize...)
    nodes = [d for e, d in events if e == "node"]
    assert any("gather" in n for n in nodes)


def test_stream_gated_emits_awaiting_approval():
    resp = client.post(
        "/triage/stream", json={"alert": "OOMKilled", "logs": "x", "require_approval": True}
    )
    events = _parse_events(resp.text)
    kinds = [e for e, _ in events]
    assert "awaiting_approval" in kinds
    assert "report" not in kinds  # paused, no report yet
