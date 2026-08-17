"""Request/response contract for the /triage endpoint.

This is the wire contract the Go operator will code against next session: it
POSTs the raw incident evidence it already gathers today, and gets back a
root-cause report to drop into the remediation PR.
"""

from __future__ import annotations

from typing import Any

from pydantic import BaseModel, Field


class Runbook(BaseModel):
    name: str
    content: str


class TriageRequest(BaseModel):
    alert: str = Field(..., description="Firing alert name, e.g. 'OOMKilled'.")
    namespace: str = Field("", description="Namespace of the affected workload.")
    pod: str = Field("", description="Affected pod name, if known.")
    logs: str = Field("", description="Raw pod logs gathered by the operator.")
    metrics: str = Field("", description="Prometheus/metrics context, rendered.")
    runbooks: list[Runbook] = Field(
        default_factory=list, description="Candidate runbooks matched by the operator."
    )
    max_iterations: int | None = Field(
        None, ge=1, le=10, description="Override the reflection-loop cap."
    )
    confidence_threshold: float | None = Field(
        None, ge=0.0, le=1.0, description="Override the confidence gate."
    )
    require_approval: bool = Field(
        False,
        description="Pause for a human decision before finalizing. When true the "
        "response has status 'awaiting_approval' and a thread_id to resume with.",
    )

    def to_incident(self) -> dict[str, Any]:
        return {
            "alert": self.alert,
            "namespace": self.namespace,
            "pod": self.pod,
            "logs": self.logs,
            "metrics": self.metrics,
            "runbooks": [rb.model_dump() for rb in self.runbooks],
        }


class TriageResponse(BaseModel):
    # status is "completed" (report fields populated) or "awaiting_approval"
    # (approval_request + thread_id populated, report fields empty). The flat
    # report fields stay top-level for backward compatibility with the Go
    # operator, which decodes them directly and ignores the rest.
    status: str = "completed"
    thread_id: str = ""
    approval_request: dict[str, Any] | None = None
    approved: bool = True

    root_cause: str = ""
    proposed_fix: str = ""
    confidence: float = 0.0
    iterations: int = 0
    critique: str = ""
    evidence: dict[str, Any] = Field(default_factory=dict)
    history: list[dict[str, Any]] = Field(default_factory=list)


class ResumeRequest(BaseModel):
    thread_id: str = Field(..., description="thread_id from an awaiting_approval response.")
    action: str = Field("approve", description="'approve' or 'reject'.")
    note: str = Field("", description="Optional reviewer note, recorded on the report.")
