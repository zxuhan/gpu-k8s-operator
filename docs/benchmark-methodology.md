# Benchmark Methodology

`make bench` produces a single number the README cites: the
**accuracy ratio** of operator-reported GPU-hours against a known-good
expected curve. This doc explains how the curve is derived, why the
bench runs on kind rather than a real cluster, and what the resulting
numbers do and don't claim.

## The expected curve

Given a scenario `(count, rate, runtime, gpus)` — N pods created at R
pods/second, each sleeping for `runtime` seconds and requesting `gpus`
units — the deterministic GPU-hours consumed by wall-clock time `T`
after the first pod is created is:

```
pod i  is created at  t_i = i / rate           (for i ∈ [0, count))
pod i  is running over [t_i, t_i + runtime)
contrib_i(T) = gpus · max(0, min(T, t_i + runtime) − t_i)
expected(T)  = (Σ contrib_i(T)) / 3600
```

This is what `bench.ExpectedGPUHours` in `test/bench/harness.go`
computes. The formula assumes every pod begins consuming the resource
the instant it is created — no image-pull, no scheduling delay, no
kubelet start-up lag. That's intentionally a *lower bound* on the
reported value: the operator's own measurement subtracts exactly
those sources of real-world delay (see
[`accounting-model.md`](./accounting-model.md)), so the comparison
tells you how close the operator's observed consumption is to the
idealised instantaneous-start curve.

## The accuracy ratio

```
accuracy = clamp(reported / expected, 0, 1)
```

Under-reporting by a real operator is the common case and expected —
the ratio is designed to be a lower-bound-biased score. Over-reporting
clamps to `1.0` but is also flagged via the signed `delta =
reported − expected` in `results.json`; a positive delta is the signal
for double-counting.

The delta is the number operators watch for correctness regressions.
The ratio is the number the README shows because it's directly
comparable across scenarios.

## Why kind, and why busybox

Two constraints:

1. The accuracy formula relies on "pod begins consuming the
   instant it is created". Heavier images violate this by spending
   seconds in image-pull before `state.running.startedAt`. On kind
   with `busybox:1.36` the pull happens once per node and subsequent
   pods start within ~100ms of creation — small enough that the
   reported-vs-expected delta measures *accounting precision*, not
   kubelet start-up lag.

2. A kind cluster is reproducible and fits on a laptop. The README
   numbers are claims about the operator, not about a particular
   cloud. Readers with five minutes and Docker can regenerate them;
   readers with a GPU cluster and a budget can substitute their own
   `gpuResourceName` and compare.

On a real GPU cluster the image-pull lag dominates: a
`pytorch/pytorch` image takes 20–60 seconds to pull on first schedule,
which would show up as a ~10–20pp accuracy drop. That's a kubelet
story, not an operator story. The bench runs the control loop it's
measuring, not the scheduler around it.

## What the scenario sweep doesn't cover

- Arrival rates above ~200 pods/second. kind's API server starts
  rejecting Create calls with rate-limits past there; the bench is
  not a control-plane load test.
- Runtimes longer than the kube default kubelet pod-GC timeout
  (roughly 60s). Past that, ended pods disappear before the next
  reconcile and the formula drifts — see
  [`accounting-model.md`](./accounting-model.md) for the bounded-error
  argument.
- Multi-budget interactions. A single bench run touches one budget.
  Cross-budget drift would need a different harness.

## Reproducing

```sh
make bench                    # one scenario into bench-results/YYYY-MM-DD/
make chaos                    # restart-correctness scenario into chaos-results/YYYY-MM-DD/
```

Both write `SUMMARY.md` tables plus a `results.json` with the raw
numbers. `bench.sh` and `chaos.sh` honour env-vars for scenario knobs
(`BENCH_COUNT`, `BENCH_RATE`, …) — see the scripts for the full list.

## What the numbers *don't* claim

- Production performance. A laptop kind cluster is bounded by a
  single Docker daemon; a real control plane has its own latency
  profile.
- GPU-scheduler interactions. We use `resource.Quantity` math, not
  an NVIDIA device plugin. On a real cluster fractional GPU support
  depends on your scheduler.
- Correctness under network partitions between the operator and
  the API server. Phase 7's chaos script only tests operator pod
  deletion, not API-server outages.
