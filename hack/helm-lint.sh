#!/usr/bin/env bash
# Thin wrapper around `helm lint` + `helm template` against the chart.
# Kept as a separate script (not a make target) so it can be wired into
# CI without Helm becoming a required build-time dependency for the Go
# side of the project.
set -euo pipefail

: "${HELM:=helm}"
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/.." && pwd)"
chart="$root/deploy/helm/gwb-operator"

command -v "$HELM" >/dev/null 2>&1 || {
  echo "helm not found. Install from https://helm.sh/docs/intro/install/" >&2
  exit 1
}

echo "==> helm lint $chart"
"$HELM" lint "$chart"

echo "==> helm template $chart (default values)"
"$HELM" template gwb-operator "$chart" --namespace gpu-k8s-operator-system > /dev/null

echo "==> helm template $chart (servicemonitor on, webhook off)"
"$HELM" template gwb-operator "$chart" \
  --namespace gpu-k8s-operator-system \
  --set metrics.serviceMonitor.enabled=true \
  --set webhook.enabled=false > /dev/null

echo "OK"
