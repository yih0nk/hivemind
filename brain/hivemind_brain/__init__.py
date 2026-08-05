"""Hivemind Brain — a LangGraph reasoning service for incident triage.

The Go operator (github.com/yih0nk/hivemind) owns the Kubernetes control plane:
it watches IncidentTriage CRs, gathers raw evidence, and opens remediation PRs.
This service owns the *reasoning*: a cyclic LangGraph that synthesizes a
root-cause hypothesis and critiques its own confidence, looping to gather more
until it is confident or hits an iteration cap.
"""

__version__ = "0.1.0"
