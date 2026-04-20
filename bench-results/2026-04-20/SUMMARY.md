# Benchmark Summary

Generated: 2026-04-20T06:57:13Z

Label: `count=50 rate=10 runtime=30s gpus=100m resource=cpu`

## Scenario

| Parameter | Value |
|---|---|
| count | 50 |
| rate (pods/s) | 10 |
| runtime | 30s |
| gpus per pod | 0.1 |
| elapsed at snapshot | 45.0s |

## Result

| Metric | Value |
|---|---|
| reported GPU-hours | 0.040000 |
| expected GPU-hours | 0.041667 |
| delta (reported − expected) | -0.001667 |
| accuracy ratio | 0.9600 |
| tracked pods | 50 |

A negative delta is the common case on kind: kubelet start-up lag means the controller observes a pod a fraction of a second after create-time. See docs/benchmark-methodology.md for the accuracy model.
