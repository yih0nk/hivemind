"""Graph nodes.

Each node takes the running ``TriageState`` and returns a partial update. The
system prompts start with a ``TASK: <name>`` tag: it steers the mock model and,
on the real path, gives the LLM an unambiguous role. Node functions are built
as closures over the chat model so the graph stays free of global state.
"""

from __future__ import annotations

import json
from typing import Any, Callable

from langgraph.types import interrupt

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
    """Combine the evidence summaries into a single root-cause hypothesis.

    Similar past incidents recalled from memory (if any) are included as
    reference so recurring failure modes converge on what worked before.
    """

    def synthesize(state: TriageState) -> dict[str, Any]:
        evidence = state.get("evidence", {})
        similar = state.get("similar_incidents", [])
        system = (
            "TASK: synthesize. You are a staff SRE. From the evidence summaries, "
            "produce one root-cause hypothesis and a concrete proposed fix. "
            "If similar past incidents are provided, use them as prior knowledge "
            "but do not assume this incident is identical. "
            'Respond as JSON with keys "root_cause" and "proposed_fix".'
        )
        user = json.dumps(evidence, indent=2, sort_keys=True)
        if similar:
            user += "\n\nSimilar past incidents (reference):\n" + "\n---\n".join(similar)
        hypothesis = call_json(model, system, user)
        return {
            "hypothesis": hypothesis,
            "history": [{"node": "synthesize", "hypothesis": hypothesis}],
        }

    return synthesize


def make_recall(memory: Any) -> Node:
    """Recall past incidents similar to this one, to prime synthesis."""

    def recall(state: TriageState) -> dict[str, Any]:
        incident = state.get("incident", {})
        query = " ".join(
            str(incident.get(k, "")) for k in ("alert", "logs", "metrics")
        )
        similar = memory.recall(query)
        return {
            "similar_incidents": similar,
            "history": [{"node": "recall", "recalled": len(similar)}],
        }

    return recall


def make_remember(memory: Any) -> Node:
    """Store this finalized incident so future triage can recall it.

    Rejected proposals are not stored -- memory should reflect fixes a human
    was willing to stand behind, not ones they turned down.
    """

    def remember(state: TriageState) -> dict[str, Any]:
        report = state.get("report", {})
        if not report.get("approved", True):
            return {"history": [{"node": "remember", "stored": False, "reason": "rejected"}]}
        incident = state.get("incident", {})
        summary = (
            f"Alert: {incident.get('alert', '')}\n"
            f"Root cause: {report.get('root_cause', '')}\n"
            f"Fix: {report.get('proposed_fix', '')}"
        )
        memory.remember(summary, metadata={"alert": incident.get("alert", "")})
        return {"history": [{"node": "remember", "stored": True}]}

    return remember


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


def approval_gate(state: TriageState) -> dict[str, Any]:
    """Pause for a human decision before finalizing, when asked to.

    When ``require_approval`` is set, the node calls LangGraph's ``interrupt()``:
    the graph stops, its state is checkpointed, and the surfaced payload (the
    proposed root cause and fix) is handed to the caller. On resume the human's
    decision is injected as ``interrupt()``'s return value. This is the durable,
    resumable pause a plain function chain cannot do -- the reason the graph is
    checkpointed. When approval is not required the gate is a no-op passthrough.
    """
    if not state.get("require_approval"):
        return {"approved": True}

    hypothesis = state.get("hypothesis", {})
    decision = interrupt(
        {
            "type": "approval_request",
            "root_cause": hypothesis.get("root_cause", ""),
            "proposed_fix": hypothesis.get("proposed_fix", ""),
            "confidence": float(state.get("confidence", 0.0)),
            "iterations": int(state.get("iterations", 0)),
        }
    )
    decision = decision or {}
    approved = str(decision.get("action", "approve")).lower() == "approve"
    return {
        "approved": approved,
        "decision": decision,
        "history": [{"node": "approval", "approved": approved, "decision": decision}],
    }


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
        "approved": bool(state.get("approved", True)),
        "decision": state.get("decision", {}),
    }
    return {"report": report, "history": [{"node": "finalize", "report": report}]}
