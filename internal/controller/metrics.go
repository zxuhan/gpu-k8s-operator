/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

// The four custom metrics are deliberately kept thin: one gauge per
// status field the reconciler already computes, plus one counter the
// enforcement loop will drive in Phase 4. Everything else — reconcile
// latency, queue depth, workqueue retries — comes free from
// controller-runtime's default registry.
//
// The accuracy_ratio gauge is registered here but left unpopulated
// until Phase 6/7, where the bench harness records the ratio of
// accounted-for vs. ground-truth GPU-seconds under chaos. Exposing the
// series at zero from day one keeps dashboards buildable before the
// data is real.
var (
	consumedGPUHours = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gwb_consumed_gpu_hours",
			Help: "GPU-hours counted against the budget in the current rolling window.",
		},
		[]string{"namespace", "name"},
	)

	remainingGPUHours = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gwb_remaining_gpu_hours",
			Help: "GPU-hours headroom before the budget is exhausted (floored at zero).",
		},
		[]string{"namespace", "name"},
	)

	trackedPods = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gwb_tracked_pods",
			Help: "Number of pods currently matched by the budget's selector.",
		},
		[]string{"namespace", "name"},
	)

	enforcementActionsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gwb_enforcement_actions_total",
			Help: "Cumulative enforcement actions the operator has taken, by action.",
		},
		[]string{"namespace", "name", "action"},
	)

	accountingAccuracyRatio = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gwb_accounting_accuracy_ratio",
			Help: "Observed accounted GPU-seconds / ground-truth GPU-seconds. Populated by the bench harness in Phase 6/7.",
		},
		[]string{"namespace", "name"},
	)
)

func init() {
	metrics.Registry.MustRegister(
		consumedGPUHours,
		remainingGPUHours,
		trackedPods,
		enforcementActionsTotal,
		accountingAccuracyRatio,
	)
}

// clearBudgetMetrics wipes the per-budget series for the given namespace
// and name. Called on deletion so a recreate or a rename doesn't leave
// orphan series in the scrape.
func clearBudgetMetrics(namespace, name string) {
	consumedGPUHours.DeleteLabelValues(namespace, name)
	remainingGPUHours.DeleteLabelValues(namespace, name)
	trackedPods.DeleteLabelValues(namespace, name)
	accountingAccuracyRatio.DeleteLabelValues(namespace, name)
	// enforcementActionsTotal is intentionally left — a Counter reset on
	// delete would hide a crash-loop scenario (budget recreated every few
	// minutes, enforcement firing each cycle) from alerts that key off
	// rate().
}
