"""The graph's shared state schema.

LangGraph threads a single ``TriageState`` dict through every node. Each node
returns a *partial* update that LangGraph merges into the running state. Keys
annotated with a reducer (``history``) accumulate across nodes and across loop
iterations instead of being overwritten.
"""

from __future__ import annotations

from operator import add
from typing import Annotated, Any, TypedDict


class TriageState(TypedDict, total=False):
    # --- Input (supplied by the caller / Go operator) ---
    incident: dict[str, Any]  # alert, namespace, pod, raw logs/metrics, runbooks
    max_iterations: int
    confidence_threshold: float
    require_approval: bool  # pause for a human decision before finalizing

    # --- Working state (mutated by nodes across the loop) ---
    evidence: dict[str, Any]  # per-source summaries produced by `gather`
    hypothesis: dict[str, Any]  # {root_cause, proposed_fix} from `synthesize`
    confidence: float  # 0..1 from `critique`
    critique: str  # critic feedback, fed back into the next `gather`
    guidance: str  # what the next gather pass should focus on
    iterations: int  # completed critique rounds
    approved: bool  # human verdict from the approval gate
    decision: dict[str, Any]  # {action: approve|reject, note: str} from the human

    # Append-only audit trail of every node execution.
    history: Annotated[list[dict[str, Any]], add]

    # --- Output ---
    report: dict[str, Any]  # final root-cause report handed back to the caller
