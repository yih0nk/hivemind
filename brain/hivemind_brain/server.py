"""FastAPI surface over the reflection graph.

    POST /triage         → run the graph, return the root-cause report
                           (or, with require_approval, pause at the approval gate)
    POST /triage/stream  → same, but stream node-by-node progress as SSE
    POST /resume         → resume a paused triage with an approve/reject decision
    GET  /healthz        → liveness + resolved provider, gather mode, memory size

The graph is compiled once at startup and reused across requests.
"""

from __future__ import annotations

import json
import uuid

from fastapi import FastAPI, HTTPException
from fastapi.responses import StreamingResponse
from langgraph.types import Command

from . import __version__
from .config import Settings
from .graph import build_graph, default_memory, initial_state
from .models import ResumeRequest, TriageRequest, TriageResponse

# Load a local .env if present (dev convenience). No-op when python-dotenv is
# absent or the file is missing — in-cluster config comes from real env vars.
try:  # pragma: no cover - trivial optional import
    from dotenv import load_dotenv

    load_dotenv()
except ImportError:  # pragma: no cover
    pass

app = FastAPI(title="Hivemind Brain", version=__version__)

_settings = Settings.from_env()
# Build the memory here (not inside build_graph) so /healthz can report its size;
# the graph shares this instance and accumulates across requests.
_memory = default_memory(_settings)
_graph = build_graph(_settings, memory=_memory)


@app.get("/healthz")
def healthz() -> dict[str, object]:
    return {
        "status": "ok",
        "version": __version__,
        "provider": _settings.resolved_provider,
        "gather_mode": _settings.gather_mode,
        "checkpointer": "sqlite" if _settings.checkpoint_path else "memory",
        "memory": {
            "enabled": _memory is not None,
            "size": _memory.size() if _memory is not None else 0,
            "persisted": _memory.persisted() if _memory is not None else False,
        },
    }


def _to_response(result: dict, thread_id: str) -> TriageResponse:
    """Map a graph result to a response.

    An interrupted run (``__interrupt__`` present) means the approval gate paused
    it: return the pending proposal and the thread_id to resume with. Otherwise
    the run finalized: return the flat report.
    """
    if "__interrupt__" in result:
        return TriageResponse(
            status="awaiting_approval",
            thread_id=thread_id,
            approval_request=result["__interrupt__"][0].value,
        )
    report = result.get("report", {})
    return TriageResponse(
        status="completed",
        thread_id=thread_id,
        approved=report.get("approved", True),
        root_cause=report.get("root_cause", ""),
        proposed_fix=report.get("proposed_fix", ""),
        confidence=report.get("confidence", 0.0),
        iterations=report.get("iterations", 0),
        critique=report.get("critique", ""),
        evidence=report.get("evidence", {}),
        history=result.get("history", []),
    )


@app.post("/triage", response_model=TriageResponse)
def triage(req: TriageRequest) -> TriageResponse:
    state = initial_state(
        incident=req.to_incident(),
        settings=_settings,
        max_iterations=req.max_iterations,
        confidence_threshold=req.confidence_threshold,
        require_approval=req.require_approval,
    )
    # The checkpointed graph requires a thread_id; it also keys a paused run for
    # /resume when require_approval is set.
    thread_id = uuid.uuid4().hex
    config = {"configurable": {"thread_id": thread_id}}
    final = _graph.invoke(state, config)
    return _to_response(final, thread_id)


def _sse(event: str, data: dict) -> str:
    return f"event: {event}\ndata: {json.dumps(data)}\n\n"


@app.post("/triage/stream")
def triage_stream(req: TriageRequest) -> StreamingResponse:
    """Run a triage and stream node-by-node progress as Server-Sent Events.

    Emits a `node` event per graph step (with confidence when available), then a
    terminal `report` event -- or an `awaiting_approval` event if the run pauses
    at the gate. Lets a UI or `curl -N` watch the reflection loop live.
    """
    state = initial_state(
        incident=req.to_incident(),
        settings=_settings,
        max_iterations=req.max_iterations,
        confidence_threshold=req.confidence_threshold,
        require_approval=req.require_approval,
    )
    thread_id = uuid.uuid4().hex
    config = {"configurable": {"thread_id": thread_id}}

    def events():
        report: dict = {}
        for chunk in _graph.stream(state, config, stream_mode="updates"):
            if "__interrupt__" in chunk:
                yield _sse("awaiting_approval", {
                    "thread_id": thread_id,
                    "approval_request": chunk["__interrupt__"][0].value,
                })
                return
            for node, update in chunk.items():
                event = {"node": node}
                if isinstance(update, dict):
                    if "confidence" in update:
                        event["confidence"] = update["confidence"]
                    if update.get("report"):
                        report = update["report"]
                yield _sse("node", event)
        yield _sse("report", {"thread_id": thread_id, **report})

    return StreamingResponse(events(), media_type="text/event-stream")


@app.post("/resume", response_model=TriageResponse)
def resume(req: ResumeRequest) -> TriageResponse:
    """Resume a triage paused at the approval gate with a human decision."""
    config = {"configurable": {"thread_id": req.thread_id}}
    snapshot = _graph.get_state(config)
    if not snapshot.next:
        raise HTTPException(
            status_code=409,
            detail=f"thread {req.thread_id!r} is not awaiting approval",
        )
    final = _graph.invoke(
        Command(resume={"action": req.action, "note": req.note}), config
    )
    return _to_response(final, req.thread_id)


def main() -> None:
    import os

    import uvicorn

    port = int(os.getenv("HIVEMIND_BRAIN_PORT", "8090"))
    uvicorn.run(app, host="0.0.0.0", port=port)


if __name__ == "__main__":
    main()
