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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// Severity classifies how urgent the incident is.
// +kubebuilder:validation:Enum=critical;warning;info
type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityWarning  Severity = "warning"
	SeverityInfo     Severity = "info"
)

// TriagePhase tracks where the triage run is in its lifecycle.
// +kubebuilder:validation:Enum=Pending;Triaging;Remediated;Failed
type TriagePhase string

const (
	PhasePending    TriagePhase = "Pending"
	PhaseTriaging   TriagePhase = "Triaging"
	PhaseRemediated TriagePhase = "Remediated"
	PhaseFailed     TriagePhase = "Failed"
)

// IncidentTriageSpec is the user's request: what fired and where to look.
type IncidentTriageSpec struct {
	// alertName is the alert that triggered this triage (e.g. from Alertmanager).
	// +required
	AlertName string `json:"alertName"`

	// severity is how urgent the incident is.
	// +required
	Severity Severity `json:"severity"`

	// affectedNamespace scopes which namespace the agents investigate.
	// +required
	AffectedNamespace string `json:"affectedNamespace"`

	// affectedPodSelector narrows investigation to pods matching these labels.
	// +optional
	AffectedPodSelector map[string]string `json:"affectedPodSelector,omitempty"`

	// prometheusURL is where the metrics-correlator agent queries.
	// +optional
	PrometheusURL string `json:"prometheusURL,omitempty"`

	// githubRepo (owner/repo) is where the root-cause PR gets opened.
	// +optional
	GithubRepo string `json:"githubRepo,omitempty"`
}

// IncidentTriageStatus is what the operator observed and produced.
type IncidentTriageStatus struct {
	// phase is the current lifecycle stage of the triage run.
	// +optional
	Phase TriagePhase `json:"phase,omitempty"`

	// agentOutputs maps agent name (log-triage, metrics-correlator, ...) to its report.
	// +optional
	AgentOutputs map[string]string `json:"agentOutputs,omitempty"`

	// prURL links to the root-cause report PR once opened.
	// +optional
	PRURL string `json:"prURL,omitempty"`

	// startTime is when agent dispatch began.
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// completionTime is when the triage run finished (success or failure).
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// message is a human-readable note about the current phase.
	// +optional
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Severity",type=string,JSONPath=`.spec.severity`
// +kubebuilder:printcolumn:name="PR",type=string,JSONPath=`.status.prURL`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// IncidentTriage is the Schema for the incidenttriages API
type IncidentTriage struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of IncidentTriage
	// +required
	Spec IncidentTriageSpec `json:"spec"`

	// status defines the observed state of IncidentTriage
	// +optional
	Status IncidentTriageStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// IncidentTriageList contains a list of IncidentTriage
type IncidentTriageList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []IncidentTriage `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &IncidentTriage{}, &IncidentTriageList{})
		return nil
	})
}
