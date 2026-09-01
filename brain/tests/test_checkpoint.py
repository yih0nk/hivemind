"""Tests for the graph checkpointer (in-memory vs SQLite)."""

from __future__ import annotations

import sqlite3

from langgraph.checkpoint.memory import MemorySaver
from langgraph.checkpoint.sqlite import SqliteSaver
from langgraph.types import Command

from hivemind_brain.config import Settings
from hivemind_brain.graph import build_graph, default_checkpointer, initial_state
from hivemind_brain.llm import MockChatModel

INCIDENT = {"alert": "OOMKilled", "logs": "OOMKilled", "metrics": "m", "runbooks": []}


def test_default_checkpointer_in_memory_without_path():
    assert isinstance(default_checkpointer(Settings(checkpoint_path="")), MemorySaver)


def test_default_checkpointer_sqlite_with_path(tmp_path):
    cp = default_checkpointer(Settings(checkpoint_path=str(tmp_path / "c.sqlite")))
    assert isinstance(cp, SqliteSaver)


def test_paused_run_survives_restart_via_sqlite(tmp_path):
    path = str(tmp_path / "ckpt.sqlite")
    settings = Settings(provider="mock", memory_enabled=False)
    cfg = {"configurable": {"thread_id": "t1"}}

    # "Process 1": start a gated run and pause at the approval gate.
    conn1 = sqlite3.connect(path, check_same_thread=False)
    g1 = build_graph(settings, model=MockChatModel(), memory=None,
                     checkpointer=SqliteSaver(conn1))
    r1 = g1.invoke(initial_state(INCIDENT, settings, require_approval=True), cfg)
    assert "__interrupt__" in r1
    conn1.close()

    # "Process 2": a fresh connection + graph on the same file resumes the run.
    conn2 = sqlite3.connect(path, check_same_thread=False)
    g2 = build_graph(settings, model=MockChatModel(), memory=None,
                     checkpointer=SqliteSaver(conn2))
    r2 = g2.invoke(Command(resume={"action": "approve", "note": "ok"}), cfg)
    assert r2["report"]["approved"] is True
    assert r2["report"]["root_cause"]
    conn2.close()
