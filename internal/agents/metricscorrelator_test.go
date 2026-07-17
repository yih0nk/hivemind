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
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	incidentsv1alpha1 "github.com/yihanhong/hivemind/api/v1alpha1"
	"github.com/yihanhong/hivemind/internal/llm"
)

const (
	testMetricsAlert   = "HighCPU"
	validMetricsReport = `{"cpuTrend":"rising","memoryTrend":"flat","restartPattern":"burst at 10:02","anomalies":["cpu spike on checkout-1"],"confidence":0.7}`
)

// matrixBody builds a Prometheus query_range matrix response with one
// series per pod, two samples each.
func matrixBody(pods ...string) string {
	series := make([]string, 0, len(pods))
	for _, pod := range pods {
		series = append(series, fmt.Sprintf(
			`{"metric":{"pod":%q},"values":[[1752300000,"0.42"],[1752300030,"0.44"]]}`, pod))
	}
	return fmt.Sprintf(
		`{"status":"success","data":{"resultType":"matrix","result":[%s]}}`,
		strings.Join(series, ","))
}

func TestMetricsCorrelatorAgentRun(t *testing.T) {
	tests := []struct {
		name        string
		promHandler http.HandlerFunc
		llmResponse string
		llmErr      error

		wantOutput         string
		wantOutputContains []string
		wantSoftError      bool // output is {"error": ...}, LLM never called
		wantErrContains    string
		promptContains     []string
	}{
		{
			name: "metrics fetched and LLM returns valid JSON",
			promHandler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = fmt.Fprint(w, matrixBody("checkout-1"))
			},
			llmResponse: validMetricsReport,
			wantOutput:  validMetricsReport,
			promptContains: []string{
				`"checkout-1"`,
				`"0.42"`,
				`"cpu"`, `"memory"`, `"restarts"`,
			},
		},
		{
			name: "prometheus non-200 becomes a soft error output",
			promHandler: func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "storage exploded", http.StatusInternalServerError)
			},
			wantSoftError: true,
		},
		{
			name: "empty results still produce a report",
			promHandler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = fmt.Fprint(w, matrixBody())
			},
			llmResponse:    validMetricsReport,
			wantOutput:     validMetricsReport,
			promptContains: []string{`"cpu":{}`},
		},
		{
			name: softErrorCaseName,
			promHandler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = fmt.Fprint(w, matrixBody("checkout-1"))
			},
			llmResponse: "The CPU looks fine to me!",
			wantOutputContains: []string{
				`"agent":"metricscorrelator"`,
				wantSoftErrorStatus,
				wantSoftErrorSummary,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotQueries []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/query_range" {
					t.Errorf("unexpected path %q", r.URL.Path)
				}
				gotQueries = append(gotQueries, r.URL.Query().Get("query"))
				tt.promHandler(w, r)
			}))
			defer srv.Close()

			fakeLLM := &llm.FakeLLMClient{Response: tt.llmResponse, Err: tt.llmErr}
			agent := NewMetricsCorrelatorAgent(srv.URL, fakeLLM)

			triage := &incidentsv1alpha1.IncidentTriage{
				ObjectMeta: metav1.ObjectMeta{
					CreationTimestamp: metav1.NewTime(time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)),
				},
				Spec: incidentsv1alpha1.IncidentTriageSpec{
					AlertName:         testMetricsAlert,
					Severity:          incidentsv1alpha1.SeverityWarning,
					AffectedNamespace: testNamespace,
				},
			}

			out, err := agent.Run(t.Context(), triage)

			if tt.wantErrContains != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErrContains) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErrContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("Run() unexpected error: %v", err)
			}

			if tt.wantSoftError {
				if !strings.Contains(out, `"error"`) {
					t.Errorf("output %q is not a soft error report", out)
				}
				if calls := fakeLLM.Calls(); len(calls) != 0 {
					t.Errorf("LLM called %d times on prometheus failure, want 0", len(calls))
				}
				return
			}

			if tt.wantOutput != "" && out != tt.wantOutput {
				t.Errorf("Run() = %q, want %q", out, tt.wantOutput)
			}
			for _, want := range tt.wantOutputContains {
				if !strings.Contains(out, want) {
					t.Errorf("Run() = %q, missing %q", out, want)
				}
			}
			if len(gotQueries) != len(metricQueries) {
				t.Errorf("prometheus received %d queries, want %d", len(gotQueries), len(metricQueries))
			}
			for _, q := range gotQueries {
				if !strings.Contains(q, fmt.Sprintf("namespace=%q", testNamespace)) {
					t.Errorf("query %q not scoped to namespace %q", q, testNamespace)
				}
			}

			calls := fakeLLM.Calls()
			if len(calls) != 1 {
				t.Fatalf("LLM called %d times, want 1", len(calls))
			}
			if !strings.Contains(calls[0].SystemPrompt, "metrics") {
				t.Errorf("system prompt missing role: %q", calls[0].SystemPrompt)
			}
			for _, want := range tt.promptContains {
				if !strings.Contains(calls[0].UserPrompt, want) {
					t.Errorf("user prompt missing %q:\n%s", want, calls[0].UserPrompt)
				}
			}
		})
	}
}

func TestMetricsCorrelatorAgentSpecURLWins(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		_, _ = fmt.Fprint(w, matrixBody())
	}))
	defer srv.Close()

	fakeLLM := &llm.FakeLLMClient{Response: validMetricsReport}
	agent := NewMetricsCorrelatorAgent("http://constructor-fallback.invalid:9090", fakeLLM)

	triage := &incidentsv1alpha1.IncidentTriage{
		Spec: incidentsv1alpha1.IncidentTriageSpec{
			AlertName:         testMetricsAlert,
			Severity:          incidentsv1alpha1.SeverityWarning,
			AffectedNamespace: testNamespace,
			PrometheusURL:     srv.URL,
		},
	}

	if _, err := agent.Run(t.Context(), triage); err != nil {
		t.Fatalf("Run() unexpected error: %v", err)
	}
	if hits != len(metricQueries) {
		t.Errorf("spec URL received %d queries, want %d", hits, len(metricQueries))
	}
}

func TestMetricsCorrelatorAgentLLMFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, matrixBody("checkout-1"))
	}))
	defer srv.Close()

	fakeLLM := &llm.FakeLLMClient{Err: errors.New("ollama unreachable")}
	agent := NewMetricsCorrelatorAgent(srv.URL, fakeLLM)

	triage := &incidentsv1alpha1.IncidentTriage{
		Spec: incidentsv1alpha1.IncidentTriageSpec{
			AlertName:         testMetricsAlert,
			Severity:          incidentsv1alpha1.SeverityWarning,
			AffectedNamespace: testNamespace,
		},
	}

	if _, err := agent.Run(t.Context(), triage); err == nil || !strings.Contains(err.Error(), "ollama unreachable") {
		t.Fatalf("err = %v, want containing %q", err, "ollama unreachable")
	}
}
