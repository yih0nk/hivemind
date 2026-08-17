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


def test_default_triage_is_completed_not_gated():
    # Backward compat: without require_approval, /triage completes in one call.
    resp = client.post("/triage", json={"alert": "OOMKilled"})
    assert resp.status_code == 200
    assert resp.json()["status"] == "completed"


def _oom_request(**extra):
    return {"alert": "OOMKilled", "namespace": "prod", "logs": "OOMKilled", **extra}


def test_gated_triage_then_resume_approve():
    # 1) Gated triage pauses and hands back a thread_id + the proposal.
    start = client.post("/triage", json=_oom_request(require_approval=True))
    assert start.status_code == 200
    body = start.json()
    assert body["status"] == "awaiting_approval"
    assert body["thread_id"]
    assert body["approval_request"]["root_cause"]

    # 2) Resuming with approve finalizes the report.
    done = client.post(
        "/resume",
        json={"thread_id": body["thread_id"], "action": "approve", "note": "ok"},
    )
    assert done.status_code == 200
    dbody = done.json()
    assert dbody["status"] == "completed"
    assert dbody["approved"] is True
    assert dbody["root_cause"]


def test_gated_triage_then_resume_reject():
    start = client.post("/triage", json=_oom_request(require_approval=True)).json()
    done = client.post(
        "/resume", json={"thread_id": start["thread_id"], "action": "reject"}
    ).json()
    assert done["status"] == "completed"
    assert done["approved"] is False


def test_resume_unknown_thread_conflicts():
    resp = client.post("/resume", json={"thread_id": "does-not-exist"})
    assert resp.status_code == 409
