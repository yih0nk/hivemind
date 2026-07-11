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
	"testing"

	incidentsv1alpha1 "github.com/yihanhong/hivemind/api/v1alpha1"
)

func TestNextPhase(t *testing.T) {
	tests := []struct {
		name    string
		current incidentsv1alpha1.TriagePhase
		want    incidentsv1alpha1.TriagePhase
	}{
		{"empty phase starts Pending", "", incidentsv1alpha1.PhasePending},
		{"Pending advances to Triaging", incidentsv1alpha1.PhasePending, incidentsv1alpha1.PhaseTriaging},
		{"Triaging advances to Remediated", incidentsv1alpha1.PhaseTriaging, incidentsv1alpha1.PhaseRemediated},
		{"Remediated is terminal", incidentsv1alpha1.PhaseRemediated, incidentsv1alpha1.PhaseRemediated},
		{"Failed is terminal", incidentsv1alpha1.PhaseFailed, incidentsv1alpha1.PhaseFailed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NextPhase(tt.current); got != tt.want {
				t.Errorf("NextPhase(%q) = %q, want %q", tt.current, got, tt.want)
			}
		})
	}
}

func TestIsTerminal(t *testing.T) {
	tests := []struct {
		name  string
		phase incidentsv1alpha1.TriagePhase
		want  bool
	}{
		{"empty is not terminal", "", false},
		{"Pending is not terminal", incidentsv1alpha1.PhasePending, false},
		{"Triaging is not terminal", incidentsv1alpha1.PhaseTriaging, false},
		{"Remediated is terminal", incidentsv1alpha1.PhaseRemediated, true},
		{"Failed is terminal", incidentsv1alpha1.PhaseFailed, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsTerminal(tt.phase); got != tt.want {
				t.Errorf("IsTerminal(%q) = %v, want %v", tt.phase, got, tt.want)
			}
		})
	}
}
