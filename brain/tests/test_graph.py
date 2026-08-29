"""Graph-level tests on the deterministic mock model."""

from __future__ import annotations

import itertools

from langgraph.types import Command

from hivemind_brain.config import Settings
from hivemind_brain.graph import build_graph, initial_state
from hivemind_brain.memory import IncidentMemory
from hivemind_brain.llm import MockChatModel, _first_json
from hivemind_brain.nodes import route_after_critique

INCIDENT = {
    "alert": "OOMKilled",
    "namespace": "prod",
    "pod": "checkout-7d9",
    "logs": "OOMKilled ... container restarted",
    "metrics": "mem climbing to limit",
    "runbooks": [{"name": "oom", "content": "raise limits"}],
}

# A checkpointed graph needs a unique thread_id per run; hand these out.
_thread_ids = (f"test-{n}" for n in itertools.count())


def _cfg() -> dict:
    return {"configurable": {"thread_id": next(_thread_ids)}}


def _settings() -> Settings:
    return Settings(provider="mock", max_iterations=3, confidence_threshold=0.75)


def test_graph_reaches_confident_report():
    settings = _settings()
    graph = build_graph(settings, model=MockChatModel())
    final = graph.invoke(initial_state(INCIDENT, settings), _cfg())

    report = final["report"]
    assert report["root_cause"]
    assert report["proposed_fix"]
    assert report["confidence"] >= settings.confidence_threshold


def test_reflection_loop_runs_at_least_one_extra_pass():
    # The mock reports low confidence on the first critique, so the graph must
    # loop back through `gather` at least once before converging.
    settings = _settings()
    graph = build_graph(settings, model=MockChatModel())
    final = graph.invoke(initial_state(INCIDENT, settings), _cfg())

    assert final["report"]["iterations"] >= 2
    gather_runs = [h for h in final["history"] if h.get("node") == "gather"]
    assert len(gather_runs) >= 2
    # The second gather pass must be steered by the critic's guidance.
    assert any(h.get("guidance") for h in gather_runs)


def test_route_respects_iteration_cap():
    # Below threshold but out of iterations → finalize, don't loop forever.
    state = {"confidence": 0.1, "confidence_threshold": 0.9,
             "iterations": 3, "max_iterations": 3}
    assert route_after_critique(state) == "done"


def test_route_loops_when_uncertain_with_budget():
    state = {"confidence": 0.1, "confidence_threshold": 0.9,
             "iterations": 1, "max_iterations": 3}
    assert route_after_critique(state) == "loop"


def test_first_json_survives_prose_and_double_block():
    raw = 'Sure! Here is the result:\n```json\n{"a": 1}\n```\nand extra {"b": 2}'
    assert _first_json(raw) == {"a": 1}


# --- human-in-the-loop approval gate ---


def test_gated_run_interrupts_before_finalizing():
    settings = _settings()
    graph = build_graph(settings, model=MockChatModel())
    result = graph.invoke(
        initial_state(INCIDENT, settings, require_approval=True), _cfg()
    )

    # The graph paused at the approval gate rather than producing a report.
    assert "__interrupt__" in result
    assert "report" not in result
    payload = result["__interrupt__"][0].value
    assert payload["type"] == "approval_request"
    assert payload["root_cause"]  # the human sees the proposal before deciding


def test_resume_approve_finalizes_report():
    settings = _settings()
    graph = build_graph(settings, model=MockChatModel())
    cfg = _cfg()
    graph.invoke(initial_state(INCIDENT, settings, require_approval=True), cfg)

    final = graph.invoke(Command(resume={"action": "approve", "note": "lgtm"}), cfg)
    assert final["report"]["approved"] is True
    assert final["report"]["root_cause"]
    assert final["report"]["decision"]["note"] == "lgtm"


def test_resume_reject_marks_not_approved():
    settings = _settings()
    graph = build_graph(settings, model=MockChatModel())
    cfg = _cfg()
    graph.invoke(initial_state(INCIDENT, settings, require_approval=True), cfg)

    final = graph.invoke(Command(resume={"action": "reject", "note": "risky"}), cfg)
    assert final["report"]["approved"] is False


def test_non_gated_run_never_interrupts():
    settings = _settings()
    graph = build_graph(settings, model=MockChatModel())
    final = graph.invoke(
        initial_state(INCIDENT, settings, require_approval=False), _cfg()
    )
    assert "__interrupt__" not in final
    assert final["report"]["approved"] is True


# --- incident memory ---


def test_recall_primes_synthesis_and_run_remembers():
    settings = _settings()
    memory = IncidentMemory(k=3)
    memory.remember("Alert: OOMKilled\nRoot cause: memory limit too low\nFix: raise it")
    graph = build_graph(settings, model=MockChatModel(), memory=memory)

    final = graph.invoke(initial_state(INCIDENT, settings), _cfg())

    # recall found the seeded incident and surfaced it into state...
    assert final["similar_incidents"]
    recall_runs = [h for h in final["history"] if h.get("node") == "recall"]
    assert recall_runs and recall_runs[0]["recalled"] >= 1
    # ...and this run was remembered for next time.
    assert memory.size() == 2
    assert any(h.get("node") == "remember" and h.get("stored") for h in final["history"])


def test_memory_learns_across_runs():
    settings = _settings()
    memory = IncidentMemory(k=3)  # starts empty
    graph = build_graph(settings, model=MockChatModel(), memory=memory)

    first = graph.invoke(initial_state(INCIDENT, settings), _cfg())
    assert first["similar_incidents"] == []  # nothing to recall yet

    second = graph.invoke(initial_state(INCIDENT, settings), _cfg())
    assert second["similar_incidents"]  # the first run is now recalled


def test_memory_disabled_skips_recall_and_remember():
    settings = _settings()
    graph = build_graph(settings, model=MockChatModel(), memory=None)
    final = graph.invoke(initial_state(INCIDENT, settings), _cfg())

    nodes = {h.get("node") for h in final["history"]}
    assert "recall" not in nodes
    assert "remember" not in nodes
    assert final["report"]["root_cause"]  # still produces a report


def test_rejected_proposal_is_not_remembered():
    settings = _settings()
    memory = IncidentMemory(k=3)
    graph = build_graph(settings, model=MockChatModel(), memory=memory)
    cfg = _cfg()

    graph.invoke(initial_state(INCIDENT, settings, require_approval=True), cfg)
    graph.invoke(Command(resume={"action": "reject", "note": "no"}), cfg)

    assert memory.size() == 0  # a rejected fix must not pollute memory
