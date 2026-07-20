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

// Package agents defines the triage agent seam and the dispatcher that
// fans agents out concurrently during the Triaging phase.
package agents

import (
	"context"

	incidentsv1alpha1 "github.com/yih0nk/hivemind/api/v1alpha1"
)

// Agent is one triage specialist (log-triage, metrics-correlator,
// runbook-lookup, synthesizer). Run returns the agent's report for
// status.agentOutputs. Implementations must treat triage as read-only:
// only the reconciler writes to the cluster.
type Agent interface {
	Name() string
	Run(ctx context.Context, triage *incidentsv1alpha1.IncidentTriage) (string, error)
}
