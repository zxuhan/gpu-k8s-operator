#!/usr/bin/env bash
# Tear down whatever hack/bench-stack/install.sh put up.
# Destructive — the namespaces go with it. Fails soft so it's safe to
# re-run against a half-installed stack.
set -euo pipefail

: "${HELM:=helm}"
: "${KUBECTL:=kubectl}"
: "${MONITORING_NS:=monitoring}"
: "${OPERATOR_NS:=gpu-k8s-operator-system}"
: "${KPS_RELEASE:=kps}"
: "${OPERATOR_RELEASE:=gwb-operator}"

"$HELM" -n "$OPERATOR_NS" uninstall "$OPERATOR_RELEASE" 2>/dev/null || true
"$HELM" -n "$MONITORING_NS" uninstall "$KPS_RELEASE" 2>/dev/null || true
"$HELM" -n cert-manager uninstall cert-manager 2>/dev/null || true
"$KUBECTL" delete ns "$OPERATOR_NS" --ignore-not-found
"$KUBECTL" delete ns "$MONITORING_NS" --ignore-not-found
"$KUBECTL" delete ns cert-manager --ignore-not-found
