"""Assembly of the reflection graph.

    START → gather → synthesize → critique → ┐
              ▲                               │ route_after_critique
              └──────────── loop ────────────┤
                            done → approval → finalize → END

The ``critique → gather`` back-edge is the whole point: the graph re-gathers
evidence (steered by the critic's guidance) until it is confident or hits the
iteration cap. That cycle is what a fixed errgroup DAG in the Go operator cannot
express, and why the reasoning layer lives here in LangGraph.

The ``approval`` node is the second thing a DAG cannot do: when the caller asks
for it, the node ``interrupt()``s, the graph checkpoints its state and pauses,
and it resumes from exactly there once a human decides. The graph is therefore
compiled with a checkpointer so a paused run survives across HTTP calls.
"""

from __future__ import annotations

from typing import Any

from langgraph.checkpoint.memory import MemorySaver
from langgraph.graph import END, START, StateGraph

from .config import Settings
from .llm import build_model
from .memory import HashingEmbeddings, IncidentMemory
from .nodes import (
    approval_gate,
    finalize,
    make_critique,
    make_gather,
    make_react_gather,
    make_react_gather_native,
    make_recall,
    make_remember,
    make_synthesize,
    route_after_critique,
)
from .state import TriageState

# Sentinel: distinguishes "caller did not specify memory" (build the default
# from settings) from "caller passed None" (disable memory).
_DEFAULT_MEMORY = object()


def default_memory(settings: Settings) -> IncidentMemory | None:
    """Build the incident memory from settings, or None when disabled."""
    if not settings.memory_enabled:
        return None
    return IncidentMemory(
        HashingEmbeddings(settings.memory_dim),
        settings.memory_k,
        path=settings.memory_path or None,
    )


def build_graph(
    settings: Settings | None = None,
    model: Any | None = None,
    memory: Any = _DEFAULT_MEMORY,
):
    """Compile the reflection graph.

    ``model`` can be injected (tests pass a mock directly); otherwise it is
    built from ``settings``. ``memory`` defaults to one built from settings;
    pass an IncidentMemory to inject one, or None to disable recall/remember.

    Compiled with an in-memory checkpointer so the approval interrupt can pause
    and resume; a single-replica deployment keeps that state (and the memory
    store) addressable. A multi-replica brain would swap in shared backends.
    """
    settings = settings or Settings.from_env()
    model = model if model is not None else build_model(settings)
    if memory is _DEFAULT_MEMORY:
        memory = default_memory(settings)

    if settings.gather_mode == "react-native":
        gather_node = make_react_gather_native(model, settings.react_max_steps)
    elif settings.gather_mode == "react":
        gather_node = make_react_gather(model, settings.react_max_steps)
    else:
        gather_node = make_gather(model)

    builder = StateGraph(TriageState)
    builder.add_node("gather", gather_node)
    builder.add_node("synthesize", make_synthesize(model))
    builder.add_node("critique", make_critique(model))
    builder.add_node("approval", approval_gate)
    builder.add_node("finalize", finalize)

    builder.add_edge("gather", "synthesize")
    builder.add_edge("synthesize", "critique")
    builder.add_conditional_edges(
        "critique",
        route_after_critique,
        {"loop": "gather", "done": "approval"},
    )
    builder.add_edge("approval", "finalize")

    # Bookend the graph with memory when enabled: recall similar incidents
    # before gathering, remember this one after finalizing.
    if memory is not None:
        builder.add_node("recall", make_recall(memory))
        builder.add_node("remember", make_remember(memory))
        builder.add_edge(START, "recall")
        builder.add_edge("recall", "gather")
        builder.add_edge("finalize", "remember")
        builder.add_edge("remember", END)
    else:
        builder.add_edge(START, "gather")
        builder.add_edge("finalize", END)

    return builder.compile(checkpointer=MemorySaver())


def initial_state(
    incident: dict[str, Any],
    settings: Settings,
    max_iterations: int | None = None,
    confidence_threshold: float | None = None,
    require_approval: bool = False,
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
        "require_approval": require_approval,
        "iterations": 0,
        "guidance": "",
        "history": [],
    }
