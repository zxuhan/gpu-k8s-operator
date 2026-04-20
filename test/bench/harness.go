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

// Package bench contains the pure math the benchmark harness needs to
// turn a scenario description ("N pods at R/s each for T seconds
// requesting G GPUs") into a ground-truth "expected GPU-hours at
// observation time", independent of the operator's own accounting.
//
// Kept in its own package — free of client-go — so the core
// deterministic-expected formula is unit-testable to ~ns precision
// without spinning up envtest.
package bench

import (
	"fmt"
	"time"
)

// Scenario describes a deterministic bench run: the generator creates
// Count pods at Rate pods/second, each running for Runtime requesting
// GPUs units of a tracked resource. Every pod starts at create-time
// (the benchmark assumes image-pull and scheduling delay are
// negligible; see docs/benchmark-methodology.md for why busybox + kind
// satisfies that).
type Scenario struct {
	Count   int
	Rate    float64       // pods per second; must be > 0
	Runtime time.Duration // how long each pod consumes the resource
	GPUs    float64       // quantity requested per pod (may be fractional)
}

// Validate checks a Scenario for pre-conditions the expected-hours
// formula relies on. Returns a human-readable error so the CLI wrapper
// can fail fast with a useful message.
func (s Scenario) Validate() error {
	if s.Count <= 0 {
		return fmt.Errorf("count must be > 0, got %d", s.Count)
	}
	if s.Rate <= 0 {
		return fmt.Errorf("rate must be > 0, got %v", s.Rate)
	}
	if s.Runtime <= 0 {
		return fmt.Errorf("runtime must be > 0, got %v", s.Runtime)
	}
	if s.GPUs <= 0 {
		return fmt.Errorf("gpus must be > 0, got %v", s.GPUs)
	}
	return nil
}

// ExpectedGPUHours returns the deterministic GPU-hours the scenario
// *would* consume by `elapsed` seconds after the first pod was
// created, given ideal scheduling (pods begin consuming the resource
// the instant they are created and stop consuming Runtime later).
//
// The exact model:
//
//	pod i ∈ [0, Count) is created at t_i = i / Rate
//	pod i is running over [t_i, t_i + Runtime)
//	its contribution at observation time T is
//	    contrib_i(T) = GPUs · max(0, min(T, t_i+Runtime) - t_i)
//	ExpectedGPUHours(T) = (Σ contrib_i(T)) / 3600
//
// A live reconciler can only under-report against this number (due to
// kubelet start-up lag, reconcile cadence, and scheduling slop).
// Comparing reported to this value gives a lower-bound-biased accuracy
// ratio — which is exactly the measurement operators care about
// ("could my over-budget alert have fired late?").
func ExpectedGPUHours(s Scenario, elapsed time.Duration) float64 {
	if err := s.Validate(); err != nil {
		return 0
	}
	if elapsed <= 0 {
		return 0
	}

	// arrivalInterval is the seconds-between-creates; using float here
	// matches the generator's ticker math so the two agree at the
	// rounding-free level.
	arrivalInterval := 1.0 / s.Rate
	tEnd := elapsed.Seconds()
	runSec := s.Runtime.Seconds()

	var totalSec float64
	for i := 0; i < s.Count; i++ {
		tStart := float64(i) * arrivalInterval
		if tStart >= tEnd {
			break
		}
		finish := tStart + runSec
		if finish > tEnd {
			finish = tEnd
		}
		totalSec += (finish - tStart) * s.GPUs
	}
	return totalSec / 3600.0
}

// AccuracyRatio normalises a reported-vs-expected pair into the
// [0,1]-clamped ratio the README cites. Defined here (not inline at
// call sites) so its handling of divide-by-zero and over-reporting is
// obvious and testable.
//
//	expected == 0: ratio is 1.0 when reported is also 0, else 0.
//	reported > expected: clamped to 1.0. A slightly-over-reported number
//	    is a sign of double-counting, but the accuracy *score* is still
//	    pinned at full marks — the real tell is that the Delta (reported
//	    − expected) is > 0, which callers inspect separately.
func AccuracyRatio(reported, expected float64) float64 {
	if expected <= 0 {
		if reported <= 0 {
			return 1.0
		}
		return 0
	}
	r := reported / expected
	if r < 0 {
		return 0
	}
	if r > 1 {
		return 1
	}
	return r
}
