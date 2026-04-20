#!/usr/bin/env bash
# README demo setup — brings a kind cluster to the pre-demo state:
#   - operator built, loaded, deployed (kustomize, same path as bench.sh)
#   - demo namespace with an Evict-mode GPUWorkloadBudget applied
#   - no workload yet; the vhs tape launches it so the viewer sees
#     consumption ramp on-screen
#
# Idempotent: re-running against an existing cluster only re-applies the
# manifests. Deliberately mirrors hack/bench.sh patterns so anything that
# works for the bench harness works here.
#
# Knobs:
#   DEMO_KIND_CLUSTER   default: gwb-demo
#   DEMO_NAMESPACE      default: demo
#   IMG                 default: controller:demo (non-"latest" so kubelet
#                       uses the kind-loaded image)

set -euo pipefail

: "${KIND:=kind}"
: "${KUBECTL:=kubectl}"
: "${DEMO_KIND_CLUSTER:=gwb-demo}"
: "${DEMO_NAMESPACE:=demo}"
: "${IMG:=controller:demo}"
: "${CERT_MANAGER_VERSION:=v1.15.3}"

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/../.." && pwd)"

need() { command -v "$1" >/dev/null 2>&1 || { echo "missing required tool: $1" >&2; exit 1; }; }
need "$KIND"; need "$KUBECTL"; need docker; need go

log() { printf '[demo-setup %s] %s\n' "$(date +%H:%M:%S)" "$*"; }

# 1. Kind cluster
if "$KIND" get clusters 2>/dev/null | grep -Fxq "$DEMO_KIND_CLUSTER"; then
  log "kind cluster $DEMO_KIND_CLUSTER already exists — reusing"
else
  log "creating kind cluster $DEMO_KIND_CLUSTER"
  "$KIND" create cluster --name "$DEMO_KIND_CLUSTER"
fi

# 2. Build + load operator image
log "building + loading operator image $IMG"
(cd "$root" && make docker-build IMG="$IMG" >/dev/null)
"$KIND" load docker-image "$IMG" --name "$DEMO_KIND_CLUSTER" >/dev/null

# 3. cert-manager (webhook dependency)
log "installing cert-manager $CERT_MANAGER_VERSION"
"$KUBECTL" apply -f "https://github.com/cert-manager/cert-manager/releases/download/${CERT_MANAGER_VERSION}/cert-manager.yaml" >/dev/null
"$KUBECTL" -n cert-manager rollout status deploy/cert-manager --timeout=180s
"$KUBECTL" -n cert-manager rollout status deploy/cert-manager-webhook --timeout=180s
"$KUBECTL" -n cert-manager rollout status deploy/cert-manager-cainjector --timeout=180s

# 4. Install CRDs + deploy operator.
# `make deploy` runs `kustomize edit set image` which mutates
# config/manager/kustomization.yaml — restore it via git afterwards so a
# `make demo` run doesn't leave a dirty working tree.
log "installing CRDs + operator"
(cd "$root" && make install deploy IMG="$IMG" >/dev/null)
if [ -d "$root/.git" ]; then
  (cd "$root" && git checkout -- config/manager/kustomization.yaml 2>/dev/null || true)
fi
"$KUBECTL" -n gpu-k8s-operator-system rollout status \
  deploy/gpu-k8s-operator-controller-manager --timeout=180s

# 5. Demo namespace + AlertOnly budget.
# Quota of 0.0005 gpu-hours ≈ 1.8 CPU-seconds so the workload crosses it
# inside the demo window, but AlertOnly keeps the pods running — that
# way Scene B of the tape (operator-kill) has a live workload to track,
# which is what actually proves the stateless-recovery claim.
log "creating namespace $DEMO_NAMESPACE + demo budget"
"$KUBECTL" create ns "$DEMO_NAMESPACE" --dry-run=client -o yaml \
  | "$KUBECTL" apply -f - >/dev/null

# The webhook can race rollout-status: kubelet marks the operator pod
# Ready before its TLS listener accepts connections (cert-manager cert
# is mounted seconds after the pod starts). Retry the apply until the
# webhook answers.
budget_yaml=$(cat <<EOF
apiVersion: budget.zxuhan.dev/v1alpha1
kind: GPUWorkloadBudget
metadata:
  name: demo
  namespace: $DEMO_NAMESPACE
spec:
  selector:
    matchLabels:
      app: demo
  quota:
    gpuHours: "0.0005"
    windowHours: 1
  enforcement:
    action: AlertOnly
    gracePeriodSeconds: 60
  gpuResourceName: cpu
EOF
)
for i in 1 2 3 4 5 6 7 8 9 10; do
  if printf '%s\n' "$budget_yaml" | "$KUBECTL" apply -f - >/dev/null 2>&1; then
    log "budget applied (attempt $i)"
    break
  fi
  if (( i == 10 )); then
    log "budget apply failed after $i attempts"; exit 1
  fi
  sleep 3
done

"$KUBECTL" -n "$DEMO_NAMESPACE" wait gwb/demo \
  --for=condition=Ready --timeout=30s || log "budget never reached Ready — continuing"

# 6. Build the workload generator once so the tape doesn't have to
(cd "$root" && make workload-generator >/dev/null)

log "ready — run 'vhs hack/demo/demo.tape' next"
