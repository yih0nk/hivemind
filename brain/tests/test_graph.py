"""Graph-level tests on the deterministic mock model."""

from __future__ import annotations

from hivemind_brain.config import Settings
from hivemind_brain.graph import build_graph, initial_state
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


def _settings() -> Settings:
    return Settings(provider="mock", max_iterations=3, confidence_threshold=0.75)


def test_graph_reaches_confident_report():
    settings = _settings()
    graph = build_graph(settings, model=MockChatModel())
    final = graph.invoke(initial_state(INCIDENT, settings))

    report = final["report"]
    assert report["root_cause"]
    assert report["proposed_fix"]
    assert report["confidence"] >= settings.confidence_threshold


def test_reflection_loop_runs_at_least_one_extra_pass():
    # The mock reports low confidence on the first critique, so the graph must
    # loop back through `gather` at least once before converging.
    settings = _settings()
    graph = build_graph(settings, model=MockChatModel())
    final = graph.invoke(initial_state(INCIDENT, settings))

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
