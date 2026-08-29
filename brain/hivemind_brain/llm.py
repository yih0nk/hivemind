"""Provider-agnostic chat model + JSON-robust invocation helpers.

The real path is LangChain's ``ChatGroq``; the fallback is a deterministic
``MockChatModel`` so the whole graph runs in tests and offline. Both expose the
same tiny surface the nodes rely on: ``.invoke(list[BaseMessage]) -> AIMessage``.

``call_json`` mirrors the Go operator's ``firstJSONValue`` lesson — small LLMs
happily emit prose or a second fenced block around the JSON we asked for, so we
extract the first balanced JSON object rather than trusting ``json.loads`` on
the raw string.
"""

from __future__ import annotations

import json
from typing import Any

from langchain_core.messages import AIMessage, BaseMessage, HumanMessage, SystemMessage

from .config import Settings


class MockChatModel:
    """Deterministic stand-in for a real chat model.

    It inspects the task tag embedded in the system prompt and returns
    plausible JSON for that node. The critique node deliberately reports low
    confidence on the first round and high confidence afterwards, so the
    reflection loop exercises at least one cycle before converging.
    """

    def __init__(self) -> None:
        self._critique_calls = 0
        self._react_calls = 0

    def invoke(self, messages: list[BaseMessage]) -> AIMessage:
        text = "\n".join(str(m.content) for m in messages).lower()

        if "task: react_gather" in text:
            # First step: call a tool. Then finish with distilled evidence.
            self._react_calls += 1
            if self._react_calls == 1:
                return AIMessage(
                    content=json.dumps(
                        {
                            "thought": "Check the logs for the failure signature.",
                            "action": "search_logs",
                            "action_input": "OOMKilled",
                        }
                    )
                )
            return AIMessage(
                content=json.dumps(
                    {
                        "thought": "Enough evidence gathered.",
                        "done": True,
                        "evidence": {
                            "logs": "Repeated OOMKilled events preceding each restart.",
                            "metrics": "Memory usage climbs to the limit before each kill.",
                            "runbooks": "OOMKill runbook: raise limits / find the leak.",
                        },
                    }
                )
            )

        if "task: critique" in text:
            self._critique_calls += 1
            confidence = 0.55 if self._critique_calls == 1 else 0.9
            return AIMessage(
                content=json.dumps(
                    {
                        "confidence": confidence,
                        "critique": (
                            "Evidence is thin on the metrics side; correlate the "
                            "restart count with memory saturation."
                            if confidence < 0.75
                            else "Root cause is well supported by logs and metrics."
                        ),
                        "guidance": (
                            "Re-summarize focusing on memory-pressure signals."
                            if confidence < 0.75
                            else ""
                        ),
                    }
                )
            )

        if "task: synthesize" in text:
            return AIMessage(
                content=json.dumps(
                    {
                        "root_cause": (
                            "Container exceeded its memory limit and was OOMKilled "
                            "under load, triggering a restart loop."
                        ),
                        "proposed_fix": (
                            "Raise the memory limit and add a memory-based HPA; "
                            "investigate the allocation spike in the request path."
                        ),
                    }
                )
            )

        # Default: the gather/evidence node.
        return AIMessage(
            content=json.dumps(
                {
                    "logs": "Repeated OOMKilled events preceding each restart.",
                    "metrics": "Memory usage climbs to the limit before each kill.",
                    "runbooks": "OOMKill runbook: raise limits / find the leak.",
                }
            )
        )


def build_model(settings: Settings) -> Any:
    """Return a chat model for the resolved provider.

    ``groq`` builds a real ``ChatGroq``; anything else (or a missing key) yields
    the deterministic mock. Import of ``langchain_groq`` is lazy so the mock
    path has no hard dependency on it.
    """
    if settings.resolved_provider == "groq":
        from langchain_groq import ChatGroq

        return ChatGroq(
            model=settings.groq_model,
            api_key=settings.groq_api_key,
            temperature=0.1,
            timeout=settings.request_timeout_s,
        )
    return MockChatModel()


def _first_json(text: str) -> dict[str, Any]:
    """Extract the first balanced JSON object from ``text``.

    Robust to leading prose, ```json fences, and trailing second blocks.
    Raises ``ValueError`` if no balanced object is found.
    """
    start = text.find("{")
    while start != -1:
        depth = 0
        in_str = False
        escape = False
        for i in range(start, len(text)):
            ch = text[i]
            if in_str:
                if escape:
                    escape = False
                elif ch == "\\":
                    escape = True
                elif ch == '"':
                    in_str = False
                continue
            if ch == '"':
                in_str = True
            elif ch == "{":
                depth += 1
            elif ch == "}":
                depth -= 1
                if depth == 0:
                    candidate = text[start : i + 1]
                    try:
                        return json.loads(candidate)
                    except json.JSONDecodeError:
                        break  # malformed; advance to the next '{'
        start = text.find("{", start + 1)
    raise ValueError(f"no balanced JSON object found in model output: {text!r}")


def call_json(
    model: Any, system: str, user: str
) -> dict[str, Any]:
    """Invoke ``model`` with a system+user prompt and parse a JSON object out."""
    messages: list[BaseMessage] = [
        SystemMessage(content=system),
        HumanMessage(content=user),
    ]
    response = model.invoke(messages)
    content = getattr(response, "content", response)
    return _first_json(str(content))
