"""Tests for the ReAct gather mode."""

from __future__ import annotations

import itertools

from hivemind_brain.config import Settings
from hivemind_brain.graph import build_graph, initial_state
from hivemind_brain.llm import MockChatModel
from hivemind_brain.nodes import make_react_gather

INCIDENT = {
    "alert": "OOMKilled",
    "namespace": "prod",
    "pod": "checkout-7d9",
    "logs": "boot ok\nOOMKilled: checkout\nrestart",
    "metrics": "mem climbing to limit",
    "runbooks": [{"name": "OOMKill", "content": "raise limits"}],
}

_tids = (f"react-{n}" for n in itertools.count())


def _cfg() -> dict:
    return {"configurable": {"thread_id": next(_tids)}}


def _react_settings() -> Settings:
    return Settings(provider="mock", gather_mode="react", memory_enabled=False)


def test_react_gather_calls_a_tool_then_produces_evidence():
    # Unit-level: the node runs a tool step before finishing.
    node = make_react_gather(MockChatModel())
    out = node({"incident": INCIDENT})

    assert out["evidence"]["logs"]
    hist = out["history"][0]
    assert hist["mode"] == "react"
    assert hist["trace"], "expected at least one tool call in the trace"
    assert hist["trace"][0]["action"] == "search_logs"
    # The tool actually ran against the bundle (matched the OOMKilled line).
    assert "OOMKilled" in hist["trace"][0]["observation"]


def test_react_mode_graph_reaches_report():
    settings = _react_settings()
    graph = build_graph(settings, model=MockChatModel(), memory=None)
    final = graph.invoke(initial_state(INCIDENT, settings), _cfg())

    assert final["report"]["root_cause"]
    gather_hist = [h for h in final["history"] if h.get("node") == "gather"]
    assert gather_hist and gather_hist[0]["mode"] == "react"


def test_react_max_steps_truncates_without_finishing():
    # A model that never emits done -> the node must stop at max_steps and
    # still hand back the raw bundle rather than looping forever.
    class NeverDone:
        def invoke(self, messages):
            from langchain_core.messages import AIMessage

            return AIMessage(
                content='{"thought":"keep looking","action":"get_metrics","action_input":""}'
            )

    node = make_react_gather(NeverDone(), max_steps=3)
    out = node({"incident": INCIDENT})
    hist = out["history"][0]
    assert hist["truncated"] is True
    assert hist["steps"] == 3
    assert out["evidence"]["logs"] == INCIDENT["logs"]  # fell back to raw bundle
