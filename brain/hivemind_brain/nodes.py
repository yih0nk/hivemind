"""Graph nodes.

Each node takes the running ``TriageState`` and returns a partial update. The
system prompts start with a ``TASK: <name>`` tag: it steers the mock model and,
on the real path, gives the LLM an unambiguous role. Node functions are built
as closures over the chat model so the graph stays free of global state.
"""

from __future__ import annotations

import json
from typing import Any, Callable

from .llm import call_json
from .state import TriageState

Node = Callable[[TriageState], dict[str, Any]]


def _incident_brief(incident: dict[str, Any]) -> str:
    """Compact, deterministic rendering of the raw incident payload."""
    return json.dumps(
        {
            "alert": incident.get("alert"),
            "namespace": incident.get("namespace"),
            "pod": incident.get("pod"),
            "logs": incident.get("logs", ""),
            "metrics": incident.get("metrics", ""),
            "runbooks": incident.get("runbooks", []),
        },
        indent=2,
        sort_keys=True,
    )


def make_gather(model: Any) -> Node:
    """Summarize raw evidence per source; re-focus using critic guidance on reloop."""

    def gather(state: TriageState) -> dict[str, Any]:
        incident = state.get("incident", {})
        guidance = state.get("guidance", "")
        system = (
            "TASK: gather. You are an SRE evidence analyst. Summarize the raw "
            "incident signals into concise per-source findings. Respond as a JSON "
            'object with keys "logs", "metrics", "runbooks".'
        )
        user = _incident_brief(incident)
        if guidance:
            user += (
                f"\n\nThe previous analysis was incomplete. Focus this pass on: "
                f"{guidance}"
            )
        evidence = call_json(model, system, user)
        return {
            "evidence": evidence,
            "history": [{"node": "gather", "guidance": guidance, "evidence": evidence}],
        }

    return gather


def make_synthesize(model: Any) -> Node:
    """Combine the evidence summaries into a single root-cause hypothesis."""

    def synthesize(state: TriageState) -> dict[str, Any]:
        evidence = state.get("evidence", {})
        system = (
            "TASK: synthesize. You are a staff SRE. From the evidence summaries, "
            "produce one root-cause hypothesis and a concrete proposed fix. "
            'Respond as JSON with keys "root_cause" and "proposed_fix".'
        )
        user = json.dumps(evidence, indent=2, sort_keys=True)
        hypothesis = call_json(model, system, user)
        return {
            "hypothesis": hypothesis,
            "history": [{"node": "synthesize", "hypothesis": hypothesis}],
        }

    return synthesize


def make_critique(model: Any) -> Node:
    """Score the hypothesis's confidence and emit guidance for another pass."""

    def critique(state: TriageState) -> dict[str, Any]:
        hypothesis = state.get("hypothesis", {})
        evidence = state.get("evidence", {})
        system = (
            "TASK: critique. You are a skeptical incident reviewer. Judge whether "
            "the hypothesis is well supported by the evidence. Respond as JSON with "
            'keys "confidence" (0..1 float), "critique" (string), and "guidance" '
            "(string: what a further evidence pass should focus on; empty if none)."
        )
        user = json.dumps(
            {"hypothesis": hypothesis, "evidence": evidence}, indent=2, sort_keys=True
        )
        result = call_json(model, system, user)
        try:
            confidence = float(result.get("confidence", 0.0))
        except (TypeError, ValueError):
            confidence = 0.0
        confidence = max(0.0, min(1.0, confidence))
        return {
            "confidence": confidence,
            "critique": str(result.get("critique", "")),
            "guidance": str(result.get("guidance", "")),
            "iterations": int(state.get("iterations", 0)) + 1,
            "history": [
                {"node": "critique", "confidence": confidence,
                 "critique": str(result.get("critique", ""))}
            ],
        }

    return critique


def route_after_critique(state: TriageState) -> str:
    """Conditional edge: loop for more evidence, or finalize.

    Loop when confidence is below threshold *and* we have iterations left;
    otherwise finalize. This is the cyclic decision that a static DAG can't
    express and that justifies a LangGraph ``StateGraph``.
    """
    threshold = float(state.get("confidence_threshold", 0.75))
    max_iterations = int(state.get("max_iterations", 3))
    confidence = float(state.get("confidence", 0.0))
    iterations = int(state.get("iterations", 0))
    if confidence < threshold and iterations < max_iterations:
        return "loop"
    return "done"


def finalize(state: TriageState) -> dict[str, Any]:
    """Assemble the report returned to the caller."""
    hypothesis = state.get("hypothesis", {})
    report = {
        "root_cause": hypothesis.get("root_cause", ""),
        "proposed_fix": hypothesis.get("proposed_fix", ""),
        "confidence": float(state.get("confidence", 0.0)),
        "iterations": int(state.get("iterations", 0)),
        "critique": state.get("critique", ""),
        "evidence": state.get("evidence", {}),
    }
    return {"report": report, "history": [{"node": "finalize", "report": report}]}
