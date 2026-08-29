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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	incidentsv1alpha1 "github.com/yih0nk/hivemind/api/v1alpha1"
)

// defaultHTTPTimeout bounds a single brain call. The reflection loop can
// take several LLM round-trips, so this is deliberately generous relative
// to a single agent's timeout.
const defaultHTTPTimeout = 60 * time.Second

// HTTPReasoner delegates synthesis to the external LangGraph brain. The
// operator gathers evidence and holds cluster credentials; the brain runs
// the reflection loop over that evidence and returns a root-cause report.
type HTTPReasoner struct {
	// BaseURL is the brain's root, e.g. http://hivemind-brain:8090.
	BaseURL string
	// HTTPClient carries the per-call timeout; nil uses a default.
	HTTPClient *http.Client
}

var _ Reasoner = (*HTTPReasoner)(nil)

// NewHTTPReasoner builds a reasoner pointed at the brain at baseURL. A
// timeout <= 0 uses defaultHTTPTimeout.
func NewHTTPReasoner(baseURL string, timeout time.Duration) *HTTPReasoner {
	if timeout <= 0 {
		timeout = defaultHTTPTimeout
	}
	return &HTTPReasoner{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		HTTPClient: &http.Client{Timeout: timeout},
	}
}

func (h *HTTPReasoner) Name() string { return SynthesizerKey }

// --- brain wire contract (mirrors brain/hivemind_brain/models.py) ---

type brainRunbook struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

type brainRequest struct {
	Alert           string         `json:"alert"`
	Namespace       string         `json:"namespace"`
	Pod             string         `json:"pod"`
	Logs            string         `json:"logs"`
	Metrics         string         `json:"metrics"`
	Runbooks        []brainRunbook `json:"runbooks"`
	RequireApproval bool           `json:"require_approval"`
}

type brainResumeRequest struct {
	ThreadID string `json:"thread_id"`
	Action   string `json:"action"`
	Note     string `json:"note"`
}

type brainApprovalRequest struct {
	RootCause   string  `json:"root_cause"`
	ProposedFix string  `json:"proposed_fix"`
	Confidence  float64 `json:"confidence"`
	Iterations  int     `json:"iterations"`
}

type brainResponse struct {
	Status          string                `json:"status"`
	ThreadID        string                `json:"thread_id"`
	ApprovalRequest *brainApprovalRequest `json:"approval_request"`
	RootCause       string                `json:"root_cause"`
	ProposedFix     string                `json:"proposed_fix"`
	Confidence      float64               `json:"confidence"`
	Iterations      int                   `json:"iterations"`
	Critique        string                `json:"critique"`
}

// synthesisReport is the JSON shape stored in status.agentOutputs and
// rendered into the PR. It is the synthesizer's schema plus the brain's
// reflection metadata (iterations/reflection), which surfaces the loop's
// work in the report.
type synthesisReport struct {
	RootCause       string  `json:"rootCause"`
	RecommendedFix  string  `json:"recommendedFix"`
	Confidence      float64 `json:"confidence"`
	Severity        string  `json:"severity"`
	EstimatedImpact string  `json:"estimatedImpact"`
	Iterations      int     `json:"iterations"`
	Reflection      string  `json:"reflection,omitempty"`
}

// Synthesize POSTs the gathered evidence to the brain's /triage endpoint and
// maps the result into a Result. When triage.Spec.RequireApproval is set and the
// brain pauses, the Result is StatusAwaitingApproval with a thread id to resume
// with. A non-2xx response or transport error is returned as an error; the
// controller tolerates a failed synthesis by publishing a partial report, so a
// brain outage degrades gracefully rather than sinking triage.
func (h *HTTPReasoner) Synthesize(ctx context.Context, triage *incidentsv1alpha1.IncidentTriage) (Result, error) {
	body, err := h.postJSON(ctx, "/triage", h.buildRequest(triage))
	if err != nil {
		return Result{}, err
	}
	return decodeResult(body, triage.Spec.Severity)
}

// Resume continues a run paused at the approval gate by POSTing the human
// decision to the brain's /resume endpoint.
func (h *HTTPReasoner) Resume(ctx context.Context, threadID string, approve bool, note string) (Result, error) {
	action := "reject"
	if approve {
		action = "approve"
	}
	body, err := h.postJSON(ctx, "/resume",
		brainResumeRequest{ThreadID: threadID, Action: action, Note: note})
	if err != nil {
		return Result{}, err
	}
	// Severity is not echoed on resume; the PR header still carries it from spec.
	return decodeResult(body, "")
}

// postJSON marshals payload, POSTs it to path under BaseURL, and returns the
// response body, erroring on transport failure or a non-2xx status.
func (h *HTTPReasoner) postJSON(ctx context.Context, path string, payload any) ([]byte, error) {
	reqBody, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshaling brain request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(
		ctx, http.MethodPost, h.BaseURL+path, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("building brain request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	client := h.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: defaultHTTPTimeout}
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("calling brain: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("reading brain response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("brain returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

// decodeResult maps a brain response into a Result. An awaiting-approval
// response yields the thread id and a human-readable proposal; anything else is
// treated as completed and mapped into the synthesizer report schema (an empty
// status, e.g. from an older brain, counts as completed).
func decodeResult(body []byte, severity incidentsv1alpha1.Severity) (Result, error) {
	var br brainResponse
	if err := json.Unmarshal(body, &br); err != nil {
		return Result{}, fmt.Errorf("decoding brain response: %w", err)
	}

	if br.Status == StatusAwaitingApproval {
		proposal := ""
		if ar := br.ApprovalRequest; ar != nil {
			proposal = fmt.Sprintf(
				"Root cause: %s\nProposed fix: %s\n(confidence %.2f, %d iterations)",
				ar.RootCause, ar.ProposedFix, ar.Confidence, ar.Iterations)
		}
		return Result{
			Status:   StatusAwaitingApproval,
			ThreadID: br.ThreadID,
			Proposal: proposal,
		}, nil
	}

	report, err := json.Marshal(synthesisReport{
		RootCause:       br.RootCause,
		RecommendedFix:  br.ProposedFix,
		Confidence:      br.Confidence,
		Severity:        string(severity),
		EstimatedImpact: "",
		Iterations:      br.Iterations,
		Reflection:      br.Critique,
	})
	if err != nil {
		return Result{}, fmt.Errorf("marshaling synthesis report: %w", err)
	}
	return Result{Status: StatusCompleted, Report: string(report)}, nil
}

// buildRequest maps the CR spec and the upstream agents' persisted outputs
// into the brain's evidence payload. The evidence-gathering agents already
// ran (their outputs are in status), so this reuses their work rather than
// re-fetching from the cluster.
func (h *HTTPReasoner) buildRequest(triage *incidentsv1alpha1.IncidentTriage) brainRequest {
	outputs := triage.Status.AgentOutputs
	req := brainRequest{
		Alert:           triage.Spec.AlertName,
		Namespace:       triage.Spec.AffectedNamespace,
		Logs:            outputs["logtriage"],
		Metrics:         outputs["metricscorrelator"],
		RequireApproval: triage.Spec.RequireApproval,
	}
	if rb := outputs["runbooklookup"]; rb != "" {
		req.Runbooks = []brainRunbook{{Name: "runbook-lookup", Content: rb}}
	}
	return req
}
