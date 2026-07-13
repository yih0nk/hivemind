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

package webhook

import (
	"bytes"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	incidentsv1alpha1 "github.com/yihanhong/hivemind/api/v1alpha1"
)

const (
	testAlertname = "PodCrashLooping"
	testNamespace = "payments"
)

func newTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := incidentsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("adding incidents scheme: %v", err)
	}
	return scheme
}

func postMessage(t *testing.T, h *AlertmanagerHandler, msg Message) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshaling payload: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(raw)))
	return rec
}

func firingAlert(labels map[string]string) Alert {
	return Alert{
		Status:      "firing",
		Labels:      labels,
		StartsAt:    time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC),
		Fingerprint: "c4d5a0d2e1f3b6a7",
	}
}

func TestAlertmanagerHandler(t *testing.T) {
	tests := []struct {
		name    string
		alerts  []Alert
		posts   int // defaults to 1
		wantCRs int
		check   func(t *testing.T, cr incidentsv1alpha1.IncidentTriage)
	}{
		{
			name: "firing alert with all fields",
			alerts: []Alert{firingAlert(map[string]string{
				labelAlertname: testAlertname,
				"severity":     "critical",
				labelNamespace: testNamespace,
				"pod_app":      "checkout",
				"pod_tier":     "backend",
			})},
			wantCRs: 1,
			check: func(t *testing.T, cr incidentsv1alpha1.IncidentTriage) {
				if !strings.HasPrefix(cr.Name, "podcrashlooping-") {
					t.Errorf("name %q missing sanitized alertname prefix", cr.Name)
				}
				if cr.Namespace != testNamespace {
					t.Errorf("namespace = %q, want payments", cr.Namespace)
				}
				if cr.Spec.AlertName != testAlertname {
					t.Errorf("alertName = %q, want PodCrashLooping", cr.Spec.AlertName)
				}
				if cr.Spec.Severity != incidentsv1alpha1.SeverityCritical {
					t.Errorf("severity = %q, want critical", cr.Spec.Severity)
				}
				if cr.Spec.AffectedNamespace != testNamespace {
					t.Errorf("affectedNamespace = %q, want payments", cr.Spec.AffectedNamespace)
				}
				wantSelector := map[string]string{"app": "checkout", "tier": "backend"}
				if !maps.Equal(cr.Spec.AffectedPodSelector, wantSelector) {
					t.Errorf("affectedPodSelector = %v, want %v", cr.Spec.AffectedPodSelector, wantSelector)
				}
				if cr.Spec.GithubRepo != "yihan/incident-reports" {
					t.Errorf("githubRepo = %q, want yihan/incident-reports", cr.Spec.GithubRepo)
				}
				if cr.Spec.PrometheusURL != "http://prom.example:9090" {
					t.Errorf("prometheusURL = %q, want http://prom.example:9090", cr.Spec.PrometheusURL)
				}
			},
		},
		{
			name:    "missing severity and namespace get defaults",
			alerts:  []Alert{firingAlert(map[string]string{labelAlertname: "HighLatency"})},
			wantCRs: 1,
			check: func(t *testing.T, cr incidentsv1alpha1.IncidentTriage) {
				if cr.Namespace != "default" {
					t.Errorf("namespace = %q, want default", cr.Namespace)
				}
				if cr.Spec.Severity != incidentsv1alpha1.SeverityWarning {
					t.Errorf("severity = %q, want warning", cr.Spec.Severity)
				}
				if len(cr.Spec.AffectedPodSelector) != 0 {
					t.Errorf("affectedPodSelector = %v, want empty", cr.Spec.AffectedPodSelector)
				}
			},
		},
		{
			name: "resolved alert creates nothing",
			alerts: []Alert{{
				Status: "resolved",
				Labels: map[string]string{labelAlertname: testAlertname},
			}},
			wantCRs: 0,
		},
		{
			name: "duplicate firing is idempotent",
			alerts: []Alert{firingAlert(map[string]string{
				labelAlertname: testAlertname,
				labelNamespace: testNamespace,
			})},
			posts:   2,
			wantCRs: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(newTestScheme(t)).Build()
			h := &AlertmanagerHandler{
				Client:        c,
				GithubRepo:    "yihan/incident-reports",
				PrometheusURL: "http://prom.example:9090",
			}

			for range max(tt.posts, 1) {
				if rec := postMessage(t, h, Message{Version: "4", Status: "firing", Alerts: tt.alerts}); rec.Code != http.StatusOK {
					t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
				}
			}

			var list incidentsv1alpha1.IncidentTriageList
			if err := c.List(t.Context(), &list); err != nil {
				t.Fatalf("listing IncidentTriages: %v", err)
			}
			if len(list.Items) != tt.wantCRs {
				t.Fatalf("got %d IncidentTriages, want %d", len(list.Items), tt.wantCRs)
			}
			if tt.check != nil && len(list.Items) > 0 {
				tt.check(t, list.Items[0])
			}
		})
	}
}

func TestAlertmanagerHandlerMalformedPayload(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newTestScheme(t)).Build()
	h := &AlertmanagerHandler{Client: c}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader("not json")))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (alertmanager retries non-2xx)", rec.Code, http.StatusOK)
	}
}
