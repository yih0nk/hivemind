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

// Package webhook receives Alertmanager notifications and turns firing
// alerts into IncidentTriage resources: alerts come in over HTTP, CRs go
// out through the Kubernetes API, and the reconciler takes it from there.
package webhook

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	incidentsv1alpha1 "github.com/yih0nk/hivemind/api/v1alpha1"
)

const (
	// statusFiring is the per-alert status while an alert is active;
	// anything else (resolved) is ignored.
	statusFiring = "firing"

	// Alert labels the handler maps into the IncidentTriage spec.
	labelAlertname = "alertname"
	labelNamespace = "namespace"
	labelSeverity  = "severity"

	defaultPrometheusURL = "http://prometheus-operated:9090"
	defaultNamespace     = "default"

	// podSelectorPrefix marks alert labels that describe the affected
	// pods: pod_app=checkout becomes the selector app=checkout.
	podSelectorPrefix = "pod_"

	// maxBodyBytes caps the request body; Alertmanager payloads are small.
	maxBodyBytes = 1 << 20
)

// Message is the Alertmanager webhook payload (version "4").
// https://prometheus.io/docs/alerting/latest/configuration/#webhook_config
type Message struct {
	Version           string            `json:"version"`
	GroupKey          string            `json:"groupKey"`
	Receiver          string            `json:"receiver"`
	Status            string            `json:"status"`
	Alerts            []Alert           `json:"alerts"`
	GroupLabels       map[string]string `json:"groupLabels"`
	CommonLabels      map[string]string `json:"commonLabels"`
	CommonAnnotations map[string]string `json:"commonAnnotations"`
	ExternalURL       string            `json:"externalURL"`
}

// Alert is one alert inside a Message. Status is per-alert: a single
// notification can mix firing and resolved alerts.
type Alert struct {
	Status       string            `json:"status"`
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     time.Time         `json:"startsAt"`
	EndsAt       time.Time         `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL"`
	Fingerprint  string            `json:"fingerprint"`
}

// AlertmanagerHandler creates an IncidentTriage for every firing alert
// it receives.
type AlertmanagerHandler struct {
	// Client creates the CRs. Injected so tests can use a fake.
	Client client.Client

	// GithubRepo (owner/repo) is stamped into every CR's spec.
	GithubRepo string

	// PrometheusURL is stamped into every CR's spec.
	PrometheusURL string
}

var _ http.Handler = (*AlertmanagerHandler)(nil)

// The handler creates IncidentTriage CRs from incoming alerts; the
// reconciler's markers only cover reading and updating them.
// +kubebuilder:rbac:groups=incidents.yih0nk.github.io,resources=incidenttriages,verbs=create

// NewAlertmanagerHandler builds a handler configured from the environment:
// HIVEMIND_GITHUB_REPO and HIVEMIND_PROMETHEUS_URL.
func NewAlertmanagerHandler(c client.Client) *AlertmanagerHandler {
	promURL := os.Getenv("HIVEMIND_PROMETHEUS_URL")
	if promURL == "" {
		promURL = defaultPrometheusURL
	}
	return &AlertmanagerHandler{
		Client:        c,
		GithubRepo:    os.Getenv("HIVEMIND_GITHUB_REPO"),
		PrometheusURL: promURL,
	}
}

// ServeHTTP always answers 200: Alertmanager retries non-2xx responses,
// and retrying won't fix a payload we couldn't handle. Failures are
// logged instead of surfaced.
func (h *AlertmanagerHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log := logf.Log.WithName("alertmanager-webhook")

	var msg Message
	body := http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if err := json.NewDecoder(body).Decode(&msg); err != nil {
		log.Error(err, "Failed to decode alertmanager payload")
		w.WriteHeader(http.StatusOK)
		return
	}

	for _, alert := range msg.Alerts {
		if alert.Status != statusFiring {
			continue
		}
		triage := h.triageFor(alert)
		err := h.Client.Create(r.Context(), triage)
		switch {
		case apierrors.IsAlreadyExists(err):
			log.Info("IncidentTriage already exists, skipping",
				"name", triage.Name, "namespace", triage.Namespace)
		case err != nil:
			log.Error(err, "Failed to create IncidentTriage",
				"name", triage.Name, "namespace", triage.Namespace)
		default:
			log.Info("Created IncidentTriage from alert",
				"name", triage.Name, "namespace", triage.Namespace,
				"alertname", triage.Spec.AlertName)
		}
	}
	w.WriteHeader(http.StatusOK)
}

// triageFor maps one firing alert onto an IncidentTriage.
func (h *AlertmanagerHandler) triageFor(alert Alert) *incidentsv1alpha1.IncidentTriage {
	namespace := alert.Labels[labelNamespace]
	if namespace == "" {
		namespace = defaultNamespace
	}
	return &incidentsv1alpha1.IncidentTriage{
		ObjectMeta: metav1.ObjectMeta{
			Name:      crName(alert),
			Namespace: namespace,
		},
		Spec: incidentsv1alpha1.IncidentTriageSpec{
			AlertName:           alert.Labels[labelAlertname],
			Severity:            severityFor(alert.Labels[labelSeverity]),
			AffectedNamespace:   namespace,
			AffectedPodSelector: podSelectorFor(alert.Labels),
			PrometheusURL:       h.PrometheusURL,
			GithubRepo:          h.GithubRepo,
		},
	}
}

// severityFor normalizes the alert's severity label to the CRD enum.
// Missing or unrecognized values become warning rather than producing a
// CR the API server would reject.
func severityFor(label string) incidentsv1alpha1.Severity {
	switch s := incidentsv1alpha1.Severity(label); s {
	case incidentsv1alpha1.SeverityCritical, incidentsv1alpha1.SeverityWarning, incidentsv1alpha1.SeverityInfo:
		return s
	default:
		return incidentsv1alpha1.SeverityWarning
	}
}

// podSelectorFor collects alert labels carrying the pod_ prefix, with
// the prefix stripped. Returns nil when no such labels exist so the
// optional spec field stays unset.
func podSelectorFor(labels map[string]string) map[string]string {
	var selector map[string]string
	for k, v := range labels {
		key, found := strings.CutPrefix(k, podSelectorPrefix)
		if !found || key == "" {
			continue
		}
		if selector == nil {
			selector = map[string]string{}
		}
		selector[key] = v
	}
	return selector
}

// crName builds a valid, deterministic object name for the alert. The
// hash covers the alert's identity (fingerprint) and this firing's start
// time: repeat notifications of the same firing map to the same name, so
// Create hits AlreadyExists and the handler skips them, while a fresh
// firing of the same alert gets a new CR.
func crName(alert Alert) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%s/%d", alert.Fingerprint, alert.StartsAt.Unix()))
	return sanitizeName(alert.Labels[labelAlertname]) + "-" + hex.EncodeToString(sum[:4])
}

// sanitizeName squeezes an alertname into an RFC 1123 label: lowercase
// alphanumerics and dashes, everything else collapsed to single dashes.
func sanitizeName(alertname string) string {
	mapped := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		default:
			return '-'
		}
	}, strings.ToLower(alertname))

	name := strings.Join(strings.FieldsFunc(mapped, func(r rune) bool { return r == '-' }), "-")
	if name == "" {
		return "alert"
	}
	if len(name) > 40 {
		name = strings.TrimRight(name[:40], "-")
	}
	return name
}
