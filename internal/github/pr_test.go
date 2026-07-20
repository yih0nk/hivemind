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

package github

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	incidentsv1alpha1 "github.com/yih0nk/hivemind/api/v1alpha1"
)

const testRepo = "yihan/incident-reports"

func incidentFixture(outputs map[string]string) *incidentsv1alpha1.IncidentTriage {
	created := metav1.NewTime(time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC))
	started := metav1.NewTime(time.Date(2026, 7, 13, 10, 0, 5, 0, time.UTC))
	return &incidentsv1alpha1.IncidentTriage{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "podcrashlooping-ab12cd34",
			Namespace:         "payments",
			CreationTimestamp: created,
		},
		Spec: incidentsv1alpha1.IncidentTriageSpec{
			AlertName:         "PodCrashLooping",
			Severity:          incidentsv1alpha1.SeverityCritical,
			AffectedNamespace: "payments",
			GithubRepo:        testRepo,
		},
		Status: incidentsv1alpha1.IncidentTriageStatus{
			StartTime:    &started,
			AgentOutputs: outputs,
		},
	}
}

func fullOutputs() map[string]string {
	return map[string]string{
		"logtriage":         `{"likelyCause":"OOM"}`,
		"metricscorrelator": `{"memoryTrend":"rising"}`,
		"runbooklookup":     `{"matches":[]}`,
		"synthesizer":       `{"rootCause":"memory leak"}`,
	}
}

func TestOpenIncidentPR(t *testing.T) {
	fake := &FakePRClient{PRURL: "https://github.com/yihan/incident-reports/pull/7"}
	triage := incidentFixture(fullOutputs())

	url, err := OpenIncidentPR(t.Context(), fake, testRepo, triage)
	if err != nil {
		t.Fatalf("OpenIncidentPR() unexpected error: %v", err)
	}
	if url != fake.PRURL {
		t.Errorf("url = %q, want %q", url, fake.PRURL)
	}

	branches := fake.Branches()
	if len(branches) != 1 {
		t.Fatalf("got %d CreateBranch calls, want 1", len(branches))
	}
	wantBranch := fmt.Sprintf("hivemind/incident-%s-%d", triage.Name, triage.CreationTimestamp.Unix())
	if want := testRepo + " main " + wantBranch; branches[0] != want {
		t.Errorf("branch call = %q, want %q", branches[0], want)
	}

	prs := fake.PRs()
	if len(prs) != 1 {
		t.Fatalf("got %d CreatePR calls, want 1", len(prs))
	}
	pr := prs[0]
	if pr.Title != "[Hivemind] PodCrashLooping (critical)" {
		t.Errorf("title = %q", pr.Title)
	}
	if pr.Head != wantBranch || pr.Base != "main" {
		t.Errorf("head->base = %s->%s, want %s->main", pr.Head, pr.Base, wantBranch)
	}
	for _, want := range []string{
		"## Incident Summary",
		"Alert: PodCrashLooping | Severity: critical | Namespace: payments",
		"Started: 2026-07-13 10:00:05 UTC",
		"## Log Triage",
		`"likelyCause": "OOM"`, // pretty-printed
		"## Metrics Analysis",
		"## Runbook Match",
		"## Root Cause & Recommended Fix",
		`"rootCause": "memory leak"`,
	} {
		if !strings.Contains(pr.Body, want) {
			t.Errorf("body missing %q:\n%s", want, pr.Body)
		}
	}
}

func TestOpenIncidentPRPartialOutputs(t *testing.T) {
	fake := &FakePRClient{PRURL: "https://github.com/yihan/incident-reports/pull/8"}
	outputs := fullOutputs()
	delete(outputs, "synthesizer")
	triage := incidentFixture(outputs)

	url, err := OpenIncidentPR(t.Context(), fake, testRepo, triage)
	if err != nil {
		t.Fatalf("OpenIncidentPR() unexpected error: %v", err)
	}
	if url == "" {
		t.Error("url is empty")
	}

	prs := fake.PRs()
	if len(prs) != 1 {
		t.Fatalf("got %d CreatePR calls, want 1", len(prs))
	}
	body := prs[0].Body
	if !strings.Contains(body, "## Root Cause & Recommended Fix\n_no output_") {
		t.Errorf("missing synthesizer section not marked _no output_:\n%s", body)
	}
	if !strings.Contains(body, `"likelyCause": "OOM"`) {
		t.Errorf("present sections should still render:\n%s", body)
	}
}

func TestOpenIncidentPRBranchFailure(t *testing.T) {
	fake := &FakePRClient{BranchErr: errors.New("ref locked")}
	triage := incidentFixture(fullOutputs())

	if _, err := OpenIncidentPR(t.Context(), fake, testRepo, triage); err == nil || !strings.Contains(err.Error(), "ref locked") {
		t.Fatalf("err = %v, want containing %q", err, "ref locked")
	}
	if prs := fake.PRs(); len(prs) != 0 {
		t.Errorf("CreatePR called %d times after branch failure, want 0", len(prs))
	}
}

func TestOpenIncidentPRCreateFailure(t *testing.T) {
	fake := &FakePRClient{Err: errors.New("rate limited")}
	triage := incidentFixture(fullOutputs())

	if _, err := OpenIncidentPR(t.Context(), fake, testRepo, triage); err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("err = %v, want containing %q", err, "rate limited")
	}
}
