#!/usr/bin/env bash
# Restart-correctness chaos scenario.
#
# Premise: the accounting engine is stateless — every reconcile recomputes
# consumedGpuHours from the current pod view. So a crashing operator
# shouldn't lose meaningful ground against a continuous observer, as long
# as kubelet hasn't GC'd any terminated pods during the downtime.
#
# What this script actually does:
#   1. same kind bring-up as hack/bench.sh (separate cluster, same image)
#   2. launch a workload; take a PRE-RESTART snapshot at T1 into pre/
#   3. kubectl delete pod on the operator → waits for rollout-to-Ready
#   4. take a POST-RESTART snapshot at T2 into post/
#   5. run gwb-bench against both and emit SUMMARY.md with the two
#      accuracy ratios and their delta
#
# Acceptance criterion (documented in SUMMARY.md, not auto-enforced): the
# POST-RESTART accuracy should be within ~0.05 of the PRE-RESTART number.
# A larger drop is the interesting finding, not an error — this script
# exists to *measure* the phenomenon, not to gate CI on it.
#
# Knobs are mostly shared with bench.sh; the restart-specific ones:
#   CHAOS_OUT              — output directory
#   CHAOS_PRE_SECONDS      — elapsed at first snapshot (default: 15)
#   CHAOS_POST_SECONDS     — elapsed at second snapshot (default: 45)
#   KIND_CLUSTER           — default: gwb-chaos
#   KEEP_CLUSTER           — 1 keeps it around for inspection

set -euo pipefail

: "${CHAOS_OUT:=chaos-results/$(date +%Y-%m-%d)}"
: "${KIND:=kind}"
: "${KUBECTL:=kubectl}"
: "${KIND_CLUSTER:=gwb-chaos}"
: "${CHAOS_NAMESPACE:=gwb-chaos}"
: "${CHAOS_COUNT:=50}"
: "${CHAOS_RATE:=10}"
: "${CHAOS_RUNTIME:=60s}"
: "${CHAOS_GPUS:=100m}"
: "${CHAOS_GPU_RESOURCE:=cpu}"
: "${CHAOS_PRE_SECONDS:=15}"
: "${CHAOS_POST_SECONDS:=45}"
: "${CHAOS_LABEL:=app=gwb-chaos-worker}"
: "${KEEP_CLUSTER:=0}"
# Non-"latest" tag so kubelet uses the kind-loaded image (IfNotPresent)
# rather than trying to pull from docker.io (the :latest default).
: "${IMG:=controller:chaos}"

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/.." && pwd)"

need() { command -v "$1" >/dev/null 2>&1 || { echo "missing required tool: $1" >&2; exit 1; }; }
need "$KIND"; need "$KUBECTL"; need docker; need go

[[ -x "$root/bin/gwb-workload" ]] || { echo "missing bin/gwb-workload — run 'make workload-generator'" >&2; exit 1; }
[[ -x "$root/bin/gwb-bench" ]] || { echo "missing bin/gwb-bench — run 'make gwb-bench'" >&2; exit 1; }

mkdir -p "$CHAOS_OUT/pre" "$CHAOS_OUT/post"
: > "$CHAOS_OUT/chaos.log"
exec > >(tee -a "$CHAOS_OUT/chaos.log") 2>&1

log() { printf '[chaos %s] %s\n' "$(date +%H:%M:%S)" "$*"; }

cleanup() {
  local rc=$?
  if [[ "$KEEP_CLUSTER" != "1" ]]; then
    log "tearing down kind cluster $KIND_CLUSTER"
    "$KIND" delete cluster --name "$KIND_CLUSTER" >/dev/null 2>&1 || true
  fi
  exit "$rc"
}
trap cleanup EXIT

label_key="${CHAOS_LABEL%=*}"
label_val="${CHAOS_LABEL#*=}"
# Same "100m" → 0.1 translation as bench.sh. Kept in shell because the
# chaos run is agnostic to the unit form: gwb-bench only needs a float.
gpus_float="$(python3 - "$CHAOS_GPUS" <<'PY'
import sys
raw = sys.argv[1]
print(float(raw[:-1]) / 1000.0 if raw.endswith("m") else float(raw))
PY
)"

log "chaos output: $CHAOS_OUT"
log "scenario: count=$CHAOS_COUNT rate=$CHAOS_RATE runtime=$CHAOS_RUNTIME gpus=$CHAOS_GPUS resource=$CHAOS_GPU_RESOURCE"
log "snapshots at t=${CHAOS_PRE_SECONDS}s (pre-restart) and t=${CHAOS_POST_SECONDS}s (post-restart)"

###############################################################################
# 1. Kind cluster
###############################################################################
if "$KIND" get clusters 2>/dev/null | grep -Fxq "$KIND_CLUSTER"; then
  log "kind cluster $KIND_CLUSTER already exists — reusing"
else
  log "creating kind cluster $KIND_CLUSTER"
  "$KIND" create cluster --name "$KIND_CLUSTER"
fi

log "building + loading operator image $IMG"
(cd "$root" && make docker-build IMG="$IMG" >/dev/null)
"$KIND" load docker-image "$IMG" --name "$KIND_CLUSTER" >/dev/null

: "${CERT_MANAGER_VERSION:=v1.15.3}"
log "installing cert-manager $CERT_MANAGER_VERSION"
"$KUBECTL" apply -f "https://github.com/cert-manager/cert-manager/releases/download/${CERT_MANAGER_VERSION}/cert-manager.yaml" >/dev/null
"$KUBECTL" -n cert-manager rollout status deploy/cert-manager --timeout=180s
"$KUBECTL" -n cert-manager rollout status deploy/cert-manager-webhook --timeout=180s
"$KUBECTL" -n cert-manager rollout status deploy/cert-manager-cainjector --timeout=180s

log "installing CRDs + operator"
(cd "$root" && make install deploy IMG="$IMG" >/dev/null)
"$KUBECTL" -n gpu-k8s-operator-system rollout status \
  deploy/gpu-k8s-operator-controller-manager --timeout=180s

###############################################################################
# 2. Namespace + budget
###############################################################################
log "provisioning namespace + budget"
"$KUBECTL" create ns "$CHAOS_NAMESPACE" --dry-run=client -o yaml \
  | "$KUBECTL" apply -f - >/dev/null

cat <<EOF | "$KUBECTL" apply -f - >/dev/null
apiVersion: budget.zxuhan.dev/v1alpha1
kind: GPUWorkloadBudget
metadata:
  name: chaos
  namespace: $CHAOS_NAMESPACE
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
  gpuResourceName: $CHAOS_GPU_RESOURCE
EOF

"$KUBECTL" -n "$CHAOS_NAMESPACE" wait gwb/chaos \
  --for=condition=Ready --timeout=30s || log "budget never reached Ready — continuing"

###############################################################################
# 3. Launch workload + pre-restart snapshot
###############################################################################
start_ts=$(date +%s)
log "launching gwb-workload"
"$root/bin/gwb-workload" \
  --namespace="$CHAOS_NAMESPACE" \
  --label="$CHAOS_LABEL" \
  --count="$CHAOS_COUNT" \
  --rate="$CHAOS_RATE" \
  --runtime="$CHAOS_RUNTIME" \
  --gpus="$CHAOS_GPUS" \
  --gpu-resource="$CHAOS_GPU_RESOURCE"

wait_until() {
  local target=$1
  local now elapsed remaining
  now=$(date +%s)
  elapsed=$(( now - start_ts ))
  remaining=$(( target - elapsed ))
  if (( remaining > 0 )); then
    log "  waiting ${remaining}s to reach t=${target}s"
    sleep "$remaining"
  fi
}

wait_until "$CHAOS_PRE_SECONDS"

log "PRE-RESTART snapshot"
"$KUBECTL" -n "$CHAOS_NAMESPACE" get gwb chaos -o json \
  > "$CHAOS_OUT/pre/status.json"
"$root/bin/gwb-bench" report \
  --count="$CHAOS_COUNT" --rate="$CHAOS_RATE" \
  --runtime="$CHAOS_RUNTIME" --gpus="$gpus_float" \
  --elapsed="${CHAOS_PRE_SECONDS}s" \
  --status-json="$CHAOS_OUT/pre/status.json" \
  --out-dir="$CHAOS_OUT/pre" \
  --label="pre-restart" \
  | sed 's/^/pre: /'

###############################################################################
# 4. Kill the operator pod and wait for rollout
###############################################################################
log "deleting operator pod to simulate crash"
"$KUBECTL" -n gpu-k8s-operator-system delete pod \
  -l control-plane=controller-manager --wait=false >/dev/null || true
"$KUBECTL" -n gpu-k8s-operator-system rollout status \
  deploy/gpu-k8s-operator-controller-manager --timeout=120s

# Give the new instance one reconcile period (~15s — controller's default
# RequeueAfter under normal accounting) so the post-restart snapshot
# isn't artificially low just because we sampled before the first tick.
log "sleeping 15s for post-restart reconcile"
sleep 15

###############################################################################
# 5. Post-restart snapshot
###############################################################################
wait_until "$CHAOS_POST_SECONDS"

log "POST-RESTART snapshot"
"$KUBECTL" -n "$CHAOS_NAMESPACE" get gwb chaos -o json \
  > "$CHAOS_OUT/post/status.json"
"$root/bin/gwb-bench" report \
  --count="$CHAOS_COUNT" --rate="$CHAOS_RATE" \
  --runtime="$CHAOS_RUNTIME" --gpus="$gpus_float" \
  --elapsed="${CHAOS_POST_SECONDS}s" \
  --status-json="$CHAOS_OUT/post/status.json" \
  --out-dir="$CHAOS_OUT/post" \
  --label="post-restart" \
  | sed 's/^/post: /'

###############################################################################
# 6. Summary — inlined rather than adding a `diff` subcommand to gwb-bench,
#    since this script is the only caller that cares about the pre/post pair.
###############################################################################
pre_acc=$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["accuracyRatio"])' "$CHAOS_OUT/pre/results.json")
post_acc=$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["accuracyRatio"])' "$CHAOS_OUT/post/results.json")
pre_delta=$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["deltaGpuHours"])' "$CHAOS_OUT/pre/results.json")
post_delta=$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["deltaGpuHours"])' "$CHAOS_OUT/post/results.json")
accuracy_drop=$(python3 -c "print(float('$pre_acc') - float('$post_acc'))")

cat > "$CHAOS_OUT/SUMMARY.md" <<EOF
# Chaos: Restart Correctness

Generated: $(date -u +%Y-%m-%dT%H:%M:%SZ)

Scenario: ${CHAOS_COUNT} pods at ${CHAOS_RATE}/s, runtime=${CHAOS_RUNTIME},
gpus=${CHAOS_GPUS} ${CHAOS_GPU_RESOURCE} each. Operator pod deleted between
snapshots.

| Phase | Elapsed | Accuracy | Delta (reported − expected) |
|---|---|---|---|
| pre-restart  | ${CHAOS_PRE_SECONDS}s  | ${pre_acc}  | ${pre_delta} |
| post-restart | ${CHAOS_POST_SECONDS}s | ${post_acc} | ${post_delta} |

Accuracy drop across restart: ${accuracy_drop}

The engine is stateless — \`.status.consumedGpuHours\` is recomputed from
the current pod view on every reconcile. On kind with the default
kubelet GC timeout (~60s) this means a restart inside a 30–60s pod
window loses no accounting, and the post-restart accuracy tracks the
pre-restart number within rounding. A larger drop is the signal
operators watch for: it means kubelet GC'd terminated pods while the
operator was down, and those seconds are unrecoverable.
EOF

log "done — see $CHAOS_OUT/SUMMARY.md"
