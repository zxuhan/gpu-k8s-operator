# gpu-k8s-operator

A Kubernetes operator that tracks cumulative GPU-hour consumption for
pods matching a label selector against a quota over a rolling window,
and enforces that quota via pod eviction, pause annotations, or alerts.

The operator is built around the `GPUWorkloadBudget` CRD
(group `budget.zxuhan.dev`, version `v1alpha1`). Accounting state is
persisted in the CR status so it is reconstructable after operator
restart. When the `nvidia.com/gpu` extended resource is absent, the
accounting engine falls back to scaled CPU-second counting — the
control loop is identical either way, which is the point.

> **Status:** early development. This README is a placeholder until
> the project lands Phase 9 (full documentation). See the `docs/`
> directory for architecture notes and `bench-results/` for measured
> performance numbers once the benchmark suite runs (Phase 6+).

## Repository layout

```
api/                      CRD types (added in Phase 1)
cmd/main.go               Manager entry point
config/                   Generated CRD, RBAC, webhook, manager manifests
internal/accounting/      Pure-Go budget math (added in Phase 2)
internal/controller/      Reconciler (added in Phase 3)
internal/enforcement/     Eviction / pause / alert (added in Phase 4)
internal/webhook/         Validating webhook (added in Phase 1)
test/e2e/                 Ginkgo e2e against a kind cluster
test/bench/               Benchmark harness (Phase 6)
test/workload-generator/  Synthetic workload generator (Phase 6)
hack/                     Benchmark + chaos scripts
deploy/helm/              Helm chart (Phase 8)
deploy/aks/               Bicep + GitHub Actions for AKS (Phase 10)
docs/                     Architecture, accounting model, benchmark methodology
```

## Prerequisites

- Go 1.25+
- Docker 17.03+
- kubectl compatible with the target cluster
- kind (for local e2e and benchmarks)

## Common commands

| Target             | What it does                                         |
| ------------------ | ---------------------------------------------------- |
| `make build`       | Build the `gwb-operator` binary into `bin/`.         |
| `make run`         | Run the manager against your current kubeconfig.     |
| `make test`        | Run unit + envtest suites with coverage.             |
| `make test-e2e`    | Spin up a kind cluster and run Ginkgo e2e tests.     |
| `make lint`        | Run golangci-lint v2.                                |
| `make manifests`   | Regenerate CRDs, RBAC, and webhook manifests.        |
| `make bench`       | Run the full benchmark suite (wired up in Phase 6).  |
| `make install`     | Apply CRDs to the currently targeted cluster.        |
| `make deploy`      | Deploy the manager to the currently targeted cluster. |

## Deploying to a cluster

Build and push an image, then install:

```sh
make docker-build docker-push IMG=<registry>/gpu-k8s-operator:<tag>
make install
make deploy IMG=<registry>/gpu-k8s-operator:<tag>
```

Remove with `make undeploy` and `make uninstall`.

## License

Apache 2.0. See `LICENSE` once it's added in Phase 9.
