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
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	incidentsv1alpha1 "github.com/yihanhong/hivemind/api/v1alpha1"
)

const (
	runbookLookupName = "runbooklookup"

	// defaultRunbookConfigMap is used when the triage spec does not name one.
	defaultRunbookConfigMap = "hivemind-runbooks"

	// maxRunbookMatches bounds how many runbooks make the report.
	maxRunbookMatches = 2

	// excerptChars is how much of each matched runbook is quoted.
	excerptChars = 400

	// minKeywordLen drops stopword-sized tokens ("a", "of", "is") that
	// would match every runbook.
	minKeywordLen = 3
)

// RunbookMatch is one scored runbook in the agent's report.
type RunbookMatch struct {
	Name    string `json:"name"`
	Excerpt string `json:"excerpt"`
	Score   int    `json:"score"`
}

// runbookReport is the agent's JSON output shape.
type runbookReport struct {
	Matches []RunbookMatch `json:"matches"`
}

// RunbookLookupAgent matches the alert name against a ConfigMap of
// runbooks by keyword overlap. Deliberately LLM-free: it is the one
// agent that keeps working when every external dependency is down.
type RunbookLookupAgent struct {
	reader client.Reader
	// namespace is the operator's own namespace, where the runbook
	// ConfigMap lives (not the incident's namespace).
	namespace string
}

var _ Agent = (*RunbookLookupAgent)(nil)

func NewRunbookLookupAgent(reader client.Reader, operatorNamespace string) *RunbookLookupAgent {
	return &RunbookLookupAgent{reader: reader, namespace: operatorNamespace}
}

func (a *RunbookLookupAgent) Name() string { return runbookLookupName }

func (a *RunbookLookupAgent) Run(ctx context.Context, triage *incidentsv1alpha1.IncidentTriage) (string, error) {
	name := triage.Spec.RunbookConfigMap
	if name == "" {
		name = defaultRunbookConfigMap
	}

	var cm corev1.ConfigMap
	err := a.reader.Get(ctx, types.NamespacedName{Namespace: a.namespace, Name: name}, &cm)
	switch {
	case apierrors.IsNotFound(err):
		// No runbooks is a valid answer, not a failure.
		return marshalReport(nil)
	case err != nil:
		return "", fmt.Errorf("reading runbook configmap %s/%s: %w", a.namespace, name, err)
	}

	keywords := alertKeywords(triage.Spec.AlertName)
	matches := make([]RunbookMatch, 0, len(cm.Data))
	for rbName, content := range cm.Data {
		matches = append(matches, RunbookMatch{
			Name:    rbName,
			Excerpt: excerpt(content),
			Score:   keywordScore(keywords, content),
		})
	}

	// Highest score first; name breaks ties so output is deterministic.
	slices.SortFunc(matches, func(a, b RunbookMatch) int {
		if c := cmp.Compare(b.Score, a.Score); c != 0 {
			return c
		}
		return cmp.Compare(a.Name, b.Name)
	})
	if len(matches) > maxRunbookMatches {
		matches = matches[:maxRunbookMatches]
	}
	return marshalReport(matches)
}

func marshalReport(matches []RunbookMatch) (string, error) {
	if matches == nil {
		matches = []RunbookMatch{} // encode as [] rather than null
	}
	out, err := json.Marshal(runbookReport{Matches: matches})
	if err != nil {
		return "", fmt.Errorf("marshaling runbook report: %w", err)
	}
	return string(out), nil
}

// alertKeywords tokenizes an alert name into lowercase words, splitting
// on non-letters and on camelCase boundaries: "KubePodCrashLooping"
// becomes [kube, pod, crash, looping]. Alert names are almost always
// camelCase, so splitting only on non-letters would yield one giant
// token that matches nothing.
func alertKeywords(alertName string) []string {
	var words []string
	var b strings.Builder
	flush := func() {
		if b.Len() >= minKeywordLen {
			words = append(words, strings.ToLower(b.String()))
		}
		b.Reset()
	}

	runes := []rune(alertName)
	for i, r := range runes {
		if !unicode.IsLetter(r) {
			flush()
			continue
		}
		if unicode.IsUpper(r) && i > 0 && unicode.IsLower(runes[i-1]) {
			flush()
		}
		b.WriteRune(r)
	}
	flush()
	return words
}

// keywordScore counts how many keywords appear in the runbook content.
func keywordScore(keywords []string, content string) int {
	lower := strings.ToLower(content)
	score := 0
	for _, w := range keywords {
		if strings.Contains(lower, w) {
			score++
		}
	}
	return score
}

func excerpt(content string) string {
	if len(content) <= excerptChars {
		return content
	}
	return content[:excerptChars]
}
