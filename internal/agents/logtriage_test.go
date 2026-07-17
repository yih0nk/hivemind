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
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	incidentsv1alpha1 "github.com/yihanhong/hivemind/api/v1alpha1"
	"github.com/yihanhong/hivemind/internal/llm"
)

const (
	testAlert     = "PodCrashLooping"
	testNamespace = "payments"
	validReport   = `{"errorLines":["OOMKilled"],"likelyCause":"memory leak","confidence":0.8,"recommendedAction":"raise limits"}`
)

// fakeLogReader serves canned logs keyed by pod name.
type fakeLogReader struct {
	logs map[string]string
	err  error
}

func (f *fakeLogReader) PodLogs(_ context.Context, _, pod string, _ *corev1.PodLogOptions) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.logs[pod], nil
}

func testPod(name string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name:      name,
		Namespace: testNamespace,
		Labels:    map[string]string{"app": "checkout"},
	}}
}

func TestLogTriageAgentRun(t *testing.T) {
	tests := []struct {
		name        string
		pods        []client.Object
		logReader   *fakeLogReader
		llmResponse string
		llmErr      error

		wantOutput         string
		wantOutputContains []string
		wantErrContains    string
		promptContains     []string
		promptExcludes     []string
	}{
		{
			name:        "pods found and LLM returns valid JSON",
			pods:        []client.Object{testPod("checkout-1")},
			logReader:   &fakeLogReader{logs: map[string]string{"checkout-1": "panic: out of memory"}},
			llmResponse: validReport,
			wantOutput:  validReport,
			promptContains: []string{
				"=== pod checkout-1 ===",
				"panic: out of memory",
			},
		},
		{
			name:        "no pods matched is reported to the LLM, not an error",
			pods:        nil,
			logReader:   &fakeLogReader{},
			llmResponse: validReport,
			wantOutput:  validReport,
			promptContains: []string{
				fmt.Sprintf("%s %q", noPodsMessage, testNamespace),
			},
		},
		{
			name:        softErrorCaseName,
			pods:        []client.Object{testPod("checkout-1")},
			logReader:   &fakeLogReader{},
			llmResponse: "Sure! The problem is probably DNS.",
			wantOutputContains: []string{
				`"agent":"logtriage"`,
				wantSoftErrorStatus,
				wantSoftErrorSummary,
			},
		},
		{
			name:            "LLM failure propagates",
			pods:            []client.Object{testPod("checkout-1")},
			logReader:       &fakeLogReader{},
			llmErr:          errors.New("ollama unreachable"),
			wantErrContains: "ollama unreachable",
		},
		{
			name:        "code-fenced JSON is unwrapped",
			pods:        []client.Object{testPod("checkout-1")},
			logReader:   &fakeLogReader{},
			llmResponse: "```json\n" + validReport + "\n```",
			wantOutput:  validReport,
		},
		{
			name:        "two fenced JSON blocks keep only the first value",
			pods:        []client.Object{testPod("checkout-1")},
			logReader:   &fakeLogReader{},
			llmResponse: "```json\n" + validReport + "\n```\n\n```json\n{\"errorLines\":[]}\n```",
			wantOutput:  validReport,
		},
		{
			name: "only the first three pods contribute logs",
			pods: []client.Object{
				testPod("checkout-1"), testPod("checkout-2"),
				testPod("checkout-3"), testPod("checkout-4"),
			},
			logReader:      &fakeLogReader{},
			llmResponse:    validReport,
			wantOutput:     validReport,
			promptContains: []string{"checkout-3"},
			promptExcludes: []string{"checkout-4"},
		},
		{
			name:        "unreadable pod logs are noted instead of failing",
			pods:        []client.Object{testPod("checkout-1")},
			logReader:   &fakeLogReader{err: errors.New("container is creating")},
			llmResponse: validReport,
			wantOutput:  validReport,
			promptContains: []string{
				"<logs unavailable:",
				"container is creating",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(clientgoscheme.Scheme).WithObjects(tt.pods...).Build()
			fakeLLM := &llm.FakeLLMClient{Response: tt.llmResponse, Err: tt.llmErr}
			agent := NewLogTriageAgent(c, tt.logReader, fakeLLM)

			triage := &incidentsv1alpha1.IncidentTriage{
				Spec: incidentsv1alpha1.IncidentTriageSpec{
					AlertName:           testAlert,
					Severity:            incidentsv1alpha1.SeverityCritical,
					AffectedNamespace:   testNamespace,
					AffectedPodSelector: map[string]string{"app": "checkout"},
				},
			}

			out, err := agent.Run(t.Context(), triage)

			if tt.wantErrContains != "" {
				if err == nil {
					t.Fatalf("Run() = %q, want error containing %q", out, tt.wantErrContains)
				}
				if !strings.Contains(err.Error(), tt.wantErrContains) {
					t.Fatalf("error %q does not contain %q", err, tt.wantErrContains)
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
			if len(calls) != 1 {
				t.Fatalf("LLM called %d times, want 1", len(calls))
			}
			if !strings.Contains(calls[0].SystemPrompt, "SRE") || !strings.Contains(calls[0].SystemPrompt, "JSON") {
				t.Errorf("system prompt missing role/format instructions: %q", calls[0].SystemPrompt)
			}
			for _, want := range tt.promptContains {
				if !strings.Contains(calls[0].UserPrompt, want) {
					t.Errorf("user prompt missing %q:\n%s", want, calls[0].UserPrompt)
				}
			}
			for _, exclude := range tt.promptExcludes {
				if strings.Contains(calls[0].UserPrompt, exclude) {
					t.Errorf("user prompt should not contain %q:\n%s", exclude, calls[0].UserPrompt)
				}
			}
		})
	}
}

func TestLogTriageAgentName(t *testing.T) {
	if got := (&LogTriageAgent{}).Name(); got != logTriageName {
		t.Errorf("Name() = %q, want logtriage", got)
	}
}
