# Benchmark Summary

Generated: 2026-04-20T18:04:49Z

Label: `pre-restart`

## Scenario

| Parameter | Value |
|---|---|
| count | 50 |
| rate (pods/s) | 10 |
| runtime | 1m0s |
| gpus per pod | 0.1 |
| elapsed at snapshot | 15.0s |

## Result

| Metric | Value |
|---|---|
| reported GPU-hours | 0.012000 |
| expected GPU-hours | 0.017431 |
| delta (reported − expected) | -0.005431 |
| accuracy ratio | 0.6884 |
| tracked pods | 50 |

A negative delta is the common case on kind: kubelet start-up lag means the controller observes a pod a fraction of a second after create-time. See docs/benchmark-methodology.md for the accuracy model.
