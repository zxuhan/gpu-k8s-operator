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

// Package accounting computes GPU-hour consumption for a set of pods over
// a rolling time window. It is deliberately k8s-free so the math can be
// unit-tested in isolation and swapped behind a different observation
// source (a file replay, a mock scheduler) in benchmarks.
//
// Recovery model: the engine is stateless. On operator restart the
// controller re-derives consumption from the current view of the API
// server (running pods + terminated pods still present before kubelet
// GC). Pods that were GC'd while the operator was down contribute zero
// to the post-restart count — see docs/accounting-model.md for the
// bounded-error argument.
package accounting

import "time"

// Pod is the minimum information the engine needs about one workload.
// Times are UTC; mixing time zones silently gives wrong answers.
//
// GPUs is a float to support the CPU-second simulation fallback
// (docs/accounting-model.md): a pod requesting 8 CPU-seconds with a
// scaling factor of 0.001 GPU-per-CPU-second-per-second contributes
// the same as a pod requesting 0.008 GPU for its lifetime. The
// control-loop doesn't care which one it sees.
type Pod struct {
	// ID is a stable, unique identifier. The controller feeds pod UIDs;
	// tests feed whatever is convenient. Duplicates are allowed — the
	// engine sums — but the caller usually wants to dedupe upstream.
	ID string
	// Start is when the pod began consuming GPU. For a k8s Pod this is
	// the earliest container StartedAt (or pod.status.startTime as a
	// fallback). Must be in UTC.
	Start time.Time
	// End is when the pod stopped. Nil means "still running at the
	// observation instant". Must be in UTC.
	End *time.Time
	// GPUs is the device count held during [Start, End). Fractional is
	// allowed for the CPU-sim mode.
	GPUs float64
}

// Budget describes the rolling-window quota.
type Budget struct {
	// Window is the rolling-window length. Non-positive windows are
	// rejected at the webhook, so the engine treats 0/negative Window
	// as "everything from the start of time" by clamping From to the
	// zero time.
	Window time.Duration
	// Quota is the maximum GPU-hours permitted inside Window. Values
	// <= 0 are rejected at the webhook; the engine still produces a
	// correct Result (Over will be true the moment Consumed > 0).
	Quota float64
}

// Result is the output of Compute. It maps 1:1 onto the fields of
// GPUWorkloadBudgetStatus the reconciler writes back, minus the
// resource.Quantity conversion.
type Result struct {
	// Consumed is the total GPU-hours inside the window.
	Consumed float64
	// Remaining is max(Quota - Consumed, 0) — never negative so the
	// status field is always a meaningful "headroom" number.
	Remaining float64
	// TrackedPods is the number of Pod entries the engine considered
	// (not just those that contributed > 0). Zero means the selector
	// matched nothing — useful signal for an operator diagnosing
	// "my budget shows 0 but I have running GPU pods".
	TrackedPods int
	// Over is true when Consumed is at or above Quota. Strict equality
	// flips the flag so a quota-of-100 with consumed-of-exactly-100
	// triggers enforcement on the next reconcile, matching the
	// principle of least surprise for operators writing alerts.
	Over bool
}

// WindowUsage returns the GPU-hours pod contributed during the half-open
// interval [from, to). Any of:
//   - from not strictly before to (zero or negative-length window),
//   - pod.End before pod.Start (clock skew; kubelet clock < controller clock),
//   - pod entirely before from or entirely at/after to,
//
// produces 0 rather than a negative number. Negative contributions would
// silently cancel real consumption and are never what the operator wants.
func WindowUsage(pod Pod, from, to time.Time) float64 {
	if !from.Before(to) {
		return 0
	}
	if pod.End != nil && pod.End.Before(pod.Start) {
		return 0
	}
	effStart := pod.Start
	if effStart.Before(from) {
		effStart = from
	}
	effEnd := to
	if pod.End != nil && pod.End.Before(effEnd) {
		effEnd = *pod.End
	}
	if !effStart.Before(effEnd) {
		return 0
	}
	if pod.GPUs <= 0 {
		return 0
	}
	return pod.GPUs * effEnd.Sub(effStart).Hours()
}

// Compute evaluates the budget as of now against the given pods.
//
// The window is [now - b.Window, now). Callers should pass every pod
// matched by the selector, including recently-terminated ones the API
// server still knows about — the more complete the input, the smaller
// the post-restart accounting gap.
func (b Budget) Compute(now time.Time, pods []Pod) Result {
	from := now.Add(-b.Window)
	if b.Window <= 0 {
		from = time.Time{}
	}
	var total float64
	for _, p := range pods {
		total += WindowUsage(p, from, now)
	}
	remaining := b.Quota - total
	if remaining < 0 {
		remaining = 0
	}
	return Result{
		Consumed:    total,
		Remaining:   remaining,
		TrackedPods: len(pods),
		Over:        total >= b.Quota,
	}
}
