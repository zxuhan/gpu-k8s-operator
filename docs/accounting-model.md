# Accounting Model

This doc describes what the operator counts, over what window, and what
it guarantees across an operator restart. Read it when a number in
`.status` looks wrong and you want to know whether that's a bug or a
documented-bounded error.

## What gets counted

For each budget, the selector picks a set of pods. For each pod we
compute a contribution to `consumedGpuHours`:

```
contrib = resource_request · min(pod.End, now) − max(pod.Start, now − window)
```

clamped at zero when the pod's `[Start, End)` interval doesn't overlap
the observation window. The unit is GPU-hours; `resource_request` is the
pod-spec value for `spec.gpuResourceName` (default: `nvidia.com/gpu`).

`pod.Start` is the *earliest container* `state.running.startedAt` —
not pod creation time, not pod scheduled time. This deliberately
excludes image-pull and scheduling slop from the quota. A pod that
image-pulls for 30s does *not* consume 30 pod-seconds of quota.

`pod.End` is the *latest container* `state.terminated.finishedAt` for
pods in `Succeeded` or `Failed` phase. For `Running` pods `End` is
`nil`, meaning "still consuming at `now`".

A pod the kubelet has already GC'd from the API server has contributed
zero from the operator's point of view — by definition, we can't see
it. The post-restart recovery story (below) is built around this.

## Fractional / simulated GPUs

When the cluster has no `nvidia.com/gpu` resource (the common kind
case), set `spec.gpuResourceName: cpu` on the budget and use small
fractional CPU requests (e.g. `100m`) to drive the same control loop
without a real accelerator. The engine treats `resource.Quantity` as
a float, so `500m` requests over 1 hour add `0.5` GPU-hours to the
budget. Same code path, same tests, same control loop.

This is why the e2e suite and the bench harness both default to
`gpuResourceName: cpu` — they exercise the full accounting + enforcement
stack on a GPU-less cluster.

## Recovery model

The accounting engine (`internal/accounting/accounting.go`) is
**stateless**: every reconcile recomputes `consumedGpuHours` from the
current API-server view. There is no in-memory cache to lose on a
restart, and the CR's `.status.consumedGpuHours` is overwritten by the
next reconcile, not additively accumulated.

Restart scenarios:

- **Operator down < kubelet GC window (~60s by default):** no loss.
  Every pod that was accounted before the restart is still visible
  after it, with its original `startedAt` and `finishedAt`, so the
  recomputed consumption is identical.

- **Operator down > kubelet GC window:** pods that *terminated and
  were GC'd* during the downtime contribute zero post-restart. Pods
  that terminated but are still present in the API server (and
  Running pods) are unaffected.

The error introduced by the second scenario is bounded by
`(GPUs_of_lost_pods × their_runtime)`. For a cluster with 10 pods/s
turnover and 30-second pod lifetimes, a 5-minute operator outage
could lose up to ~90 GPU-pod-minutes of accounting — visible as a
sudden dip in the reported-vs-expected ratio.

`hack/chaos.sh` measures this empirically: it takes a pre-restart
snapshot, kills the operator pod, takes a post-restart snapshot, and
writes the accuracy delta into `chaos-results/YYYY-MM-DD/SUMMARY.md`.

## What isn't counted

- Time a pod spends `Pending` (unscheduled or image-pulling).
- Init-container time (per controller-runtime convention: Start is
  the earliest *regular* container's `startedAt`).
- Time the pod's containers are `Waiting` due to CrashLoopBackOff —
  `Running → Terminated` only.
- Multi-cluster aggregation. A budget is namespace-scoped; federation
  across clusters is out of scope.

If any of these matter for your accounting contract, say so in an
issue — they're design decisions, not oversights, and changing them
changes the numeric contract with existing users.
