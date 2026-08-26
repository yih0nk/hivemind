#!/usr/bin/env bash
# Demonstrates the human-in-the-loop approval gate against the current
# kubectl context: file a gated IncidentTriage, wait for it to pause at
# AwaitingApproval, show the proposed root cause/fix, then approve (or
# reject) it and watch the run finish.
#
# Prerequisites:
#   - CRDs installed (make install) and the operator running (make run,
#     or deployed in-cluster)
#   - The brain reachable (HIVEMIND_REASONER_URL set); the in-process
#     synthesizer cannot pause, so the gate only works with the brain.
#
# Usage:
#   hack/approve-incident.sh            # approves after the pause
#   DECISION=reject hack/approve-incident.sh
set -euo pipefail

cd "$(dirname "$0")/.."

CR_NAME=checkout-oom-gated
NS=default
DECISION="${DECISION:-approve}"
TIMEOUT="${HIVEMIND_APPROVAL_TIMEOUT:-120}"

echo "==> Filing a gated IncidentTriage"
kubectl apply -f config/samples/incidents_v1alpha1_incidenttriage_gated.yaml

echo "==> Waiting for the run to pause at AwaitingApproval"
for _ in $(seq 1 "$TIMEOUT"); do
  phase=$(kubectl get incidenttriage "$CR_NAME" -n "$NS" \
    -o jsonpath='{.status.phase}' 2>/dev/null || true)
  if [ "$phase" = "AwaitingApproval" ]; then
    break
  fi
  if [ "$phase" = "Failed" ] || [ "$phase" = "Remediated" ]; then
    echo "!! Run reached $phase without pausing (is the brain wired up?)"
    kubectl get incidenttriage "$CR_NAME" -n "$NS" -o jsonpath='{.status.message}'
    exit 1
  fi
  sleep 1
done

echo "==> Proposal awaiting your decision:"
kubectl get incidenttriage "$CR_NAME" -n "$NS" \
  -o jsonpath='{.status.pendingProposal}'
echo

echo "==> Recording decision: $DECISION"
kubectl annotate --overwrite incidenttriage "$CR_NAME" -n "$NS" \
  "hivemind.io/approval=$DECISION"

echo "==> Waiting for the run to finish"
for _ in $(seq 1 "$TIMEOUT"); do
  phase=$(kubectl get incidenttriage "$CR_NAME" -n "$NS" \
    -o jsonpath='{.status.phase}' 2>/dev/null || true)
  if [ "$phase" = "Remediated" ] || [ "$phase" = "Failed" ]; then
    break
  fi
  sleep 1
done

echo "==> Final state:"
kubectl get incidenttriage "$CR_NAME" -n "$NS" \
  -o custom-columns=PHASE:.status.phase,PR:.status.prURL,MESSAGE:.status.message
