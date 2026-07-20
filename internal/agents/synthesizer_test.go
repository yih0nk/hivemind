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

package agents

import (
	"strings"
	"testing"

	incidentsv1alpha1 "github.com/yih0nk/hivemind/api/v1alpha1"
	"github.com/yih0nk/hivemind/internal/llm"
)

const (
	testLogReport  = `{"likelyCause":"OOM"}`
	validSynthesis = `{"rootCause":"memory leak in checkout","recommendedFix":"roll back v1.4.2","confidence":0.85,"severity":"critical","estimatedImpact":"checkout unavailable"}`
)

func fullUpstreamOutputs() map[string]string {
	return map[string]string{
		logTriageName:         testLogReport,
		metricsCorrelatorName: `{"memoryTrend":"rising"}`,
		runbookLookupName:     `{"matches":[{"name":"OOMKill"}]}`,
	}
}

func TestSynthesizerAgentRun(t *testing.T) {
	tests := []struct {
		name        string
		outputs     map[string]string
		llmResponse string

		wantOutput         string
		wantOutputContains []string
		wantErrContains    string
		wantLLMCalled      bool
		promptContains     []string
	}{
		{
			name:          "all upstream outputs present",
			outputs:       fullUpstreamOutputs(),
			llmResponse:   validSynthesis,
			wantOutput:    validSynthesis,
			wantLLMCalled: true,
			promptContains: []string{
				testLogReport,
				`{"memoryTrend":"rising"}`,
				`{"matches":[{"name":"OOMKill"}]}`,
				testAlert,
			},
		},
		{
			name:            "no upstream outputs is an error",
			outputs:         nil,
			wantErrContains: "upstream agent outputs not yet available",
		},
		{
			name: "partial upstream outputs still synthesize",
			outputs: map[string]string{
				logTriageName: testLogReport,
			},
			llmResponse:    validSynthesis,
			wantOutput:     validSynthesis,
			wantLLMCalled:  true,
			promptContains: []string{testLogReport},
		},
		{
			// Seen live from llama3.2-vision: recommendedFix as a list of
			// step objects instead of the requested string. The report
			// embeds the JSON verbatim, so the shape drift is harmless.
			name:    "recommendedFix as a structured list is accepted",
			outputs: fullUpstreamOutputs(),
			llmResponse: `{"rootCause":"OOM","recommendedFix":[{"step":1,"action":"raise the chaos-crashloop memory limit"}],` +
				`"confidence":1.0,"severity":"critical","estimatedImpact":"High"}`,
			wantOutput: `{"rootCause":"OOM","recommendedFix":[{"step":1,"action":"raise the chaos-crashloop memory limit"}],` +
				`"confidence":1.0,"severity":"critical","estimatedImpact":"High"}`,
			wantLLMCalled: true,
		},
		{
			name:        softErrorCaseName,
			outputs:     fullUpstreamOutputs(),
			llmResponse: "Root cause: probably the deploy.",
			wantOutputContains: []string{
				`"agent":"synthesizer"`,
				wantSoftErrorStatus,
				wantSoftErrorSummary,
			},
			wantLLMCalled: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeLLM := &llm.FakeLLMClient{Response: tt.llmResponse}
			agent := NewSynthesizerAgent(fakeLLM)

			triage := &incidentsv1alpha1.IncidentTriage{
				Spec: incidentsv1alpha1.IncidentTriageSpec{
					AlertName:         testAlert,
					Severity:          incidentsv1alpha1.SeverityCritical,
					AffectedNamespace: testNamespace,
				},
				Status: incidentsv1alpha1.IncidentTriageStatus{
					AgentOutputs: tt.outputs,
				},
			}

			out, err := agent.Run(t.Context(), triage)

			if calls := fakeLLM.Calls(); tt.wantLLMCalled != (len(calls) == 1) {
				t.Errorf("LLM called %d times, wantLLMCalled=%v", len(calls), tt.wantLLMCalled)
			}

			if tt.wantErrContains != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErrContains) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErrContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("Run() unexpected error: %v", err)
			}
			if tt.wantOutput != "" && out != tt.wantOutput {
				t.Errorf("Run() = %q, want %q", out, tt.wantOutput)
			}
			for _, want := range tt.wantOutputContains {
				if !strings.Contains(out, want) {
					t.Errorf("Run() = %q, missing %q", out, want)
				}
			}

			calls := fakeLLM.Calls()
			for _, want := range tt.promptContains {
				if !strings.Contains(calls[0].UserPrompt, want) {
					t.Errorf("user prompt missing %q:\n%s", want, calls[0].UserPrompt)
				}
			}
		})
	}
}

func TestSynthesizerAgentName(t *testing.T) {
	if got := NewSynthesizerAgent(nil).Name(); got != synthesizerName {
		t.Errorf("Name() = %q, want %q", got, synthesizerName)
	}
}
