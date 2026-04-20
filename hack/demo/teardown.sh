#!/usr/bin/env bash
# README demo teardown — deletes the kind cluster created by setup.sh.
# Keep this trivial on purpose; the cluster is the only artifact worth
# cleaning up. Everything else lives inside it.

set -euo pipefail

: "${KIND:=kind}"
: "${DEMO_KIND_CLUSTER:=gwb-demo}"

if "$KIND" get clusters 2>/dev/null | grep -Fxq "$DEMO_KIND_CLUSTER"; then
  "$KIND" delete cluster --name "$DEMO_KIND_CLUSTER"
else
  echo "kind cluster $DEMO_KIND_CLUSTER not found — nothing to do"
fi
