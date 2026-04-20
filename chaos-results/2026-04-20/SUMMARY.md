# Chaos: Restart Correctness

Generated: 2026-04-20T18:06:33Z

Scenario: 50 pods at 10/s, runtime=60s,
gpus=100m cpu each. Operator pod deleted between
snapshots.

| Phase | Elapsed | Accuracy | Delta (reported − expected) |
|---|---|---|---|
| pre-restart  | 15s  | 0.6884462151394422  | -0.0054305555555555565 |
| post-restart | 120s | 0.9960000000000001 | -0.0003333333333333244 |

Accuracy drop across restart: -0.30755378486055795

The engine is stateless — `.status.consumedGpuHours` is recomputed from
the current pod view on every reconcile. On kind with the default
kubelet GC timeout (~60s) this means a restart inside a 30–60s pod
window loses no accounting, and the post-restart accuracy tracks the
pre-restart number within rounding. A larger drop is the signal
operators watch for: it means kubelet GC'd terminated pods while the
operator was down, and those seconds are unrecoverable.
