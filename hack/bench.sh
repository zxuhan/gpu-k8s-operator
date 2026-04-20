#!/usr/bin/env bash
# Benchmark harness entrypoint.
#
# Orchestrates a one-shot accuracy bench on a kind cluster:
#   1. ensure kind cluster up, operator image built + loaded
#   2. install CRDs, deploy operator, wait for rollout
#   3. apply a dedicated AlertOnly budget with a high quota (we measure
#      accounting accuracy here, *not* enforcement latency — that lives
#      under Phase 7's chaos tests)
#   4. launch N pods at R/s via gwb-workload
#   5. sleep until BENCH_OBSERVE_SECONDS past first-pod create
#   6. snapshot the GWB status and feed it to gwb-bench report
#   7. write bench-results/YYYY-MM-DD/{status.json,results.json,SUMMARY.md}
#
# Defaults are tuned for an 8-core laptop running kind: 50 pods at 10/s
# with a 0.1 CPU request each peaks at 5 concurrent CPU-cores worth of
# pods, which kind schedules without queueing — keeping the observed
# run aligned with the expected-curve formula that assumes instant
# start-time.
#
# Knobs (env vars, with defaults):
#   BENCH_OUT              — output directory (default: bench-results/$(date))
#   KIND_CLUSTER           — kind cluster name (default: gwb-bench)
#   BENCH_NAMESPACE        — namespace for pods+budget (default: gwb-bench)
#   BENCH_COUNT            — pod count (default: 50)
#   BENCH_RATE             — pods per second (default: 10)
#   BENCH_RUNTIME          — per-pod sleep (default: 30s)
#   BENCH_GPUS             — resource request per pod (default: "100m")
#   BENCH_GPU_RESOURCE     — resource name (default: cpu)
#   BENCH_OBSERVE_SECONDS  — elapsed at snapshot (default: 45)
#   KEEP_CLUSTER           — 1 keeps the kind cluster around (default: 0)
#   IMG                    — operator image tag (default: controller:latest)

set -euo pipefail

: "${BENCH_OUT:=bench-results/$(date +%Y-%m-%d)}"
: "${KIND:=kind}"
: "${KUBECTL:=kubectl}"
: "${KIND_CLUSTER:=gwb-bench}"
: "${BENCH_NAMESPACE:=gwb-bench}"
: "${BENCH_COUNT:=50}"
: "${BENCH_RATE:=10}"
: "${BENCH_RUNTIME:=30s}"
: "${BENCH_GPUS:=100m}"
: "${BENCH_GPU_RESOURCE:=cpu}"
: "${BENCH_OBSERVE_SECONDS:=45}"
: "${BENCH_LABEL:=app=gwb-bench-worker}"
: "${KEEP_CLUSTER:=0}"
# Deliberately avoid ":latest" — kubelet defaults imagePullPolicy to Always
# for :latest, which makes kind's image-load pointless (kubelet would try to
# pull from docker.io instead). Any non-"latest" tag defaults to IfNotPresent.
: "${IMG:=controller:bench}"

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/.." && pwd)"

need() { command -v "$1" >/dev/null 2>&1 || { echo "missing required tool: $1" >&2; exit 1; }; }
need "$KIND"
need "$KUBECTL"
need docker
need go

# Fail fast when the prebuilt binaries aren't there — `make bench` builds
# them, but someone running hack/bench.sh directly would otherwise hit a
# cryptic error halfway through the run.
[[ -x "$root/bin/gwb-workload" ]] || { echo "missing $root/bin/gwb-workload — run 'make workload-generator'" >&2; exit 1; }
[[ -x "$root/bin/gwb-bench" ]] || { echo "missing $root/bin/gwb-bench — run 'make gwb-bench'" >&2; exit 1; }

mkdir -p "$BENCH_OUT"
: > "$BENCH_OUT/bench.log"
exec > >(tee -a "$BENCH_OUT/bench.log") 2>&1

log() { printf '[bench %s] %s\n' "$(date +%H:%M:%S)" "$*"; }

cleanup() {
  local rc=$?
  if [[ "$KEEP_CLUSTER" != "1" ]]; then
    log "tearing down kind cluster $KIND_CLUSTER (set KEEP_CLUSTER=1 to skip)"
    "$KIND" delete cluster --name "$KIND_CLUSTER" >/dev/null 2>&1 || true
  else
    log "leaving kind cluster $KIND_CLUSTER running (KEEP_CLUSTER=1)"
  fi
  exit "$rc"
}
trap cleanup EXIT

label_key="${BENCH_LABEL%=*}"
label_val="${BENCH_LABEL#*=}"

log "bench output: $BENCH_OUT"
log "scenario: count=$BENCH_COUNT rate=$BENCH_RATE runtime=$BENCH_RUNTIME gpus=$BENCH_GPUS resource=$BENCH_GPU_RESOURCE observe=${BENCH_OBSERVE_SECONDS}s"

###############################################################################
# 1. Kind cluster
###############################################################################
if "$KIND" get clusters 2>/dev/null | grep -Fxq "$KIND_CLUSTER"; then
  log "kind cluster $KIND_CLUSTER already exists — reusing"
else
  log "creating kind cluster $KIND_CLUSTER"
  "$KIND" create cluster --name "$KIND_CLUSTER"
fi

###############################################################################
# 2. Build + load operator image
###############################################################################
log "building operator image $IMG"
(cd "$root" && make docker-build IMG="$IMG" >/dev/null)
log "loading $IMG into kind"
"$KIND" load docker-image "$IMG" --name "$KIND_CLUSTER" >/dev/null

###############################################################################
# 3. Install cert-manager, CRDs, deploy operator
#
# The kustomize deploy in config/ uses cert-manager Issuer + Certificate
# resources for the webhook/metrics TLS. Install cert-manager first so
# those resources have a home; use the upstream static manifest rather
# than helm to keep the bench dependency set small.
###############################################################################
: "${CERT_MANAGER_VERSION:=v1.15.3}"
log "installing cert-manager $CERT_MANAGER_VERSION"
"$KUBECTL" apply -f "https://github.com/cert-manager/cert-manager/releases/download/${CERT_MANAGER_VERSION}/cert-manager.yaml" >/dev/null
"$KUBECTL" -n cert-manager rollout status deploy/cert-manager --timeout=180s
"$KUBECTL" -n cert-manager rollout status deploy/cert-manager-webhook --timeout=180s
"$KUBECTL" -n cert-manager rollout status deploy/cert-manager-cainjector --timeout=180s

log "installing CRDs + operator"
(cd "$root" && make install deploy IMG="$IMG" >/dev/null)

log "waiting for operator rollout"
"$KUBECTL" -n gpu-k8s-operator-system rollout status \
  deploy/gpu-k8s-operator-controller-manager --timeout=180s

###############################################################################
# 4. Namespace + bench budget
#
# Quota is deliberately large (1 GPU-hour) so enforcement never fires during
# the bench window. We're measuring reported-vs-expected *accounting*, not
# "did the right pod get killed when" — that's Phase 7.
###############################################################################
log "provisioning namespace + budget in ns=$BENCH_NAMESPACE"
"$KUBECTL" create ns "$BENCH_NAMESPACE" --dry-run=client -o yaml \
  | "$KUBECTL" apply -f - >/dev/null

bench_budget="$BENCH_OUT/budget.yaml"
cat > "$bench_budget" <<EOF
apiVersion: budget.zxuhan.dev/v1alpha1
kind: GPUWorkloadBudget
metadata:
  name: bench
  namespace: $BENCH_NAMESPACE
spec:
  selector:
    matchLabels:
      $label_key: $label_val
  quota:
    gpuHours: "1"
    windowHours: 24
  enforcement:
    action: AlertOnly
    gracePeriodSeconds: 30
  gpuResourceName: $BENCH_GPU_RESOURCE
EOF
"$KUBECTL" apply -f "$bench_budget" >/dev/null

# Ensure the controller has seen the budget once before the first pod lands,
# so the reconcile-lag contribution to the observed delta is bounded by the
# normal tick cadence, not first-reconcile queue time.
"$KUBECTL" -n "$BENCH_NAMESPACE" wait gwb/bench \
  --for=condition=Ready --timeout=30s || {
    log "bench budget never became Ready — continuing; check controller logs"; }

###############################################################################
# 5. Launch workload
###############################################################################
start_ts=$(date +%s)
log "launching gwb-workload"
"$root/bin/gwb-workload" \
  --namespace="$BENCH_NAMESPACE" \
  --label="$BENCH_LABEL" \
  --count="$BENCH_COUNT" \
  --rate="$BENCH_RATE" \
  --runtime="$BENCH_RUNTIME" \
  --gpus="$BENCH_GPUS" \
  --gpu-resource="$BENCH_GPU_RESOURCE"

###############################################################################
# 6. Wait out the observation window
###############################################################################
now=$(date +%s)
elapsed_so_far=$(( now - start_ts ))
remaining=$(( BENCH_OBSERVE_SECONDS - elapsed_so_far ))
if (( remaining > 0 )); then
  log "observation: ${elapsed_so_far}s elapsed in launch, sleeping ${remaining}s to reach ${BENCH_OBSERVE_SECONDS}s"
  sleep "$remaining"
else
  log "launch already took ${elapsed_so_far}s >= ${BENCH_OBSERVE_SECONDS}s — snapshotting immediately"
fi

###############################################################################
# 7. Snapshot + report
###############################################################################
status_json="$BENCH_OUT/status.json"
log "snapshotting gwb/bench status to $status_json"
"$KUBECTL" -n "$BENCH_NAMESPACE" get gwb bench -o json > "$status_json"

log "running gwb-bench report"
"$root/bin/gwb-bench" report \
  --count="$BENCH_COUNT" \
  --rate="$BENCH_RATE" \
  --runtime="$BENCH_RUNTIME" \
  --gpus="$(python3 - "$BENCH_GPUS" <<'PY'
# Translate the Kubernetes-quantity form (e.g. "100m", "1", "500m") used
# by the workload generator into the plain float the bench harness wants.
# Done in-line rather than in Go because bench.sh is the only caller and
# the translation is trivial — keeping it here avoids adding a second
# CLI binary just for parsing.
import sys
raw = sys.argv[1]
if raw.endswith("m"):
    print(float(raw[:-1]) / 1000.0)
else:
    print(float(raw))
PY
)" \
  --elapsed="${BENCH_OBSERVE_SECONDS}s" \
  --status-json="$status_json" \
  --out-dir="$BENCH_OUT" \
  --label="count=$BENCH_COUNT rate=$BENCH_RATE runtime=$BENCH_RUNTIME gpus=$BENCH_GPUS resource=$BENCH_GPU_RESOURCE"

log "done — see $BENCH_OUT/SUMMARY.md"
