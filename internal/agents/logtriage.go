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
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	incidentsv1alpha1 "github.com/yihanhong/hivemind/api/v1alpha1"
	"github.com/yihanhong/hivemind/internal/llm"
)

const (
	logTriageName = "logtriage"

	// maxPods bounds how many pods contribute logs to one report.
	maxPods = 3

	// tailLines and sinceSeconds both bound each pod's log fetch; the
	// API server applies whichever cuts off first.
	tailLines    = 200
	sinceSeconds = 3600

	// maxContextChars caps the user prompt so small local models are not
	// fed more context than they can attend to.
	maxContextChars = 8000

	// noPodsMessage is sent to the LLM in place of logs when nothing
	// matched, so the report says "nothing to look at" instead of failing.
	noPodsMessage = "no pods matched selector in namespace"
)

const logTriageSystemPrompt = `You are an SRE triaging a Kubernetes production incident.
Analyze the following pod logs and return JSON only, no prose.
Identify the LAST error before each crash or restart -- that is the primary signal.
Do not simply list every error line; earlier errors are context, not the cause.
JSON schema: {errorLines: string[], likelyCause: string, confidence: float (0-1), recommendedAction: string}`

// LogTriageReport is the JSON shape the LLM is asked to produce. Run
// uses it only to validate that the response parses; the raw JSON string
// is what lands in status.agentOutputs.
type LogTriageReport struct {
	ErrorLines        []string `json:"errorLines"`
	LikelyCause       string   `json:"likelyCause"`
	Confidence        float64  `json:"confidence"`
	RecommendedAction string   `json:"recommendedAction"`
}

// LogTriageAgent fetches recent logs from the affected pods and asks the
// LLM for a structured root-cause hypothesis.
type LogTriageAgent struct {
	// reader lists the affected pods. A plain Reader (not client.Client)
	// so main can inject the manager's uncached API reader: one list per
	// incident does not justify a cluster-wide pod informer.
	reader client.Reader
	logs   PodLogReader
	llm    llm.LLMClient
}

var _ Agent = (*LogTriageAgent)(nil)

func NewLogTriageAgent(reader client.Reader, logs PodLogReader, llmClient llm.LLMClient) *LogTriageAgent {
	return &LogTriageAgent{reader: reader, logs: logs, llm: llmClient}
}

func (a *LogTriageAgent) Name() string { return logTriageName }

func (a *LogTriageAgent) Run(ctx context.Context, triage *incidentsv1alpha1.IncidentTriage) (string, error) {
	logsContext, err := a.gatherLogs(ctx, triage)
	if err != nil {
		return "", err
	}

	raw, err := a.llm.Complete(ctx, logTriageSystemPrompt, logsContext)
	if err != nil {
		return "", fmt.Errorf("completing log triage: %w", err)
	}

	return decodeReport[LogTriageReport](raw, logTriageName)
}

// gatherLogs lists the affected pods and concatenates their recent logs
// into one prompt-sized context block.
func (a *LogTriageAgent) gatherLogs(ctx context.Context, triage *incidentsv1alpha1.IncidentTriage) (string, error) {
	var pods corev1.PodList
	if err := a.reader.List(ctx, &pods,
		client.InNamespace(triage.Spec.AffectedNamespace),
		client.MatchingLabels(triage.Spec.AffectedPodSelector),
	); err != nil {
		return "", fmt.Errorf("listing pods in %s: %w", triage.Spec.AffectedNamespace, err)
	}

	var b strings.Builder
	for i, pod := range pods.Items {
		if i >= maxPods {
			break
		}
		text, err := a.logs.PodLogs(ctx, pod.Namespace, pod.Name, &corev1.PodLogOptions{
			TailLines:    ptr.To(int64(tailLines)),
			SinceSeconds: ptr.To(int64(sinceSeconds)),
		})
		if err != nil {
			// One unreadable pod (container still creating, just evicted)
			// must not sink the report for the pods that did have logs.
			text = fmt.Sprintf("<logs unavailable: %v>", err)
		}
		fmt.Fprintf(&b, "=== pod %s ===\n%s\n", pod.Name, text)
	}

	if b.Len() == 0 {
		return fmt.Sprintf("%s %q", noPodsMessage, triage.Spec.AffectedNamespace), nil
	}
	if b.Len() > maxContextChars {
		return b.String()[:maxContextChars], nil
	}
	return b.String(), nil
}
