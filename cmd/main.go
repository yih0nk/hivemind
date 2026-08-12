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

package main

import (
	"context"
	"crypto/tls"
	"flag"
	"net/http"
	"os"
	"strconv"
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	incidentsv1alpha1 "github.com/yih0nk/hivemind/api/v1alpha1"
	"github.com/yih0nk/hivemind/internal/agents"
	"github.com/yih0nk/hivemind/internal/controller"
	"github.com/yih0nk/hivemind/internal/github"
	"github.com/yih0nk/hivemind/internal/llm"
	"github.com/yih0nk/hivemind/internal/reasoner"
	amwebhook "github.com/yih0nk/hivemind/internal/webhook"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(incidentsv1alpha1.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

// nolint:gocyclo
func main() {
	var metricsAddr string
	var metricsCertPath, metricsCertName, metricsCertKey string
	var webhookCertPath, webhookCertName, webhookCertKey string
	var enableLeaderElection bool
	var probeAddr string
	var secureMetrics bool
	var enableHTTP2 bool
	var tlsOpts []func(*tls.Config)
	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	flag.StringVar(&webhookCertPath, "webhook-cert-path", "", "The directory that contains the webhook certificate.")
	flag.StringVar(&webhookCertName, "webhook-cert-name", "tls.crt", "The name of the webhook certificate file.")
	flag.StringVar(&webhookCertKey, "webhook-cert-key", "tls.key", "The name of the webhook key file.")
	flag.StringVar(&metricsCertPath, "metrics-cert-path", "",
		"The directory that contains the metrics server certificate.")
	flag.StringVar(&metricsCertName, "metrics-cert-name", "tls.crt", "The name of the metrics server certificate file.")
	flag.StringVar(&metricsCertKey, "metrics-cert-key", "tls.key", "The name of the metrics server key file.")
	flag.BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// if the enable-http2 flag is false (the default), http/2 should be disabled
	// due to its vulnerabilities. More specifically, disabling http/2 will
	// prevent from being vulnerable to the HTTP/2 Stream Cancellation and
	// Rapid Reset CVEs. For more information see:
	// - https://github.com/advisories/GHSA-qppj-fm5r-hxr3
	// - https://github.com/advisories/GHSA-4374-p667-p6c8
	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("Disabling HTTP/2")
		c.NextProtos = []string{"http/1.1"}
	}

	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	// Initial webhook TLS options
	webhookTLSOpts := tlsOpts
	webhookServerOptions := webhook.Options{
		TLSOpts: webhookTLSOpts,
	}

	if len(webhookCertPath) > 0 {
		setupLog.Info("Initializing webhook certificate watcher using provided certificates",
			"webhook-cert-path", webhookCertPath, "webhook-cert-name", webhookCertName, "webhook-cert-key", webhookCertKey)

		webhookServerOptions.CertDir = webhookCertPath
		webhookServerOptions.CertName = webhookCertName
		webhookServerOptions.KeyName = webhookCertKey
	}

	webhookServer := webhook.NewServer(webhookServerOptions)

	// Metrics endpoint is enabled in 'config/default/kustomization.yaml'. The Metrics options configure the server.
	// More info:
	// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.24.1/pkg/metrics/server
	// - https://book.kubebuilder.io/reference/metrics.html
	metricsServerOptions := metricsserver.Options{
		BindAddress:   metricsAddr,
		SecureServing: secureMetrics,
		TLSOpts:       tlsOpts,
	}

	if secureMetrics {
		// FilterProvider is used to protect the metrics endpoint with authn/authz.
		// These configurations ensure that only authorized users and service accounts
		// can access the metrics endpoint. The RBAC are configured in 'config/rbac/kustomization.yaml'. More info:
		// https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.24.1/pkg/metrics/filters#WithAuthenticationAndAuthorization
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	// If the certificate is not specified, controller-runtime will automatically
	// generate self-signed certificates for the metrics server. While convenient for development and testing,
	// this setup is not recommended for production.
	//
	// TODO(user): If you enable certManager, uncomment the following lines:
	// - [METRICS-WITH-CERTS] at config/default/kustomization.yaml to generate and use certificates
	// managed by cert-manager for the metrics server.
	// - [PROMETHEUS-WITH-CERTS] at config/prometheus/kustomization.yaml for TLS certification.
	if len(metricsCertPath) > 0 {
		setupLog.Info("Initializing metrics certificate watcher using provided certificates",
			"metrics-cert-path", metricsCertPath, "metrics-cert-name", metricsCertName, "metrics-cert-key", metricsCertKey)

		metricsServerOptions.CertDir = metricsCertPath
		metricsServerOptions.CertName = metricsCertName
		metricsServerOptions.KeyName = metricsCertKey
	}

	restConfig := ctrl.GetConfigOrDie()

	mgr, err := ctrl.NewManager(restConfig, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		WebhookServer:          webhookServer,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "1f393ca0.yih0nk.github.io",
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		// LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "Failed to start manager")
		os.Exit(1)
	}

	// The log subresource is not served by controller-runtime's client,
	// so the log reader gets a plain client-go clientset.
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		setupLog.Error(err, "Failed to build clientset")
		os.Exit(1)
	}

	ollamaModel := os.Getenv("HIVEMIND_OLLAMA_MODEL")
	if ollamaModel == "" {
		ollamaModel = "llama3.2"
	}
	llmClient := llm.NewOllamaClient(ollamaModel)
	// The client defaults to localhost:11434, which only works when the
	// operator runs on the same host as Ollama; in-cluster deployments
	// must point this at a Service URL.
	if url := os.Getenv("HIVEMIND_OLLAMA_URL"); url != "" {
		llmClient.BaseURL = url
	}
	// Required by key-gated providers (Groq); a local Ollama needs none.
	if apiKey := os.Getenv("HIVEMIND_LLM_API_KEY"); apiKey != "" {
		llmClient.APIKey = apiKey
	}

	// Fallback when a triage spec carries no prometheusURL; the webhook
	// stamps the same default into the CRs it creates.
	prometheusURL := os.Getenv("HIVEMIND_PROMETHEUS_URL")
	if prometheusURL == "" {
		prometheusURL = "http://prometheus-operated:9090"
	}

	// The runbook ConfigMap lives in the operator's own namespace,
	// injected via the downward API in the Deployment.
	operatorNamespace := os.Getenv("POD_NAMESPACE")
	if operatorNamespace == "" {
		operatorNamespace = "hivemind-system"
	}

	// Zero means "use the dispatcher's default"; slow local models
	// (first Ollama call loads the model from disk) may need more.
	var agentTimeout time.Duration
	if raw := os.Getenv("HIVEMIND_AGENT_TIMEOUT_SECONDS"); raw != "" {
		secs, err := strconv.Atoi(raw)
		if err != nil || secs <= 0 {
			setupLog.Error(err, "Invalid HIVEMIND_AGENT_TIMEOUT_SECONDS, using default", "value", raw)
		} else {
			agentTimeout = time.Duration(secs) * time.Second
		}
	}

	// Without a token the operator still runs end to end: the fake PR
	// client records requests and returns a placeholder URL, so triage
	// completes and the report stays inspectable in status.agentOutputs.
	var prClient github.PRClient
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		prClient = github.NewRESTClient(token)
	} else {
		setupLog.Info("GITHUB_TOKEN not set; incident PRs will not be opened on GitHub")
		prClient = &github.FakePRClient{PRURL: "https://example.invalid/hivemind/pr"}
	}

	// Agents get the uncached API reader: they read once per incident,
	// which does not justify the cluster-wide informers the cached
	// client would start (and the watch RBAC they would need).
	dispatcher := &agents.Dispatcher{
		Agents: []agents.Agent{
			agents.NewLogTriageAgent(
				mgr.GetAPIReader(),
				&agents.ClientsetPodLogReader{Clientset: clientset},
				llmClient,
			),
			agents.NewMetricsCorrelatorAgent(prometheusURL, llmClient),
			agents.NewRunbookLookupAgent(mgr.GetAPIReader(), operatorNamespace),
		},
		MaxConcurrent: 4,
		Timeout:       agentTimeout,
	}

	// Synthesis runs in-process by default. When HIVEMIND_REASONER_URL is
	// set, delegate it to the external LangGraph brain instead: the operator
	// still gathers evidence, the brain runs the reflection loop over it.
	// Note the brain makes several LLM calls per triage, and the controller
	// bounds this call with AgentTimeout (default 30s) -- raise
	// HIVEMIND_AGENT_TIMEOUT_SECONDS when pointing at the brain.
	var reasonerImpl reasoner.Reasoner = reasoner.NewInProcess(agents.NewSynthesizerAgent(llmClient))
	if brainURL := os.Getenv("HIVEMIND_REASONER_URL"); brainURL != "" {
		reasonerImpl = reasoner.NewHTTPReasoner(brainURL, agentTimeout)
		setupLog.Info("Delegating synthesis to external LangGraph brain", "url", brainURL)
	}

	if err := (&controller.IncidentTriageReconciler{
		Client:       mgr.GetClient(),
		Scheme:       mgr.GetScheme(),
		Recorder:     mgr.GetEventRecorder("hivemind"),
		Dispatcher:   dispatcher,
		Reasoner:     reasonerImpl,
		PRClient:     prClient,
		AgentTimeout: agentTimeout,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "incidenttriage")
		os.Exit(1)
	}
	// +kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "Failed to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "Failed to set up ready check")
		os.Exit(1)
	}

	webhookAddr := ":8080"
	if port := os.Getenv("HIVEMIND_WEBHOOK_PORT"); port != "" {
		webhookAddr = ":" + port
	}
	alertmanagerMux := http.NewServeMux()
	alertmanagerMux.Handle("POST /webhook", amwebhook.NewAlertmanagerHandler(mgr.GetClient()))
	alertmanagerMux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	alertmanagerServer := &http.Server{
		Addr:              webhookAddr,
		Handler:           alertmanagerMux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	// Registered as a Runnable so the manager owns its lifecycle: it
	// starts with the manager and drains gracefully when the manager's
	// context is canceled on SIGTERM.
	if err := mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		serveErr := make(chan error, 1)
		go func() { serveErr <- alertmanagerServer.ListenAndServe() }()
		setupLog.Info("Starting alertmanager webhook server", "addr", webhookAddr)
		select {
		case err := <-serveErr:
			return err
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return alertmanagerServer.Shutdown(shutdownCtx)
		}
	})); err != nil {
		setupLog.Error(err, "Failed to add alertmanager webhook server")
		os.Exit(1)
	}

	setupLog.Info("Starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "Failed to run manager")
		os.Exit(1)
	}
}
