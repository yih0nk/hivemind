"""Tests for request-id logging middleware."""

from __future__ import annotations

import logging
import os

os.environ.setdefault("HIVEMIND_LLM_PROVIDER", "mock")

from fastapi.testclient import TestClient  # noqa: E402

from hivemind_brain.server import app  # noqa: E402

client = TestClient(app)


def test_request_id_header_present_and_stable():
    resp = client.post("/triage", json={"alert": "OOMKilled", "logs": "x"})
    assert resp.headers.get("X-Request-ID")

    # A caller-supplied id is echoed back for correlation.
    resp2 = client.post(
        "/triage", json={"alert": "OOMKilled", "logs": "x"},
        headers={"X-Request-ID": "abc123"},
    )
    assert resp2.headers["X-Request-ID"] == "abc123"


def test_triage_is_logged(caplog):
    with caplog.at_level(logging.INFO, logger="hivemind.brain"):
        client.post("/triage", json={"alert": "OOMKilled", "logs": "x"})
    assert any("/triage" in r.message and "id=" in r.message for r in caplog.records)


def test_healthz_not_logged(caplog):
    with caplog.at_level(logging.INFO, logger="hivemind.brain"):
        client.get("/healthz")
    assert not any("/healthz" in r.message for r in caplog.records)
