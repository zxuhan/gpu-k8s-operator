#!/usr/bin/env bash
# Benchmark harness entrypoint. Wired up in Phase 6.
# Phases 0–5 run without touching this script; it deliberately fails
# loudly so any accidental early invocation is obvious.
set -euo pipefail

echo "hack/bench.sh: benchmark suite is wired up in Phase 6." >&2
echo "See docs/benchmark-methodology.md once that phase lands." >&2
exit 64
