"""Assembly of the reflection graph.

    START → gather → synthesize → critique → ┐
              ▲                               │ route_after_critique
              └──────────── loop ────────────┤
                                             └── done → finalize → END

The ``critique → gather`` back-edge is the whole point: the graph re-gathers
evidence (steered by the critic's guidance) until it is confident or hits the
iteration cap. That cycle is what a fixed errgroup DAG in the Go operator cannot
express, and why the reasoning layer lives here in LangGraph.
"""

from __future__ import annotations

from typing import Any

from langgraph.graph import END, START, StateGraph

from .config import Settings
from .llm import build_model
from .nodes import (
    finalize,
    make_critique,
    make_gather,
    make_synthesize,
    route_after_critique,
)
from .state import TriageState


def build_graph(settings: Settings | None = None, model: Any | None = None):
    """Compile the reflection graph.

    ``model`` can be injected (tests pass a mock directly); otherwise it is
    built from ``settings``.
    """
    settings = settings or Settings.from_env()
    model = model if model is not None else build_model(settings)

    builder = StateGraph(TriageState)
    builder.add_node("gather", make_gather(model))
    builder.add_node("synthesize", make_synthesize(model))
    builder.add_node("critique", make_critique(model))
    builder.add_node("finalize", finalize)

    builder.add_edge(START, "gather")
    builder.add_edge("gather", "synthesize")
    builder.add_edge("synthesize", "critique")
    builder.add_conditional_edges(
        "critique",
        route_after_critique,
        {"loop": "gather", "done": "finalize"},
    )
    builder.add_edge("finalize", END)

    return builder.compile()


def initial_state(
    incident: dict[str, Any],
    settings: Settings,
    max_iterations: int | None = None,
    confidence_threshold: float | None = None,
) -> TriageState:
    """Seed a fresh run state from an incident payload and defaults."""
    return {
        "incident": incident,
        "max_iterations": max_iterations or settings.max_iterations,
        "confidence_threshold": (
            confidence_threshold
            if confidence_threshold is not None
            else settings.confidence_threshold
        ),
        "iterations": 0,
        "guidance": "",
        "history": [],
    }
