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
	Alert     string         `json:"alert"`
	Namespace string         `json:"namespace"`
	Pod       string         `json:"pod"`
	Logs      string         `json:"logs"`
	Metrics   string         `json:"metrics"`
	Runbooks  []brainRunbook `json:"runbooks"`
}

type brainResponse struct {
	RootCause   string  `json:"root_cause"`
	ProposedFix string  `json:"proposed_fix"`
	Confidence  float64 `json:"confidence"`
	Iterations  int     `json:"iterations"`
	Critique    string  `json:"critique"`
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

// Synthesize POSTs the gathered evidence to the brain's /triage endpoint
// and maps the reflection result into the synthesizer report schema. A
// non-2xx response or transport error is returned as an error; the
// controller already tolerates a failed synthesis by publishing a partial
// report, so a brain outage degrades gracefully rather than sinking triage.
func (h *HTTPReasoner) Synthesize(ctx context.Context, triage *incidentsv1alpha1.IncidentTriage) (string, error) {
	reqBody, err := json.Marshal(h.buildRequest(triage))
	if err != nil {
		return "", fmt.Errorf("marshaling brain request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(
		ctx, http.MethodPost, h.BaseURL+"/triage", bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("building brain request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	client := h.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: defaultHTTPTimeout}
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("calling brain: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("reading brain response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("brain returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var br brainResponse
	if err := json.Unmarshal(body, &br); err != nil {
		return "", fmt.Errorf("decoding brain response: %w", err)
	}

	report, err := json.Marshal(synthesisReport{
		RootCause:       br.RootCause,
		RecommendedFix:  br.ProposedFix,
		Confidence:      br.Confidence,
		Severity:        string(triage.Spec.Severity),
		EstimatedImpact: "",
		Iterations:      br.Iterations,
		Reflection:      br.Critique,
	})
	if err != nil {
		return "", fmt.Errorf("marshaling synthesis report: %w", err)
	}
	return string(report), nil
}

// buildRequest maps the CR spec and the upstream agents' persisted outputs
// into the brain's evidence payload. The evidence-gathering agents already
// ran (their outputs are in status), so this reuses their work rather than
// re-fetching from the cluster.
func (h *HTTPReasoner) buildRequest(triage *incidentsv1alpha1.IncidentTriage) brainRequest {
	outputs := triage.Status.AgentOutputs
	req := brainRequest{
		Alert:     triage.Spec.AlertName,
		Namespace: triage.Spec.AffectedNamespace,
		Logs:      outputs["logtriage"],
		Metrics:   outputs["metricscorrelator"],
	}
	if rb := outputs["runbooklookup"]; rb != "" {
		req.Runbooks = []brainRunbook{{Name: "runbook-lookup", Content: rb}}
	}
	return req
}
