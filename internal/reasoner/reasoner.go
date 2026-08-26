/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package reasoner is the seam between the operator's control plane and
// whatever turns gathered evidence into a root-cause report. The operator
// still owns evidence collection (it holds the cluster credentials); the
// reasoner owns synthesis. Two implementations satisfy it: InProcess, which
// wraps the in-cluster LLM synthesizer agent, and HTTPReasoner, which calls
// the external LangGraph brain over HTTP.
package reasoner

import (
	"context"

	incidentsv1alpha1 "github.com/yih0nk/hivemind/api/v1alpha1"
)

// SynthesizerKey is the status.agentOutputs key the synthesis report is
// stored under. The PR renderer keys its "Root Cause & Recommended Fix"
// section off the same string, so every Reasoner must report under it for
// the report to render -- part of the CR's observable contract, spelled
// out here rather than imported from the agents package (which spells it
// out too, for the same reason).
const SynthesizerKey = "synthesizer"

// Reasoning outcome statuses.
const (
	// StatusCompleted means Report holds the finished synthesis.
	StatusCompleted = "completed"
	// StatusAwaitingApproval means the run paused at the approval gate;
	// ThreadID resumes it and Proposal summarizes what awaits a decision.
	StatusAwaitingApproval = "awaiting_approval"
)

// Result is the outcome of a synthesis or resume. A completed result carries
// the report; an awaiting-approval result carries the thread id to resume with
// and a human-readable proposal summary.
type Result struct {
	Status   string
	Report   string // synthesizer-schema JSON, when Status == completed
	ThreadID string // when Status == awaiting_approval
	Proposal string // human-readable root cause/fix, when awaiting approval
}

// Reasoner produces the root-cause report from the evidence the operator
// has already gathered into triage.Status.AgentOutputs. A completed Result's
// Report is a JSON string in the synthesizer schema; it lands verbatim in
// status.agentOutputs[Name()] and is rendered into the incident PR.
type Reasoner interface {
	// Name is the status.agentOutputs key the report is stored under.
	Name() string
	// Synthesize runs the reasoning over the upstream agent outputs. When
	// triage.Spec.RequireApproval is set and the reasoner supports it, the
	// Result may be StatusAwaitingApproval instead of a finished report.
	Synthesize(ctx context.Context, triage *incidentsv1alpha1.IncidentTriage) (Result, error)
	// Resume continues a run paused at the approval gate with a human
	// decision. approve=false records a rejection. Reasoners that cannot
	// pause return an error.
	Resume(ctx context.Context, threadID string, approve bool, note string) (Result, error)
}
