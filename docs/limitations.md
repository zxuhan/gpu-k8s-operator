# Known Limitations

These are real limitations discovered while building and benchmarking
the operator — not aspirational TODOs. Each one is tagged with where
it bites and the phase during which it was confirmed.

## Accounting precision

- **Reconcile cadence floor.** `consumedGpuHours` is recomputed every
  ~15s under normal operation (sooner when a pod event triggers a
  reconcile). A budget observed between ticks sees values up to 15s
  stale. For quotas measured in GPU-hours this is noise, but alerts
  with sub-minute thresholds will see step-wise updates, not a smooth
  curve. *Phase 3.*

- **Milli-hour quantisation of `.status`.** The `consumedGpuHours`
  field is a `resource.Quantity`, which canonicalises to the nearest
  milli-unit (0.001 GPU-hours = 3.6 seconds). Reads below that
  resolution are rounded. Prometheus metrics export the float
  directly and are unaffected. *Phase 2.*

- **Kubelet-GC'd pods don't count.** Once the API server has dropped
  a terminated pod, its contribution is permanently zero. During
  normal operation this is bounded by the kubelet's GC timeout
  (~60s by default) and overlaps entirely with our reconcile
  cadence, so the loss is negligible. It only becomes visible
  during long operator outages — see
  [`accounting-model.md`](./accounting-model.md). *Phase 2.*

## Enforcement

- **Eviction respects PodDisruptionBudgets.** The operator submits a
  `policy/v1.Eviction`, which the API server rejects with 429 if a
  matching PDB would be violated. The controller surfaces the
  rejection as an event but does *not* retry aggressively — by
  design. An over-quota workload protected by a zero-disruption PDB
  will stay running until the PDB changes or the pods finish
  naturally. *Phase 4.*

- **Pause is annotation-only.** The `Pause` enforcement mode writes
  an annotation that downstream controllers (e.g. your job operator)
  must honour. Without a cooperating controller the pod keeps
  running and the budget keeps climbing. The annotation key is
  documented in `internal/enforcement/`. *Phase 4.*

- **Grace period is wall-clock, not reconcile-count.** A
  `gracePeriodSeconds` of 10 means enforcement fires 10s after
  `.status.quotaExceeded` flips true, measured on the operator's
  clock. If the operator pod is restarted during the grace window,
  the replacement resets the timer from the moment it first observes
  the over-quota condition. A determined workload can avoid
  enforcement by crash-looping the operator — out of scope. *Phase 4.*

## Webhook

- **Validating only.** Invalid fields get rejected with a clear
  message, but no defaults are auto-populated beyond what the CRD
  schema's `+kubebuilder:default` provides. If you want
  `gpuResourceName: nvidia.com/gpu` to appear in YAML you pulled
  with `kubectl get`, write it explicitly. *Phase 1.*

- **Not fail-safe on webhook outage.** `failurePolicy: Fail` is set,
  meaning if the webhook is unreachable the API server rejects new
  GWB creates/updates with a 500. For a cluster where the webhook
  is intermittently down this is surprising. Flip to `Ignore` via
  the chart only if you accept temporarily admitting invalid GWBs.
  *Phase 1.*

## Benchmarks

- **Kind, not production.** Every README number comes from a kind
  cluster with busybox sleepers, `gpuResourceName: cpu`, and
  fractional CPU requests. The accounting code is cluster-agnostic,
  but the *numbers* are not a claim about NVIDIA device-plugin
  behaviour or the GPU scheduler. *Phase 6.*

- **Single-budget scenarios only.** `make bench` targets one budget
  at a time. Selector-overlap between two budgets is supported by
  the code (each budget recomputes independently) but isn't
  exercised by the bench. If you run overlapping selectors with
  different quotas and the numbers look wrong, open an issue with
  your setup. *Phase 6.*

- **No API-server chaos.** `hack/chaos.sh` tests operator pod
  deletion. It does *not* test API-server partitions, etcd latency
  spikes, or webhook certificate expiry. Those are real failure
  modes; they're just not measured here. *Phase 7.*

## Platform

- **Linux amd64/arm64 only for the controller image.** `make
  docker-buildx` supports more platforms but the pushed images
  published alongside the helm chart target just those two. The
  workload generator and bench CLI build on any platform Go does.

## Honest status

- **No long-running cluster proof.** The operator has been exercised
  over tens-of-minutes benches and chaos scenarios; it has not been
  deployed continuously for weeks against production workloads. Leak
  behaviour of the accounting engine is bounded by its statelessness
  (no in-memory growth possible per-reconcile), but this is a
  theoretical argument, not an observed one.
