"""Tests for the evidence tools."""

from __future__ import annotations

from hivemind_brain.tools import (
    build_lc_tools,
    get_metrics,
    list_runbooks,
    read_runbook,
    run_tool,
    search_logs,
    tool_descriptions,
)

INCIDENT = {
    "alert": "OOMKilled",
    "logs": "line one ok\nOOMKilled: checkout\nline three ok\nOOMKilled again",
    "metrics": "memory climbing to limit",
    "runbooks": [
        {"name": "OOMKill", "content": "raise the memory limit"},
        {"name": "CrashLoop", "content": "check exit code"},
    ],
}


def test_search_logs_matches_case_insensitively():
    out = search_logs(INCIDENT, "oomkilled")
    assert out.count("OOMKilled") == 2
    assert "line one" not in out


def test_search_logs_no_match():
    assert "no log lines match" in search_logs(INCIDENT, "zzz")


def test_get_metrics():
    assert get_metrics(INCIDENT) == "memory climbing to limit"


def test_list_runbooks():
    assert list_runbooks(INCIDENT) == "OOMKill, CrashLoop"


def test_read_runbook_exact_then_substring():
    assert read_runbook(INCIDENT, "OOMKill") == "raise the memory limit"
    assert read_runbook(INCIDENT, "oom") == "raise the memory limit"  # substring
    assert "no runbook named" in read_runbook(INCIDENT, "nope")


def test_run_tool_dispatch_and_unknown():
    assert run_tool("get_metrics", INCIDENT, "") == "memory climbing to limit"
    assert "unknown tool" in run_tool("nope", INCIDENT, "")


def test_tool_descriptions_lists_all_tools():
    desc = tool_descriptions()
    for name in ("search_logs", "get_metrics", "list_runbooks", "read_runbook"):
        assert name in desc


def test_build_lc_tools_names_and_bound_incident():
    tools = build_lc_tools(INCIDENT)
    assert [t.name for t in tools] == [
        "search_logs", "get_metrics", "list_runbooks", "read_runbook",
    ]
    by_name = {t.name: t for t in tools}
    # Each tool closes over INCIDENT; the model only supplies the query arg.
    assert "OOMKilled" in by_name["search_logs"].invoke({"pattern": "oomkilled"})
    assert by_name["get_metrics"].invoke({}) == "memory climbing to limit"
    assert by_name["read_runbook"].invoke({"name": "OOMKill"}) == "raise the memory limit"
