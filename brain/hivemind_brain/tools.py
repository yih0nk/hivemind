"""Evidence tools the ReAct gather agent calls over one incident's bundle.

The brain has no cluster access -- it reasons over the evidence the operator
POSTs in. These tools expose that bundle as discrete, queryable actions, so a
tool-choosing agent can *investigate* (grep the logs for a pattern, read a
specific runbook) instead of summarizing everything blind in one pass. Each tool
takes the incident dict plus a string argument and returns a string observation.
"""

from __future__ import annotations

from typing import Any, Callable

Tool = Callable[[dict[str, Any], str], str]

_MAX_MATCHES = 50
_MAX_LEN = 2000


def search_logs(incident: dict[str, Any], pattern: str) -> str:
    """Return log lines containing the (case-insensitive) substring pattern."""
    logs = incident.get("logs", "") or ""
    if not pattern:
        return logs[:_MAX_LEN] or "(no logs)"
    hits = [ln for ln in logs.splitlines() if pattern.lower() in ln.lower()]
    if not hits:
        return f"(no log lines match {pattern!r})"
    return "\n".join(hits[:_MAX_MATCHES])


def get_metrics(incident: dict[str, Any], _arg: str = "") -> str:
    """Return the metrics context for the incident."""
    return (incident.get("metrics", "") or "(no metrics)")[:_MAX_LEN]


def list_runbooks(incident: dict[str, Any], _arg: str = "") -> str:
    """Return the names of the candidate runbooks."""
    names = [rb.get("name", "") for rb in incident.get("runbooks", []) if rb.get("name")]
    return ", ".join(names) or "(no runbooks)"


def read_runbook(incident: dict[str, Any], name: str) -> str:
    """Return a runbook's content by exact name, then by substring match."""
    runbooks = incident.get("runbooks", [])
    for rb in runbooks:
        if rb.get("name", "").lower() == (name or "").lower():
            return rb.get("content", "")[:_MAX_LEN]
    for rb in runbooks:
        if name and name.lower() in rb.get("name", "").lower():
            return rb.get("content", "")[:_MAX_LEN]
    return f"(no runbook named {name!r})"


# name -> (fn, one-line description shown to the agent)
TOOLS: dict[str, tuple[Tool, str]] = {
    "search_logs": (search_logs, "search_logs(pattern): log lines containing a substring"),
    "get_metrics": (get_metrics, "get_metrics(): the metrics/trends context"),
    "list_runbooks": (list_runbooks, "list_runbooks(): names of candidate runbooks"),
    "read_runbook": (read_runbook, "read_runbook(name): a named runbook's content"),
}


def run_tool(name: str, incident: dict[str, Any], arg: str) -> str:
    """Dispatch to a tool by name; unknown tools return an error observation."""
    entry = TOOLS.get(name)
    if entry is None:
        return f"(unknown tool {name!r}; available: {', '.join(TOOLS)})"
    return entry[0](incident, arg or "")


def tool_descriptions() -> str:
    """A bulleted list of tool signatures for the agent's system prompt."""
    return "\n".join(f"- {desc}" for _, desc in TOOLS.values())
