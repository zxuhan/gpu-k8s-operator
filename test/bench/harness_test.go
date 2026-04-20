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

package bench

import (
	"math"
	"testing"
	"time"
)

// approx returns true when two float64s agree within 1e-9 — enough for
// scenarios measured in GPU-hours where a single pod's contribution at
// 1/3600 precision is still six orders of magnitude larger.
func approx(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func TestExpectedGPUHours_SinglePodPartialRun(t *testing.T) {
	// One pod, 1 GPU, sleep 60s. Observe at 30s → 30 GPU-seconds = 1/120 GPU-hours.
	s := Scenario{Count: 1, Rate: 1, Runtime: 60 * time.Second, GPUs: 1}
	got := ExpectedGPUHours(s, 30*time.Second)
	want := 30.0 / 3600.0
	if !approx(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestExpectedGPUHours_SinglePodFullRun(t *testing.T) {
	// Same pod, observed well past Runtime — contribution caps at Runtime.
	s := Scenario{Count: 1, Rate: 1, Runtime: 60 * time.Second, GPUs: 1}
	got := ExpectedGPUHours(s, 10*time.Minute)
	want := 60.0 / 3600.0
	if !approx(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestExpectedGPUHours_StaggeredArrivals(t *testing.T) {
	// 10 pods at 10/s → created at t=0, 0.1, 0.2, ..., 0.9s.
	// Each runs 30s at 1 GPU. Observe at t=30.0s:
	//   pod i is running for min(30 - i*0.1, 30) = 30 - i*0.1 seconds.
	//   (all pods still within their Runtime window at t=30s)
	// sum = Σ_{i=0..9} (30 - 0.1*i) = 300 - 0.1*(0+1+...+9) = 300 - 4.5 = 295.5 GPU-seconds
	// expected = 295.5 / 3600 hours
	s := Scenario{Count: 10, Rate: 10, Runtime: 30 * time.Second, GPUs: 1}
	got := ExpectedGPUHours(s, 30*time.Second)
	want := 295.5 / 3600.0
	if !approx(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestExpectedGPUHours_FractionalGPUs(t *testing.T) {
	// 4 pods @ 1/s, each requests 0.5 GPU, runs 10s. Observe at t=20s.
	//   pod 0: t0=0,  runs [0,10],  contributes 10s
	//   pod 1: t1=1,  runs [1,11],  contributes 10s
	//   pod 2: t2=2,  runs [2,12],  contributes 10s
	//   pod 3: t3=3,  runs [3,13],  contributes 10s
	//   total GPU-seconds = 40 * 0.5 = 20 → 20/3600 hours
	s := Scenario{Count: 4, Rate: 1, Runtime: 10 * time.Second, GPUs: 0.5}
	got := ExpectedGPUHours(s, 20*time.Second)
	want := 20.0 / 3600.0
	if !approx(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestExpectedGPUHours_BeforeFirstArrival(t *testing.T) {
	s := Scenario{Count: 5, Rate: 1, Runtime: 10 * time.Second, GPUs: 1}
	if got := ExpectedGPUHours(s, 0); got != 0 {
		t.Errorf("at t=0 got %v, want 0", got)
	}
	if got := ExpectedGPUHours(s, -time.Second); got != 0 {
		t.Errorf("at t<0 got %v, want 0", got)
	}
}

func TestExpectedGPUHours_InvalidScenarioReturnsZero(t *testing.T) {
	cases := []Scenario{
		{Count: 0, Rate: 1, Runtime: time.Second, GPUs: 1},
		{Count: 1, Rate: 0, Runtime: time.Second, GPUs: 1},
		{Count: 1, Rate: 1, Runtime: 0, GPUs: 1},
		{Count: 1, Rate: 1, Runtime: time.Second, GPUs: 0},
	}
	for _, s := range cases {
		if got := ExpectedGPUHours(s, time.Hour); got != 0 {
			t.Errorf("invalid scenario %+v returned %v, want 0", s, got)
		}
	}
}

func TestAccuracyRatio(t *testing.T) {
	cases := []struct {
		name     string
		reported float64
		expected float64
		want     float64
	}{
		{"perfect", 1.0, 1.0, 1.0},
		{"under-reports half", 0.5, 1.0, 0.5},
		{"over-reports clamped", 2.0, 1.0, 1.0},
		{"both zero is perfect", 0, 0, 1.0},
		{"reported non-zero against zero is 0", 1.0, 0, 0},
		{"negative reported is 0", -0.1, 1.0, 0},
	}
	for _, c := range cases {
		got := AccuracyRatio(c.reported, c.expected)
		if !approx(got, c.want) {
			t.Errorf("%s: AccuracyRatio(%v,%v) = %v, want %v",
				c.name, c.reported, c.expected, got, c.want)
		}
	}
}
