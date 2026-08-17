# Hivemind

A Kubernetes operator that turns a firing Prometheus alert into a GitHub pull request containing an LLM-generated root-cause report — logs, metrics, and runbook guidance triaged by a swarm of agents before a human ever opens a terminal.

## How it works

A Prometheus alert fires and Alertmanager POSTs it to the operator's webhook receiver, which creates an `IncidentTriage` custom resource in the alert's namespace. The reconciler drives that CR through a phase machine, fanning out three evidence agents concurrently with an `errgroup`: one fetches pod logs, one queries Prometheus for resource trends, one matches the alert against a ConfigMap of runbooks. A synthesizer agent then combines their outputs into a root-cause summary and recommended fix, and the operator opens a GitHub PR with the full report. All LLM calls go through any OpenAI-compatible backend — a local Ollama by default, or a hosted provider like Groq.

Built in Go with kubebuilder v4.

## Architecture

```
Prometheus ──alert──▶ Alertmanager ──POST /webhook──▶ ┌──────────────────────┐
                                                      │  Hivemind Operator   │
                                                      │  webhook receiver    │
                                                      │   └▶ IncidentTriage  │
                                                      │  reconciler          │
                                                      │  dispatcher          │
                                                      └──────────┬───────────┘
                                               fan out (errgroup)│
                               ┌──────────────────┬──────────────┤
                               ▼                  ▼              ▼
                          logtriage       metricscorrelator  runbooklookup
                          (pod logs)        (Prometheus)      (ConfigMap)
                               │                  │              │
                               └────────┬─────────┴──────────────┘
                                        ▼
                                   synthesizer ◀───▶ Ollama / Groq (LLM)
                                        │
                                        ▼
                             GitHub PR: root-cause report
```

## Reasoning service (experimental)

The operator's synthesizer is a single LLM pass over a fixed fan-out. The
[`brain/`](brain/) service explores a more capable successor: a
[LangGraph](https://langchain-ai.github.io/langgraph/) reasoning layer that
synthesizes a root-cause hypothesis, **critiques its own confidence, and loops
to re-examine the evidence** until it is confident or hits an iteration cap —
a cyclic graph the operator's `errgroup` DAG can't express.

The operator reaches it through a **`Reasoner` seam** ([`internal/reasoner`](internal/reasoner/)):
the Triaging phase's synthesis step is an interface, satisfied in-process by the
LLM synthesizer agent by default, or by an HTTP client to the brain when
`HIVEMIND_REASONER_URL` is set. Evidence collection stays in the operator (it
holds the cluster credentials); only the reasoning is delegated.

The chart ships the brain as its own Deployment + Service (`brain.enabled`, on by
default) and wires `HIVEMIND_REASONER_URL` at it automatically:

```sh
make docker-build-brain docker-push-brain
helm upgrade --install hivemind ./charts/hivemind \
  --set llmApiKey=$GROQ_API_KEY --set brain.provider=groq
```

For local dev, run the brain out-of-cluster (`python -m hivemind_brain.server`)
and `export HIVEMIND_REASONER_URL=http://localhost:8090` before `make run`. The
brain is Groq-backed with a deterministic mock fallback for offline tests; set
`brain.enabled=false` to use the operator's in-process synthesizer instead. See
[`brain/README.md`](brain/README.md).

## Quickstart

Prerequisites: Go 1.26+, kubectl, [kind](https://kind.sigs.k8s.io/), Helm 3, and [Ollama](https://ollama.com/) with the `llama3.2` model pulled.

1. Clone and install the CRD:

   ```sh
   git clone https://github.com/yih0nk/hivemind.git && cd hivemind
   kind create cluster --name hivemind
   make install
   ```

2. Apply the runbooks:

   ```sh
   kubectl create namespace hivemind-system
   kubectl apply -n hivemind-system -f config/samples/runbooks.yaml
   ```

3. Run the operator locally:

   ```sh
   HIVEMIND_AGENT_TIMEOUT_SECONDS=120 make run
   ```

   Or against Groq instead of local Ollama:

   ```sh
   HIVEMIND_OLLAMA_URL=https://api.groq.com/openai/v1 \
   HIVEMIND_OLLAMA_MODEL=llama-3.1-70b-versatile \
   HIVEMIND_LLM_API_KEY=<your-groq-key> \
   HIVEMIND_AGENT_TIMEOUT_SECONDS=30 make run
   ```

4. Simulate an incident (or run `hack/simulate-incident.sh` to do all of this in one step):

   ```sh
   kubectl apply -f config/samples/chaos/crashloop.yaml
   kubectl apply -f config/samples/chaos/manual-cr.yaml
   kubectl get incidenttriages -n hivemind-test -w
   ```

5. Read the output:

   ```sh
   kubectl get incidenttriage chaos-crashloop-manual \
     -n hivemind-test -o yaml
   ```

   The full agent reports live in `status.agentOutputs`; the PR URL (when a GitHub token is configured) in `status.prURL`.

## Deploy to a cluster (Helm)

```sh
helm upgrade --install hivemind ./charts/hivemind \
  --set ollamaURL=http://<your-ollama-svc>:11434 \
  --set githubToken=<token> \
  --set githubRepo=owner/repo
```

Then point Alertmanager at `http://hivemind.<namespace>:8080/webhook` — every firing alert routed there becomes an `IncidentTriage` CR. See [config/samples/alertmanager-webhook-config.yaml](config/samples/alertmanager-webhook-config.yaml) for a ready-made receiver snippet (usable directly as a kube-prometheus-stack values overlay).

## Agents

| Agent | What it does |
|---|---|
| logtriage | Fetches pod logs, LLM identifies error lines and likely cause |
| metricscorrelator | Queries Prometheus for CPU/memory/restarts, LLM summarizes trends |
| runbooklookup | Keyword-matches alert name against a ConfigMap of runbooks |
| synthesizer | Combines all three into a root-cause summary and recommended fix |

## Configuration

All configuration is via environment variables:

| Variable | Default | Purpose |
|---|---|---|
| `HIVEMIND_OLLAMA_URL` | `http://localhost:11434` | Base URL of the OpenAI-compatible LLM API |
| `HIVEMIND_OLLAMA_MODEL` | `llama3.2` | Model name sent to the LLM backend |
| `HIVEMIND_PROMETHEUS_URL` | `http://prometheus-operated:9090` | Prometheus queried by the metrics agent; also stamped into CRs created by the webhook |
| `HIVEMIND_LLM_API_KEY` | *(empty)* | Bearer token for key-gated providers like Groq; not needed for local Ollama (Helm value: `llmApiKey`) |
| `HIVEMIND_AGENT_TIMEOUT_SECONDS` | `30` | Per-agent timeout; raise for slow local models (first call loads the model from disk) |
| `HIVEMIND_WEBHOOK_PORT` | `8080` | Port for the Alertmanager webhook server |
| `HIVEMIND_GITHUB_REPO` | *(empty)* | `owner/repo` the webhook stamps into CRs it creates from alerts |
| `GITHUB_TOKEN` | *(empty)* | Token for opening PRs; when unset, triage still completes but no PR is opened |
| `POD_NAMESPACE` | `hivemind-system` | Namespace holding the runbooks ConfigMap (injected via the downward API in-cluster) |

## Known limitations

- Ollama must be reachable from the operator pod; `localhost:11434` works for `make run` but not in-cluster unless Ollama is deployed as a Service.
- A dead Ollama sets `phase=Failed` (by design — triage without an LLM is not useful).

## License

MIT
