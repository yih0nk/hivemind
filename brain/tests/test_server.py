"""HTTP-level tests using FastAPI's TestClient (mock provider)."""

from __future__ import annotations

import os

os.environ.setdefault("HIVEMIND_LLM_PROVIDER", "mock")

from fastapi.testclient import TestClient  # noqa: E402

from hivemind_brain.server import app  # noqa: E402

client = TestClient(app)


def test_healthz_reports_mock_provider():
    resp = client.get("/healthz")
    assert resp.status_code == 200
    body = resp.json()
    assert body["status"] == "ok"
    assert body["provider"] == "mock"


def test_triage_returns_report():
    resp = client.post(
        "/triage",
        json={
            "alert": "OOMKilled",
            "namespace": "prod",
            "pod": "checkout-7d9",
            "logs": "OOMKilled",
            "metrics": "mem climbing",
            "runbooks": [{"name": "oom", "content": "raise limits"}],
        },
    )
    assert resp.status_code == 200
    body = resp.json()
    assert body["root_cause"]
    assert body["proposed_fix"]
    assert 0.0 <= body["confidence"] <= 1.0
    assert body["iterations"] >= 1


def test_triage_validation_rejects_bad_threshold():
    resp = client.post(
        "/triage",
        json={"alert": "OOMKilled", "confidence_threshold": 1.5},
    )
    assert resp.status_code == 422
