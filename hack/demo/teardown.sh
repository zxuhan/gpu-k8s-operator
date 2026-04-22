#!/usr/bin/env bash
# README demo teardown. Kills the Grafana port-forward from setup.sh,
# then deletes the kind cluster.

set -euo pipefail

: "${KIND:=kind}"
: "${DEMO_KIND_CLUSTER:=gwb-demo}"

if [ -f /tmp/gwb-demo-pf.pid ]; then
  pid=$(cat /tmp/gwb-demo-pf.pid || true)
  if [ -n "${pid:-}" ] && kill -0 "$pid" 2>/dev/null; then
    kill "$pid" 2>/dev/null || true
  fi
  rm -f /tmp/gwb-demo-pf.pid /tmp/gwb-demo-pf.log
fi
pkill -f "port-forward.*grafana" 2>/dev/null || true

if "$KIND" get clusters 2>/dev/null | grep -Fxq "$DEMO_KIND_CLUSTER"; then
  "$KIND" delete cluster --name "$DEMO_KIND_CLUSTER"
else
  echo "kind cluster $DEMO_KIND_CLUSTER not found, nothing to do"
fi
