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

package reasoner

import (
	"context"

	incidentsv1alpha1 "github.com/yih0nk/hivemind/api/v1alpha1"
	"github.com/yih0nk/hivemind/internal/agents"
)

// InProcess adapts the in-cluster synthesizer agent to the Reasoner seam.
// It is the default: no external service, one LLM pass, identical behavior
// to the pre-seam operator.
type InProcess struct {
	Agent agents.Agent
}

var _ Reasoner = InProcess{}

// NewInProcess wraps a synthesizer agent as a Reasoner.
func NewInProcess(agent agents.Agent) InProcess {
	return InProcess{Agent: agent}
}

func (i InProcess) Name() string { return i.Agent.Name() }

func (i InProcess) Synthesize(ctx context.Context, triage *incidentsv1alpha1.IncidentTriage) (string, error) {
	return i.Agent.Run(ctx, triage)
}
