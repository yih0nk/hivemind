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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	incidentsv1alpha1 "github.com/yih0nk/hivemind/api/v1alpha1"
)

// baseBranch is where incident report branches fork from and PRs merge into.
const baseBranch = "main"

// reportSections maps status.agentOutputs keys to their heading in the
// PR body, in render order. The keys are the agents' Name() values --
// part of the CR's observable contract, so spelled out here rather than
// imported from the agents package.
var reportSections = []struct {
	heading string
	key     string
}{
	{"Log Triage", "logtriage"},
	{"Metrics Analysis", "metricscorrelator"},
	{"Runbook Match", "runbooklookup"},
	{"Root Cause & Recommended Fix", "synthesizer"},
}

// OpenIncidentPR publishes a triage run's full report as a pull request
// and returns its URL. Sections whose agent output is missing render as
// "_no output_" -- a partial report published beats a complete report
// that never lands. CreateBranch tolerates the branch already existing,
// so a retried reconcile reuses it instead of failing.
func OpenIncidentPR(ctx context.Context, c PRClient, repo string, triage *incidentsv1alpha1.IncidentTriage) (string, error) {
	// triage.Name is DNS-1123 (lowercase alphanumerics and dashes), so it
	// needs no sanitizing for a git ref. The creation timestamp keeps
	// branches unique across distinct firings of the same alert.
	branch := fmt.Sprintf("hivemind/incident-%s-%d", triage.Name, triage.CreationTimestamp.Unix())
	title := fmt.Sprintf("[Hivemind] %s (%s)", triage.Spec.AlertName, triage.Spec.Severity)

	if err := c.CreateBranch(ctx, repo, baseBranch, branch); err != nil {
		return "", fmt.Errorf("creating incident branch: %w", err)
	}
	url, err := c.CreatePR(ctx, repo, title, reportBody(triage), branch, baseBranch)
	if err != nil {
		return "", fmt.Errorf("opening incident PR: %w", err)
	}
	return url, nil
}

// reportBody renders the markdown incident report.
func reportBody(triage *incidentsv1alpha1.IncidentTriage) string {
	started := "unknown"
	if t := triage.Status.StartTime; t != nil {
		started = t.UTC().Format("2006-01-02 15:04:05 UTC")
	}

	var b strings.Builder
	fmt.Fprintf(&b, "## Incident Summary\n")
	fmt.Fprintf(&b, "Alert: %s | Severity: %s | Namespace: %s\n\n",
		triage.Spec.AlertName, triage.Spec.Severity, triage.Spec.AffectedNamespace)
	fmt.Fprintf(&b, "Started: %s\n", started)

	for _, s := range reportSections {
		fmt.Fprintf(&b, "\n## %s\n%s\n", s.heading, renderOutput(triage.Status.AgentOutputs[s.key]))
	}
	return b.String()
}

// renderOutput pretty-prints an agent's JSON report inside a code fence;
// non-JSON output is included verbatim and missing output is marked.
func renderOutput(out string) string {
	if out == "" {
		return "_no output_"
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(out), "", "  "); err != nil {
		return out
	}
	return "```json\n" + buf.String() + "\n```"
}
