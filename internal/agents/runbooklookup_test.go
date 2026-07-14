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
	"encoding/json"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	incidentsv1alpha1 "github.com/yihanhong/hivemind/api/v1alpha1"
)

const operatorNamespace = "hivemind-system"

func runbookConfigMap(data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      defaultRunbookConfigMap,
			Namespace: operatorNamespace,
		},
		Data: data,
	}
}

func TestRunbookLookupAgentRun(t *testing.T) {
	tests := []struct {
		name      string
		objects   []client.Object
		alertName string

		wantTopName  string
		wantTopScore int
		wantMatches  int
	}{
		{
			name: "alert matches one of three runbooks",
			objects: []client.Object{runbookConfigMap(map[string]string{
				"OOMKill":          "Pod was killed by the OOM killer. Check memory limits and recent deploys.",
				"CrashLoopBackOff": "Container crashes on start, kubelet backs off restarting the crash loop.",
				"HighErrorRate":    "Elevated HTTP 5xx usually means a bad deploy or dependency outage.",
			})},
			alertName:    "KubePodCrashLooping",
			wantTopName:  "CrashLoopBackOff",
			wantTopScore: 2, // "crash" appears, and "kube" matches inside "kubelet"
			wantMatches:  2,
		},
		{
			name:        "missing configmap yields empty matches without error",
			objects:     nil,
			alertName:   "KubePodCrashLooping",
			wantMatches: 0,
		},
		{
			name: "all runbooks scoring zero are still returned",
			objects: []client.Object{runbookConfigMap(map[string]string{
				"DiskPressure": "Node disk is filling up.",
				"DNSFailure":   "CoreDNS resolution problems.",
			})},
			alertName:    "CertificateExpiry",
			wantTopName:  "DNSFailure", // ties broken by name, ascending
			wantTopScore: 0,
			wantMatches:  2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(clientgoscheme.Scheme).WithObjects(tt.objects...).Build()
			agent := NewRunbookLookupAgent(c, operatorNamespace)

			triage := &incidentsv1alpha1.IncidentTriage{
				Spec: incidentsv1alpha1.IncidentTriageSpec{
					AlertName:         tt.alertName,
					Severity:          incidentsv1alpha1.SeverityWarning,
					AffectedNamespace: testNamespace,
				},
			}

			out, err := agent.Run(t.Context(), triage)
			if err != nil {
				t.Fatalf("Run() unexpected error: %v", err)
			}

			var report struct {
				Matches []RunbookMatch `json:"matches"`
			}
			if err := json.Unmarshal([]byte(out), &report); err != nil {
				t.Fatalf("output is not valid JSON: %v\n%s", err, out)
			}
			if report.Matches == nil {
				t.Fatalf("matches encoded as null, want []: %s", out)
			}
			if len(report.Matches) != tt.wantMatches {
				t.Fatalf("got %d matches, want %d: %s", len(report.Matches), tt.wantMatches, out)
			}
			if tt.wantMatches == 0 {
				return
			}

			top := report.Matches[0]
			if top.Name != tt.wantTopName {
				t.Errorf("top match = %q, want %q", top.Name, tt.wantTopName)
			}
			if top.Score != tt.wantTopScore {
				t.Errorf("top score = %d, want %d", top.Score, tt.wantTopScore)
			}
			if top.Excerpt == "" || len(top.Excerpt) > excerptChars {
				t.Errorf("excerpt length %d, want 1..%d", len(top.Excerpt), excerptChars)
			}
		})
	}
}

func TestRunbookLookupAgentCustomConfigMapName(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "team-runbooks", Namespace: operatorNamespace},
		Data:       map[string]string{"OOMKill": "OOM killer struck again."},
	}
	c := fake.NewClientBuilder().WithScheme(clientgoscheme.Scheme).WithObjects(cm).Build()
	agent := NewRunbookLookupAgent(c, operatorNamespace)

	triage := &incidentsv1alpha1.IncidentTriage{
		Spec: incidentsv1alpha1.IncidentTriageSpec{
			AlertName:         "PodOOMKilled",
			Severity:          incidentsv1alpha1.SeverityCritical,
			AffectedNamespace: testNamespace,
			RunbookConfigMap:  "team-runbooks",
		},
	}

	out, err := agent.Run(t.Context(), triage)
	if err != nil {
		t.Fatalf("Run() unexpected error: %v", err)
	}
	if !strings.Contains(out, "OOMKill") {
		t.Errorf("output missing match from custom configmap: %s", out)
	}
}

func TestAlertKeywords(t *testing.T) {
	got := alertKeywords("KubePodCrashLooping")
	want := []string{"kube", "pod", "crash", "looping"}
	if len(got) != len(want) {
		t.Fatalf("alertKeywords() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("keyword[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
