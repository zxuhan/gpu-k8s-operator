#!/usr/bin/env bash
# README demo setup. Brings a kind cluster to the pre-demo state:
#   - operator built, loaded, Helm-deployed with ServiceMonitor
#   - kube-prometheus-stack installed (scrapes operator metrics)
#   - Grafana dashboard applied via ConfigMap sidecar
#   - demo namespace with an AlertOnly GPUWorkloadBudget applied
#   - Grafana port-forwarded on localhost:3000 so record.mjs can capture it
#   - no workload yet; orchestrate.sh launches it during the recording
#
# Idempotent: re-running against an existing cluster only re-applies the
# manifests.
#
# Knobs:
#   DEMO_KIND_CLUSTER   default: gwb-demo
#   DEMO_NAMESPACE      default: demo
#   MONITORING_NS       default: monitoring
#   KPS_RELEASE         default: kps
#   IMG                 default: controller:demo

set -euo pipefail

: "${KIND:=kind}"
: "${KUBECTL:=kubectl}"
: "${HELM:=helm}"
: "${DEMO_KIND_CLUSTER:=gwb-demo}"
: "${DEMO_NAMESPACE:=demo}"
: "${MONITORING_NS:=monitoring}"
: "${KPS_RELEASE:=kps}"
: "${OPERATOR_NS:=gpu-k8s-operator-system}"
: "${OPERATOR_RELEASE:=gwb-operator}"
: "${IMG:=controller:demo}"

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/../.." && pwd)"

need() { command -v "$1" >/dev/null 2>&1 || { echo "missing required tool: $1" >&2; exit 1; }; }
need "$KIND"; need "$KUBECTL"; need "$HELM"; need docker; need go
need node; need ffmpeg; need curl

log() { printf '[demo-setup %s] %s\n' "$(date +%H:%M:%S)" "$*"; }

# 1. Kind cluster
if "$KIND" get clusters 2>/dev/null | grep -Fxq "$DEMO_KIND_CLUSTER"; then
  log "kind cluster $DEMO_KIND_CLUSTER exists, reusing"
else
  log "creating kind cluster $DEMO_KIND_CLUSTER"
  "$KIND" create cluster --name "$DEMO_KIND_CLUSTER"
fi

# 2. Build + load operator image
log "building + loading operator image $IMG"
(cd "$root" && make docker-build IMG="$IMG" >/dev/null)
"$KIND" load docker-image "$IMG" --name "$DEMO_KIND_CLUSTER" >/dev/null

# 3. Helm repos
"$HELM" repo add prometheus-community https://prometheus-community.github.io/helm-charts >/dev/null 2>&1 || true
"$HELM" repo add jetstack https://charts.jetstack.io >/dev/null 2>&1 || true
"$HELM" repo update >/dev/null

# 4. cert-manager (webhook dependency)
log "installing cert-manager"
"$HELM" upgrade --install cert-manager jetstack/cert-manager \
  --namespace cert-manager --create-namespace \
  --set crds.enabled=true --wait >/dev/null

# 5. kube-prometheus-stack (Prometheus + Grafana + sidecar). Pin the
# Grafana admin password so record.mjs can log in with a known value;
# the cluster is ephemeral and local-only so this is fine for the demo.
log "installing kube-prometheus-stack ($KPS_RELEASE)"
"$HELM" upgrade --install "$KPS_RELEASE" prometheus-community/kube-prometheus-stack \
  --namespace "$MONITORING_NS" --create-namespace \
  --set prometheus.prometheusSpec.scrapeInterval=5s \
  --set prometheus.prometheusSpec.serviceMonitorSelectorNilUsesHelmValues=false \
  --set grafana.defaultDashboardsEnabled=false \
  --set grafana.sidecar.dashboards.enabled=true \
  --set grafana.sidecar.dashboards.searchNamespace=ALL \
  --set grafana.adminPassword=prom-operator \
  --wait >/dev/null

# 6. Operator Helm chart with ServiceMonitor labelled for kps Prometheus
log "installing operator Helm chart with ServiceMonitor"
"$HELM" upgrade --install "$OPERATOR_RELEASE" "$root/deploy/helm/gwb-operator" \
  --namespace "$OPERATOR_NS" --create-namespace \
  --set fullnameOverride="$OPERATOR_RELEASE" \
  --set image.repository=controller \
  --set image.tag=demo \
  --set image.pullPolicy=IfNotPresent \
  --set metrics.serviceMonitor.enabled=true \
  --set "metrics.serviceMonitor.labels.release=$KPS_RELEASE" \
  --wait >/dev/null

"$KUBECTL" -n "$OPERATOR_NS" rollout status \
  "deploy/$OPERATOR_RELEASE" --timeout=180s

# 6b. The operator's /metrics endpoint requires callers to have the
# 'get /metrics' non-resource URL right. The chart binds that to the
# operator's own SA (for TokenReview). Prometheus scrapes with its own
# SA, so we add a cluster-scoped binding from that SA to a fresh
# metrics-reader ClusterRole.
cat <<EOF | "$KUBECTL" apply -f - >/dev/null
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: gwb-operator-metrics-reader
rules:
- nonResourceURLs: ["/metrics"]
  verbs: ["get"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: gwb-operator-metrics-reader
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: gwb-operator-metrics-reader
subjects:
- kind: ServiceAccount
  name: ${KPS_RELEASE}-kube-prometheus-stack-prometheus
  namespace: ${MONITORING_NS}
EOF

# 7. Dashboard ConfigMap (sidecar auto-loads when label grafana_dashboard=1)
log "installing Grafana dashboard ConfigMap"
dash_body=$(sed 's/^/    /' "$here/dashboard.json")
cat <<EOF | "$KUBECTL" apply -f -
apiVersion: v1
kind: ConfigMap
metadata:
  name: gwb-demo-dashboard
  namespace: $MONITORING_NS
  labels:
    grafana_dashboard: "1"
data:
  gwb-demo.json: |
$dash_body
EOF

# 8. Demo namespace + AlertOnly budget.
# Quota ≈ 1.8 CPU-seconds so the workload crosses it inside the demo
# window, but AlertOnly keeps the pods running so Scene B (operator kill)
# has a live workload to re-track.
log "creating namespace $DEMO_NAMESPACE + demo budget"
"$KUBECTL" create ns "$DEMO_NAMESPACE" --dry-run=client -o yaml \
  | "$KUBECTL" apply -f - >/dev/null
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
    log "budget applied (attempt $i)"; break
  fi
  (( i == 10 )) && { log "budget apply failed"; exit 1; }
  sleep 3
done
"$KUBECTL" -n "$DEMO_NAMESPACE" wait gwb/demo \
  --for=condition=Ready --timeout=30s || log "budget never reached Ready, continuing"

# 9. Build the workload generator
(cd "$root" && make workload-generator >/dev/null)

# 9b. Playwright (for record.mjs). Install into hack/demo/node_modules and
# fetch the chromium binary once. Skip if already present.
if [ ! -d "$here/node_modules/playwright" ]; then
  log "installing playwright into hack/demo/node_modules"
  (cd "$here" && npm install --silent --no-audit --no-fund)
fi
if ! (cd "$here" && npx --no playwright install --dry-run chromium 2>/dev/null | grep -q "is already installed"); then
  log "downloading chromium for playwright (~150MB, one-time)"
  (cd "$here" && npx --no playwright install chromium)
fi

# 10. Port-forward Grafana. Stash PID so teardown can clean it up.
log "port-forwarding Grafana on :3000"
pkill -f "port-forward.*$KPS_RELEASE-grafana" 2>/dev/null || true
"$KUBECTL" -n "$MONITORING_NS" port-forward "svc/$KPS_RELEASE-grafana" 3000:80 \
  >/tmp/gwb-demo-pf.log 2>&1 &
echo $! > /tmp/gwb-demo-pf.pid

for i in $(seq 1 30); do
  if curl -sf -o /dev/null "http://localhost:3000/login"; then
    log "Grafana ready on :3000"; break
  fi
  (( i == 30 )) && { log "Grafana never answered on :3000"; exit 1; }
  sleep 2
done

# Give the dashboards sidecar a moment to pick up the ConfigMap.
sleep 15

log "ready, run 'node hack/demo/record.mjs' next"
