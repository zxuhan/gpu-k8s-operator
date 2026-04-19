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

package accounting

import (
	"math"
	"testing"
	"time"
)

// t0 is the notional "now" all tests compute against. Chosen arbitrarily
// but fixed so tests are deterministic and easy to reason about.
var t0 = time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC)

// ptr returns a pointer to its argument — test-only helper for Pod.End.
func ptr[T any](v T) *T { return &v }

// approx compares floats to within 1e-9 GPU-hours (≈3.6 microseconds of
// 1-GPU work). More than enough precision; less fragile than ==.
func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// ---- WindowUsage ----

func TestWindowUsage(t *testing.T) {
	hour := time.Hour

	tests := []struct {
		name      string
		pod       Pod
		from, to  time.Time
		wantHours float64
	}{
		{
			name:      "entirely inside window, still running",
			pod:       Pod{Start: t0.Add(-2 * hour), End: nil, GPUs: 1},
			from:      t0.Add(-24 * hour),
			to:        t0,
			wantHours: 2,
		},
		{
			name:      "entirely inside window, already ended",
			pod:       Pod{Start: t0.Add(-5 * hour), End: ptr(t0.Add(-3 * hour)), GPUs: 1},
			from:      t0.Add(-24 * hour),
			to:        t0,
			wantHours: 2,
		},
		{
			name:      "starts before window, still running — clipped to window start",
			pod:       Pod{Start: t0.Add(-48 * hour), End: nil, GPUs: 1},
			from:      t0.Add(-24 * hour),
			to:        t0,
			wantHours: 24,
		},
		{
			name:      "starts before window, ends inside — clipped to window start",
			pod:       Pod{Start: t0.Add(-30 * hour), End: ptr(t0.Add(-6 * hour)), GPUs: 1},
			from:      t0.Add(-24 * hour),
			to:        t0,
			wantHours: 18,
		},
		{
			name:      "ends before window — zero",
			pod:       Pod{Start: t0.Add(-72 * hour), End: ptr(t0.Add(-48 * hour)), GPUs: 1},
			from:      t0.Add(-24 * hour),
			to:        t0,
			wantHours: 0,
		},
		{
			name:      "starts after window end — zero",
			pod:       Pod{Start: t0.Add(1 * hour), End: nil, GPUs: 1},
			from:      t0.Add(-24 * hour),
			to:        t0,
			wantHours: 0,
		},
		{
			name:      "starts exactly at window end — zero (half-open)",
			pod:       Pod{Start: t0, End: nil, GPUs: 1},
			from:      t0.Add(-24 * hour),
			to:        t0,
			wantHours: 0,
		},
		{
			name:      "ends exactly at window start — zero (half-open)",
			pod:       Pod{Start: t0.Add(-48 * hour), End: ptr(t0.Add(-24 * hour)), GPUs: 1},
			from:      t0.Add(-24 * hour),
			to:        t0,
			wantHours: 0,
		},
		{
			name:      "zero-duration pod — zero",
			pod:       Pod{Start: t0.Add(-3 * hour), End: ptr(t0.Add(-3 * hour)), GPUs: 1},
			from:      t0.Add(-24 * hour),
			to:        t0,
			wantHours: 0,
		},
		{
			name:      "inverted interval (clock skew) — zero",
			pod:       Pod{Start: t0.Add(-2 * hour), End: ptr(t0.Add(-3 * hour)), GPUs: 1},
			from:      t0.Add(-24 * hour),
			to:        t0,
			wantHours: 0,
		},
		{
			name:      "start in the future (clock skew) — zero",
			pod:       Pod{Start: t0.Add(5 * hour), End: nil, GPUs: 1},
			from:      t0.Add(-24 * hour),
			to:        t0,
			wantHours: 0,
		},
		{
			name:      "zero GPUs — zero",
			pod:       Pod{Start: t0.Add(-2 * hour), End: nil, GPUs: 0},
			from:      t0.Add(-24 * hour),
			to:        t0,
			wantHours: 0,
		},
		{
			name:      "negative GPUs (malformed input) — zero",
			pod:       Pod{Start: t0.Add(-2 * hour), End: nil, GPUs: -1},
			from:      t0.Add(-24 * hour),
			to:        t0,
			wantHours: 0,
		},
		{
			name:      "fractional GPUs (CPU-sim mode)",
			pod:       Pod{Start: t0.Add(-4 * hour), End: nil, GPUs: 0.25},
			from:      t0.Add(-24 * hour),
			to:        t0,
			wantHours: 1,
		},
		{
			name:      "8-GPU pod for 3 hours → 24 GPU-hours",
			pod:       Pod{Start: t0.Add(-3 * hour), End: nil, GPUs: 8},
			from:      t0.Add(-24 * hour),
			to:        t0,
			wantHours: 24,
		},
		{
			name:      "window inverted (from >= to) — zero",
			pod:       Pod{Start: t0.Add(-2 * hour), End: nil, GPUs: 1},
			from:      t0,
			to:        t0.Add(-24 * hour),
			wantHours: 0,
		},
		{
			name:      "window of zero length — zero",
			pod:       Pod{Start: t0.Add(-2 * hour), End: nil, GPUs: 1},
			from:      t0,
			to:        t0,
			wantHours: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := WindowUsage(tc.pod, tc.from, tc.to)
			if !approx(got, tc.wantHours) {
				t.Fatalf("WindowUsage = %v; want %v", got, tc.wantHours)
			}
			if got < 0 {
				t.Fatalf("WindowUsage returned negative hours: %v", got)
			}
		})
	}
}

// ---- Budget.Compute ----

func TestBudgetCompute(t *testing.T) {
	hour := time.Hour
	mkPod := func(id string, startHoursAgo, gpus float64, endHoursAgo *float64) Pod {
		p := Pod{
			ID:    id,
			Start: t0.Add(-time.Duration(startHoursAgo * float64(hour))),
			GPUs:  gpus,
		}
		if endHoursAgo != nil {
			end := t0.Add(-time.Duration(*endHoursAgo * float64(hour)))
			p.End = &end
		}
		return p
	}
	hoursAgo := func(h float64) *float64 { return &h }

	tests := []struct {
		name   string
		budget Budget
		pods   []Pod
		want   Result
	}{
		{
			name:   "empty pods — zero consumed, remaining = quota",
			budget: Budget{Window: 24 * hour, Quota: 100},
			pods:   nil,
			want:   Result{Consumed: 0, Remaining: 100, TrackedPods: 0, Over: false},
		},
		{
			name:   "single pod under quota",
			budget: Budget{Window: 24 * hour, Quota: 100},
			pods:   []Pod{mkPod("a", 2, 1, nil)},
			want:   Result{Consumed: 2, Remaining: 98, TrackedPods: 1, Over: false},
		},
		{
			name:   "multiple pods summed",
			budget: Budget{Window: 24 * hour, Quota: 100},
			pods: []Pod{
				mkPod("a", 2, 8, nil),           // 16 GPU-h
				mkPod("b", 5, 4, hoursAgo(1)),   // 16 GPU-h
				mkPod("c", 30, 1, hoursAgo(28)), // ends before window — 0
				mkPod("d", 48, 2, nil),          // clipped to 24h × 2 = 48 GPU-h
			},
			want: Result{Consumed: 80, Remaining: 20, TrackedPods: 4, Over: false},
		},
		{
			name:   "quota reached exactly — Over=true",
			budget: Budget{Window: 24 * hour, Quota: 100},
			pods:   []Pod{mkPod("a", 10, 10, nil)},
			want:   Result{Consumed: 100, Remaining: 0, TrackedPods: 1, Over: true},
		},
		{
			name:   "quota exceeded — Remaining floored at 0",
			budget: Budget{Window: 24 * hour, Quota: 100},
			pods:   []Pod{mkPod("a", 20, 10, nil)},
			want:   Result{Consumed: 200, Remaining: 0, TrackedPods: 1, Over: true},
		},
		{
			name:   "pod matched but contributed 0 still counts as tracked",
			budget: Budget{Window: 24 * hour, Quota: 100},
			pods: []Pod{
				mkPod("a", 2, 1, nil),           // 2 GPU-h
				mkPod("b", 48, 1, hoursAgo(36)), // before window — 0
			},
			want: Result{Consumed: 2, Remaining: 98, TrackedPods: 2, Over: false},
		},
		{
			name:   "restart-recovery path: running + already-terminated pod",
			budget: Budget{Window: 24 * hour, Quota: 100},
			pods: []Pod{
				// Terminated pod observed after operator restart.
				mkPod("terminated", 12, 2, hoursAgo(6)), // 2 × 6h = 12 GPU-h
				// Live pod observed on the first post-restart reconcile.
				mkPod("running", 4, 4, nil), // 4 × 4h = 16 GPU-h
			},
			want: Result{Consumed: 28, Remaining: 72, TrackedPods: 2, Over: false},
		},
		{
			name:   "fractional quota / CPU-sim GPUs",
			budget: Budget{Window: 24 * hour, Quota: 0.5},
			pods:   []Pod{mkPod("a", 4, 0.1, nil)}, // 0.4 GPU-h
			want:   Result{Consumed: 0.4, Remaining: 0.1, TrackedPods: 1, Over: false},
		},
		{
			name:   "window=0 falls back to all-time window (no panic)",
			budget: Budget{Window: 0, Quota: 100},
			pods:   []Pod{mkPod("a", 10, 1, nil)},
			want:   Result{Consumed: 10, Remaining: 90, TrackedPods: 1, Over: false},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.budget.Compute(t0, tc.pods)
			if !approx(got.Consumed, tc.want.Consumed) {
				t.Errorf("Consumed = %v; want %v", got.Consumed, tc.want.Consumed)
			}
			if !approx(got.Remaining, tc.want.Remaining) {
				t.Errorf("Remaining = %v; want %v", got.Remaining, tc.want.Remaining)
			}
			if got.TrackedPods != tc.want.TrackedPods {
				t.Errorf("TrackedPods = %d; want %d", got.TrackedPods, tc.want.TrackedPods)
			}
			if got.Over != tc.want.Over {
				t.Errorf("Over = %v; want %v", got.Over, tc.want.Over)
			}
			if got.Consumed < 0 {
				t.Errorf("Consumed is negative: %v", got.Consumed)
			}
			if got.Remaining < 0 {
				t.Errorf("Remaining is negative: %v", got.Remaining)
			}
		})
	}
}

// TestComputeInvariants cross-checks that Compute == sum of per-pod
// WindowUsage. Guards against a future refactor that splits the loop
// and drops a term.
func TestComputeInvariants(t *testing.T) {
	hour := time.Hour
	b := Budget{Window: 24 * hour, Quota: 100}
	pods := []Pod{
		{ID: "a", Start: t0.Add(-2 * hour), End: nil, GPUs: 1},
		{ID: "b", Start: t0.Add(-48 * hour), End: ptr(t0.Add(-6 * hour)), GPUs: 2},
		{ID: "c", Start: t0.Add(-1 * hour), End: nil, GPUs: 4},
	}

	var perPodSum float64
	for _, p := range pods {
		perPodSum += WindowUsage(p, t0.Add(-b.Window), t0)
	}
	got := b.Compute(t0, pods)
	if !approx(got.Consumed, perPodSum) {
		t.Fatalf("Compute.Consumed (%v) != sum of WindowUsage (%v)", got.Consumed, perPodSum)
	}
}
