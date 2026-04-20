#!/usr/bin/env bash
# Install a minimal prometheus + grafana stack alongside gwb-operator so
# the custom metrics (`gwb_consumed_gpu_hours`, `gwb_remaining_gpu_hours`,
# etc.) can be scraped and eyeballed during a bench run.
#
# Intentionally thin: installs kube-prometheus-stack via helm with the
# upstream defaults, then reinstalls the operator chart with the
# ServiceMonitor enabled and labelled to match the kps Prometheus's
# serviceMonitorSelector (`release=kps` by default on kube-prometheus-stack).
#
# Assumes the cluster exists and kubectl/helm are configured against it.
# Run against the same kind cluster hack/bench.sh brought up (export
# KUBECONFIG first if needed).
#
# Usage:
#   hack/bench-stack/install.sh           # install stack + operator
#   KEEP=1 hack/bench-stack/install.sh    # don't uninstall on re-run

set -euo pipefail

: "${HELM:=helm}"
: "${KUBECTL:=kubectl}"
: "${MONITORING_NS:=monitoring}"
: "${OPERATOR_NS:=gpu-k8s-operator-system}"
: "${KPS_RELEASE:=kps}"
: "${OPERATOR_RELEASE:=gwb-operator}"

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/../.." && pwd)"
chart="$root/deploy/helm/gwb-operator"

command -v "$HELM" >/dev/null 2>&1 || { echo "helm not found; install from https://helm.sh/docs/intro/install/" >&2; exit 1; }
command -v "$KUBECTL" >/dev/null 2>&1 || { echo "kubectl not found" >&2; exit 1; }

"$HELM" repo add prometheus-community https://prometheus-community.github.io/helm-charts >/dev/null 2>&1 || true
"$HELM" repo add jetstack https://charts.jetstack.io >/dev/null 2>&1 || true
"$HELM" repo update >/dev/null

# cert-manager is a hard prerequisite — the operator's webhook cert is
# provisioned by it. Reinstall idempotently so this script is safe to
# re-run.
"$HELM" upgrade --install cert-manager jetstack/cert-manager \
  --namespace cert-manager --create-namespace \
  --set crds.enabled=true

"$HELM" upgrade --install "$KPS_RELEASE" prometheus-community/kube-prometheus-stack \
  --namespace "$MONITORING_NS" --create-namespace \
  --set prometheus.prometheusSpec.scrapeInterval=5s \
  --set prometheus.prometheusSpec.serviceMonitorSelectorNilUsesHelmValues=false \
  --set grafana.defaultDashboardsEnabled=false

"$HELM" upgrade --install "$OPERATOR_RELEASE" "$chart" \
  --namespace "$OPERATOR_NS" --create-namespace \
  --set metrics.serviceMonitor.enabled=true \
  --set "metrics.serviceMonitor.labels.release=$KPS_RELEASE"

cat <<EOF
done.

  # wait for the operator
  kubectl -n $OPERATOR_NS rollout status deploy/$OPERATOR_RELEASE

  # port-forward Prometheus:
  kubectl -n $MONITORING_NS port-forward svc/$KPS_RELEASE-kube-prometh-prometheus 9090

  # port-forward Grafana (default admin/prom-operator):
  kubectl -n $MONITORING_NS port-forward svc/$KPS_RELEASE-grafana 3000:80
EOF
