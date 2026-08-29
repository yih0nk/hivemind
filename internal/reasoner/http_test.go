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

package reasoner

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	incidentsv1alpha1 "github.com/yih0nk/hivemind/api/v1alpha1"
)

func testTriage() *incidentsv1alpha1.IncidentTriage {
	return &incidentsv1alpha1.IncidentTriage{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout-oom", Namespace: "default"},
		Spec: incidentsv1alpha1.IncidentTriageSpec{
			AlertName:         "OOMKilled",
			Severity:          incidentsv1alpha1.SeverityCritical,
			AffectedNamespace: "prod",
		},
		Status: incidentsv1alpha1.IncidentTriageStatus{
			AgentOutputs: map[string]string{
				"logtriage":         `{"summary":"OOMKilled x5"}`,
				"metricscorrelator": `{"summary":"memory at limit"}`,
				"runbooklookup":     `{"match":"oom runbook"}`,
			},
		},
	}
}

func TestHTTPReasoner_Synthesize_MapsResponse(t *testing.T) {
	var gotBody brainRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/triage" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Fatalf("request body not valid brainRequest: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"root_cause": "container OOMKilled under load",
			"proposed_fix": "raise memory limit; add HPA",
			"confidence": 0.9,
			"iterations": 2,
			"critique": "well supported by logs and metrics"
		}`))
	}))
	defer srv.Close()

	r := NewHTTPReasoner(srv.URL, 0)
	out, err := r.Synthesize(context.Background(), testTriage())
	if err != nil {
		t.Fatalf("Synthesize returned error: %v", err)
	}
	if out.Status != StatusCompleted {
		t.Errorf("status = %q, want completed", out.Status)
	}

	// The evidence the operator already gathered must reach the brain.
	if !strings.Contains(gotBody.Logs, "OOMKilled") {
		t.Errorf("logs not forwarded to brain: %q", gotBody.Logs)
	}
	if gotBody.Alert != "OOMKilled" || gotBody.Namespace != "prod" {
		t.Errorf("incident metadata not forwarded: %+v", gotBody)
	}
	if len(gotBody.Runbooks) != 1 {
		t.Errorf("runbook evidence not forwarded: %+v", gotBody.Runbooks)
	}
	if gotBody.RequireApproval {
		t.Errorf("require_approval should default false")
	}

	// The response must map into the synthesizer schema the PR renderer expects.
	var report synthesisReport
	if err := json.Unmarshal([]byte(out.Report), &report); err != nil {
		t.Fatalf("output is not a synthesisReport: %v", err)
	}
	if report.RootCause != "container OOMKilled under load" {
		t.Errorf("rootCause = %q", report.RootCause)
	}
	if report.RecommendedFix != "raise memory limit; add HPA" {
		t.Errorf("recommendedFix = %q", report.RecommendedFix)
	}
	if report.Confidence != 0.9 || report.Iterations != 2 {
		t.Errorf("confidence/iterations = %v/%d", report.Confidence, report.Iterations)
	}
	// Severity is carried from the CR spec, not the brain.
	if report.Severity != "critical" {
		t.Errorf("severity = %q, want critical", report.Severity)
	}
}

func TestHTTPReasoner_Synthesize_ErrorOnServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "graph exploded", http.StatusInternalServerError)
	}))
	defer srv.Close()

	r := NewHTTPReasoner(srv.URL, 0)
	_, err := r.Synthesize(context.Background(), testTriage())
	if err == nil {
		t.Fatal("expected an error on 500, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should mention status code: %v", err)
	}
}

func TestHTTPReasoner_Name(t *testing.T) {
	if got := NewHTTPReasoner("http://x", 0).Name(); got != SynthesizerKey {
		t.Errorf("Name() = %q, want %q", got, SynthesizerKey)
	}
}

func TestHTTPReasoner_BuildRequest_OmitsEmptyRunbooks(t *testing.T) {
	tr := testTriage()
	delete(tr.Status.AgentOutputs, "runbooklookup")
	req := NewHTTPReasoner("http://x", 0).buildRequest(tr)
	if len(req.Runbooks) != 0 {
		t.Errorf("expected no runbooks when lookup output is absent, got %+v", req.Runbooks)
	}
}

func TestHTTPReasoner_Synthesize_AwaitingApproval(t *testing.T) {
	var gotBody brainRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status": "awaiting_approval",
			"thread_id": "thread-123",
			"approval_request": {
				"root_cause": "OOMKilled under load",
				"proposed_fix": "raise memory limit",
				"confidence": 0.8,
				"iterations": 1
			}
		}`))
	}))
	defer srv.Close()

	tr := testTriage()
	tr.Spec.RequireApproval = true
	out, err := NewHTTPReasoner(srv.URL, 0).Synthesize(context.Background(), tr)
	if err != nil {
		t.Fatalf("Synthesize returned error: %v", err)
	}
	if !gotBody.RequireApproval {
		t.Error("require_approval should be forwarded to the brain")
	}
	if out.Status != StatusAwaitingApproval {
		t.Errorf("status = %q, want awaiting_approval", out.Status)
	}
	if out.ThreadID != "thread-123" {
		t.Errorf("thread_id = %q", out.ThreadID)
	}
	if out.Report != "" {
		t.Errorf("no report should be set while awaiting approval, got %q", out.Report)
	}
	if !strings.Contains(out.Proposal, "OOMKilled under load") {
		t.Errorf("proposal should summarize the root cause, got %q", out.Proposal)
	}
}

func TestHTTPReasoner_Resume_Approve(t *testing.T) {
	var gotBody brainResumeRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/resume" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status": "completed",
			"root_cause": "confirmed OOM",
			"proposed_fix": "raise limit",
			"confidence": 0.9,
			"iterations": 2
		}`))
	}))
	defer srv.Close()

	out, err := NewHTTPReasoner(srv.URL, 0).Resume(
		context.Background(), "thread-123", true, "lgtm")
	if err != nil {
		t.Fatalf("Resume returned error: %v", err)
	}
	if gotBody.ThreadID != "thread-123" || gotBody.Action != "approve" || gotBody.Note != "lgtm" {
		t.Errorf("resume request not forwarded correctly: %+v", gotBody)
	}
	if out.Status != StatusCompleted {
		t.Errorf("status = %q, want completed", out.Status)
	}
	var report synthesisReport
	if err := json.Unmarshal([]byte(out.Report), &report); err != nil {
		t.Fatalf("resume output is not a synthesisReport: %v", err)
	}
	if report.RootCause != "confirmed OOM" {
		t.Errorf("rootCause = %q", report.RootCause)
	}
}

func TestHTTPReasoner_Resume_RejectSendsRejectAction(t *testing.T) {
	var gotBody brainResumeRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status": "completed", "root_cause": "x"}`))
	}))
	defer srv.Close()

	_, err := NewHTTPReasoner(srv.URL, 0).Resume(context.Background(), "t", false, "")
	if err != nil {
		t.Fatalf("Resume returned error: %v", err)
	}
	if gotBody.Action != "reject" {
		t.Errorf("action = %q, want reject", gotBody.Action)
	}
}
