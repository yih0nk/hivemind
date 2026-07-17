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
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	incidentsv1alpha1 "github.com/yihanhong/hivemind/api/v1alpha1"
	"github.com/yihanhong/hivemind/internal/llm"
)

const (
	metricsCorrelatorName = "metricscorrelator"

	// metricsWindow is queried on each side of the alert time, sampled
	// every metricsStep.
	metricsWindow = 15 * time.Minute
	metricsStep   = "30s"

	// maxMetricsChars caps the metric summary fed to the LLM.
	maxMetricsChars = 6000
)

const metricsSystemPrompt = `You are an SRE analyzing Kubernetes metrics during an incident.
Return JSON only: {cpuTrend: string, memoryTrend: string, restartPattern: string, anomalies: string[], confidence: float}`

// metricQueries are the PromQL queries run per incident; %q is filled
// with the affected namespace.
var metricQueries = []struct {
	name   string
	promql string
}{
	{"cpu", `sum(rate(container_cpu_usage_seconds_total{namespace=%q}[5m])) by (pod)`},
	{"memory", `sum(container_memory_working_set_bytes{namespace=%q}) by (pod)`},
	{"restarts", `sum(increase(kube_pod_container_status_restarts_total{namespace=%q}[15m])) by (pod)`},
}

// MetricsReport is the JSON shape the LLM is asked to produce; Run only
// uses it to validate that the response parses.
type MetricsReport struct {
	CPUTrend       string   `json:"cpuTrend"`
	MemoryTrend    string   `json:"memoryTrend"`
	RestartPattern string   `json:"restartPattern"`
	Anomalies      []string `json:"anomalies"`
	Confidence     float64  `json:"confidence"`
}

// samplePoint is one metric sample in the LLM context. The value stays
// the string Prometheus sent; the LLM does not need float precision and
// we avoid re-encoding artifacts.
type samplePoint struct {
	T int64  `json:"t"`
	V string `json:"v"`
}

// MetricsCorrelatorAgent queries Prometheus around the alert time and
// asks the LLM to describe CPU, memory, and restart trends.
type MetricsCorrelatorAgent struct {
	// baseURL is the fallback Prometheus endpoint when the triage spec
	// does not carry one.
	baseURL    string
	httpClient *http.Client
	llm        llm.LLMClient
}

var _ Agent = (*MetricsCorrelatorAgent)(nil)

func NewMetricsCorrelatorAgent(prometheusBaseURL string, llmClient llm.LLMClient) *MetricsCorrelatorAgent {
	return &MetricsCorrelatorAgent{
		baseURL:    prometheusBaseURL,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		llm:        llmClient,
	}
}

func (a *MetricsCorrelatorAgent) Name() string { return metricsCorrelatorName }

func (a *MetricsCorrelatorAgent) Run(ctx context.Context, triage *incidentsv1alpha1.IncidentTriage) (string, error) {
	summary, promErr := a.gatherMetrics(ctx, triage)
	if promErr != nil {
		// A dead Prometheus must not sink the whole triage run: report it
		// as this agent's finding so siblings' outputs still land.
		soft, err := json.Marshal(map[string]string{"error": promErr.Error()})
		if err != nil {
			return "", fmt.Errorf("marshaling soft error: %w", err)
		}
		return string(soft), nil
	}

	raw, err := a.llm.Complete(ctx, metricsSystemPrompt, summary)
	if err != nil {
		return "", fmt.Errorf("completing metrics correlation: %w", err)
	}

	return decodeReport[MetricsReport](raw, metricsCorrelatorName)
}

// gatherMetrics runs every query and packs the results into one compact
// JSON string: {"cpu": {"pod-a": [{"t":..., "v":"..."}]}, ...}.
func (a *MetricsCorrelatorAgent) gatherMetrics(ctx context.Context, triage *incidentsv1alpha1.IncidentTriage) (string, error) {
	promURL := triage.Spec.PrometheusURL
	if promURL == "" {
		promURL = a.baseURL
	}

	// The CR's creation time is the best proxy for when the alert fired:
	// the webhook creates it as the notification arrives.
	alertTime := triage.CreationTimestamp.Time
	if alertTime.IsZero() {
		alertTime = time.Now()
	}

	summary := make(map[string]map[string][]samplePoint, len(metricQueries))
	for _, q := range metricQueries {
		series, err := a.queryRange(ctx, promURL,
			fmt.Sprintf(q.promql, triage.Spec.AffectedNamespace),
			alertTime.Add(-metricsWindow), alertTime.Add(metricsWindow))
		if err != nil {
			return "", fmt.Errorf("querying prometheus for %s: %w", q.name, err)
		}
		summary[q.name] = series
	}

	packed, err := json.Marshal(summary)
	if err != nil {
		return "", fmt.Errorf("marshaling metric summary: %w", err)
	}
	if len(packed) > maxMetricsChars {
		packed = packed[:maxMetricsChars]
	}
	return string(packed), nil
}

// promMatrixResponse mirrors the Prometheus query_range envelope for
// resultType "matrix". Each value pair arrives as [unixSeconds, "value"].
type promMatrixResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string    `json:"metric"`
			Values [][2]json.RawMessage `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

// queryRange calls GET /api/v1/query_range and flattens the matrix into
// pod name -> samples.
func (a *MetricsCorrelatorAgent) queryRange(ctx context.Context, baseURL, query string, start, end time.Time) (map[string][]samplePoint, error) {
	params := url.Values{
		"query": {query},
		"start": {strconv.FormatInt(start.Unix(), 10)},
		"end":   {strconv.FormatInt(end.Unix(), 10)},
		"step":  {metricsStep},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		baseURL+"/api/v1/query_range?"+params.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling prometheus: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("prometheus returned %d: %s", resp.StatusCode, msg)
	}

	var parsed promMatrixResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	if parsed.Status != "success" {
		return nil, fmt.Errorf("prometheus status %q", parsed.Status)
	}

	series := make(map[string][]samplePoint, len(parsed.Data.Result))
	for _, r := range parsed.Data.Result {
		pod := r.Metric["pod"]
		if pod == "" {
			pod = "(unlabeled)"
		}
		points := make([]samplePoint, 0, len(r.Values))
		for _, v := range r.Values {
			ts, err := strconv.ParseFloat(string(v[0]), 64)
			if err != nil {
				return nil, fmt.Errorf("parsing sample timestamp %s: %w", v[0], err)
			}
			var value string
			if err := json.Unmarshal(v[1], &value); err != nil {
				return nil, fmt.Errorf("parsing sample value %s: %w", v[1], err)
			}
			points = append(points, samplePoint{T: int64(ts), V: value})
		}
		series[pod] = points
	}
	return series, nil
}
