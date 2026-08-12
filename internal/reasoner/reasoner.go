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

// Reasoner produces the root-cause report from the evidence the operator
// has already gathered into triage.Status.AgentOutputs. The return value
// is a JSON string in the synthesizer schema; it lands verbatim in
// status.agentOutputs[Name()] and is rendered into the incident PR.
type Reasoner interface {
	// Name is the status.agentOutputs key the report is stored under.
	Name() string
	// Synthesize returns the root-cause report as a JSON string, built
	// from triage.Spec and the upstream agent outputs in triage.Status.
	Synthesize(ctx context.Context, triage *incidentsv1alpha1.IncidentTriage) (string, error)
}
