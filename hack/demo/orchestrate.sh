#!/usr/bin/env bash
# Drives the cluster while record.mjs captures Grafana.
#
# Timeline (t=0 is script start, which is synchronised with record.mjs
# via the leading sleep):
#   t=0    record.mjs has started; dashboard is loading
#   t=5    launch 8 workload pods (120s runtime) against the demo budget
#   t=50   delete the operator pod — informer must rebuild from API server
#   t=85   done. record.mjs (RECORD_SECONDS=90) closes the browser shortly after.
#
# The operator restart lands around the 2/3 mark of the recording so the
# post-restart "tracked pods held at 8" scene has room to breathe.
#
# Assumes hack/demo/setup.sh has left a kind cluster with:
#   - operator deployed (Helm, ServiceMonitor enabled)
#   - kube-prometheus-stack installed
#   - dashboard ConfigMap applied
#   - demo namespace + AlertOnly budget
#   - Grafana port-forwarded on :3000

set -euo pipefail

: "${KUBECTL:=kubectl}"
: "${DEMO_NAMESPACE:=demo}"
: "${OPERATOR_NS:=gpu-k8s-operator-system}"

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/../.." && pwd)"
GWB_WORKLOAD="$root/bin/gwb-workload"

log() { printf '[demo-orch %s] %s\n' "$(date +%H:%M:%S)" "$*"; }

log "t=0: letting dashboard settle"
sleep 5

log "t=5: launching 8-pod workload (120s runtime)"
"$GWB_WORKLOAD" \
  --namespace="$DEMO_NAMESPACE" \
  --label=app=demo \
  --count=8 --rate=2 --runtime=120s \
  --gpus=100m --gpu-resource=cpu >/dev/null &
WORKLOAD_PID=$!

sleep 45

log "t=50: deleting operator pod — informer must re-observe"
"$KUBECTL" delete pod -n "$OPERATOR_NS" \
  -l app.kubernetes.io/name=gwb-operator --wait=false >/dev/null 2>&1 \
  || "$KUBECTL" delete pod -n "$OPERATOR_NS" \
       -l control-plane=controller-manager --wait=false >/dev/null

sleep 35
log "t=85: orchestration done"

wait "$WORKLOAD_PID" 2>/dev/null || true
