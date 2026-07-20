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
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	incidentsv1alpha1 "github.com/yih0nk/hivemind/api/v1alpha1"
	"github.com/yih0nk/hivemind/internal/llm"
)

const synthesizerName = "synthesizer"

// upstreamAgents are the evidence-gathering agents whose reports the
// synthesizer combines. Their outputs come from status.agentOutputs,
// written by an earlier reconcile pass — never from the same Dispatch
// the synthesizer runs in.
var upstreamAgents = []string{logTriageName, metricsCorrelatorName, runbookLookupName}

const synthesizerSystemPrompt = `You are an SRE synthesizing an incident report.
Given the outputs of three triage agents, return JSON only:
{rootCause: string, recommendedFix: string, confidence: float, severity: string, estimatedImpact: string}
recommendedFix must be a single string, not a list: at most 3 concrete steps numbered
inside the string ("1. ... 2. ..."), each actionable against this specific incident
(name the deployment, flag, or limit to change). No generic advice such as
"check the logs", "monitor the situation", or "investigate further".`

// SynthesisReport is the JSON shape the LLM is asked to produce; Run
// only uses it to validate that the response parses.
type SynthesisReport struct {
	RootCause string `json:"rootCause"`
	// RecommendedFix is asked for as a string, but local models often
	// return a structured list of steps instead. The report embeds the
	// raw JSON verbatim, so any shape renders fine — don't reject it.
	RecommendedFix  json.RawMessage `json:"recommendedFix"`
	Confidence      float64         `json:"confidence"`
	Severity        string          `json:"severity"`
	EstimatedImpact string          `json:"estimatedImpact"`
}

// SynthesizerAgent distills the upstream agents' reports into one
// root-cause summary. It reads only status.agentOutputs, so it must run
// after those outputs have been persisted.
type SynthesizerAgent struct {
	llm llm.LLMClient
}

var _ Agent = (*SynthesizerAgent)(nil)

func NewSynthesizerAgent(llmClient llm.LLMClient) *SynthesizerAgent {
	return &SynthesizerAgent{llm: llmClient}
}

func (a *SynthesizerAgent) Name() string { return synthesizerName }

func (a *SynthesizerAgent) Run(ctx context.Context, triage *incidentsv1alpha1.IncidentTriage) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "Incident: alert %q, severity %s, namespace %s\n\n",
		triage.Spec.AlertName, triage.Spec.Severity, triage.Spec.AffectedNamespace)

	found := 0
	for _, name := range upstreamAgents {
		out, ok := triage.Status.AgentOutputs[name]
		if !ok || out == "" {
			continue
		}
		found++
		fmt.Fprintf(&b, "### %s\n%s\n\n", name, out)
	}
	if found == 0 {
		return "", errors.New("upstream agent outputs not yet available")
	}

	raw, err := a.llm.Complete(ctx, synthesizerSystemPrompt, b.String())
	if err != nil {
		return "", fmt.Errorf("completing synthesis: %w", err)
	}

	return decodeReport[SynthesisReport](raw, synthesizerName)
}
