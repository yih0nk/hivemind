#!/usr/bin/env bash
# Simulates an incident end to end against the current kubectl context:
# break a deployment, file an IncidentTriage by hand, watch the operator
# triage it, and print the resulting PR URL.
#
# Prerequisites:
#   - CRDs installed (make install) and the operator running (make run,
#     or deployed in-cluster)
#   - Ollama serving the configured model, or expect the LLM agents to
#     degrade and the run to publish a partial report
set -euo pipefail

cd "$(dirname "$0")/.."

NS=hivemind-test
CR_NAME=chaos-crashloop-manual
TIMEOUT="${HIVEMIND_SIM_TIMEOUT:-300s}"

echo "==> Deploying the crashlooping workload"
kubectl apply -f config/samples/chaos/crashloop.yaml

echo "==> Waiting 10s for the pod to start crashlooping"
sleep 10
kubectl get pods -n "$NS" -l app=chaos-crashloop

echo "==> Filing the IncidentTriage"
kubectl apply -f config/samples/chaos/manual-cr.yaml

echo "==> Watching the triage (phase transitions appear below)"
kubectl get incidenttriages -n "$NS" -w &
WATCH_PID=$!
trap 'kill "$WATCH_PID" 2>/dev/null || true' EXIT

if ! kubectl wait "incidenttriage/$CR_NAME" -n "$NS" \
  --for=jsonpath='{.status.phase}'=Remediated --timeout="$TIMEOUT"; then
  echo "!! Triage did not reach Remediated within $TIMEOUT" >&2
  echo "!! Current status:" >&2
  kubectl get "incidenttriage/$CR_NAME" -n "$NS" -o jsonpath='{.status.message}' >&2
  echo >&2
  exit 1
fi

echo
echo "==> Incident remediated. PR:"
kubectl get "incidenttriage/$CR_NAME" -n "$NS" -o jsonpath='{.status.prURL}'
echo
