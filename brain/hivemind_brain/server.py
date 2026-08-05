"""FastAPI surface over the reflection graph.

    POST /triage   → run the graph on an incident, return the root-cause report
    GET  /healthz  → liveness + which LLM provider resolved

The graph is compiled once at startup and reused across requests.
"""

from __future__ import annotations

from fastapi import FastAPI

from . import __version__
from .config import Settings
from .graph import build_graph, initial_state
from .models import TriageRequest, TriageResponse

# Load a local .env if present (dev convenience). No-op when python-dotenv is
# absent or the file is missing — in-cluster config comes from real env vars.
try:  # pragma: no cover - trivial optional import
    from dotenv import load_dotenv

    load_dotenv()
except ImportError:  # pragma: no cover
    pass

app = FastAPI(title="Hivemind Brain", version=__version__)

_settings = Settings.from_env()
_graph = build_graph(_settings)


@app.get("/healthz")
def healthz() -> dict[str, str]:
    return {
        "status": "ok",
        "version": __version__,
        "provider": _settings.resolved_provider,
    }


@app.post("/triage", response_model=TriageResponse)
def triage(req: TriageRequest) -> TriageResponse:
    state = initial_state(
        incident=req.to_incident(),
        settings=_settings,
        max_iterations=req.max_iterations,
        confidence_threshold=req.confidence_threshold,
    )
    final = _graph.invoke(state)
    report = final.get("report", {})
    return TriageResponse(
        root_cause=report.get("root_cause", ""),
        proposed_fix=report.get("proposed_fix", ""),
        confidence=report.get("confidence", 0.0),
        iterations=report.get("iterations", 0),
        critique=report.get("critique", ""),
        evidence=report.get("evidence", {}),
        history=final.get("history", []),
    )


def main() -> None:
    import uvicorn

    uvicorn.run(app, host="0.0.0.0", port=8090)


if __name__ == "__main__":
    main()
