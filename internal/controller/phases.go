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

package controller

import (
	incidentsv1alpha1 "github.com/yihanhong/hivemind/api/v1alpha1"
)

// NextPhase encodes the triage state machine:
//
//	"" -> Pending -> Triaging -> Remediated
//
// Remediated and Failed are terminal and return themselves. Any transition
// the reconciler performs must come from here so the lifecycle has exactly
// one source of truth.
func NextPhase(current incidentsv1alpha1.TriagePhase) incidentsv1alpha1.TriagePhase {
	switch current {
	case "":
		return incidentsv1alpha1.PhasePending
	case incidentsv1alpha1.PhasePending:
		return incidentsv1alpha1.PhaseTriaging
	case incidentsv1alpha1.PhaseTriaging:
		return incidentsv1alpha1.PhaseRemediated
	default:
		return current
	}
}

// IsTerminal reports whether a phase ends the triage lifecycle.
func IsTerminal(phase incidentsv1alpha1.TriagePhase) bool {
	return phase == incidentsv1alpha1.PhaseRemediated || phase == incidentsv1alpha1.PhaseFailed
}
