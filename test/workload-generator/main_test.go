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

package main

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestParseLabel(t *testing.T) {
	cases := []struct {
		in      string
		wantK   string
		wantV   string
		wantErr bool
	}{
		{"app=bench", "app", "bench", false},
		{"team.k8s.io/foo=bar", "team.k8s.io/foo", "bar", false},
		{"", "", "", true},
		{"no-equals", "", "", true},
		{"=value-only", "", "", true},
		{"key-only=", "", "", true},
	}
	for _, c := range cases {
		gotK, gotV, err := parseLabel(c.in)
		if (err != nil) != c.wantErr {
			t.Fatalf("parseLabel(%q) err=%v, wantErr=%v", c.in, err, c.wantErr)
		}
		if err == nil {
			if gotK != c.wantK || gotV != c.wantV {
				t.Errorf("parseLabel(%q) = (%q,%q), want (%q,%q)", c.in, gotK, gotV, c.wantK, c.wantV)
			}
		}
	}
}

func TestIntervalFromRate(t *testing.T) {
	cases := []struct {
		rate float64
		want time.Duration
	}{
		{1.0, time.Second},
		{10.0, 100 * time.Millisecond},
		{2.0, 500 * time.Millisecond},
		// 0.5 pods/sec = one pod every 2s
		{0.5, 2 * time.Second},
	}
	for _, c := range cases {
		got := intervalFromRate(c.rate)
		if got != c.want {
			t.Errorf("intervalFromRate(%v) = %v, want %v", c.rate, got, c.want)
		}
	}
}

func TestBuildPodShape(t *testing.T) {
	o := options{
		Namespace:   "bench",
		Count:       3,
		Runtime:     15 * time.Second,
		GPUResource: "cpu",
		Image:       "busybox:1.36",
		Prefix:      "bench-",
	}
	gpuQty := resource.MustParse("100m")

	pod := buildPod(o, "app", "gwb-bench-worker", gpuQty, 7)

	// Identity + namespace + prefix padding
	if pod.Name != "bench-0007" {
		t.Errorf("pod name = %q, want bench-0007", pod.Name)
	}
	if pod.Namespace != "bench" {
		t.Errorf("pod namespace = %q, want bench", pod.Namespace)
	}

	// Label is the primary contract with the budget selector; double
	// check both the bench label AND the managed-by metadata (used by
	// bench.sh for cleanup without knowing the user's label).
	if got := pod.Labels["app"]; got != "gwb-bench-worker" {
		t.Errorf("pod label app=%q, want gwb-bench-worker", got)
	}
	if got := pod.Labels["app.kubernetes.io/managed-by"]; got != "gwb-workload" {
		t.Errorf("pod managed-by label = %q, want gwb-workload", got)
	}

	// Pod spec: one container, RestartPolicyNever, sleep=runtime,
	// requested resource matches --gpu-resource.
	if pod.Spec.RestartPolicy != corev1.RestartPolicyNever {
		t.Errorf("RestartPolicy = %q, want Never", pod.Spec.RestartPolicy)
	}
	if n := len(pod.Spec.Containers); n != 1 {
		t.Fatalf("containers = %d, want 1", n)
	}
	c := pod.Spec.Containers[0]
	if c.Image != "busybox:1.36" {
		t.Errorf("image = %q", c.Image)
	}
	wantCmd := "sleep 15"
	if len(c.Command) != 3 || c.Command[2] != wantCmd {
		t.Errorf("command = %v, want [sh -c %q]", c.Command, wantCmd)
	}
	q, ok := c.Resources.Requests["cpu"]
	if !ok {
		t.Fatalf("missing cpu request; got %v", c.Resources.Requests)
	}
	if q.Cmp(gpuQty) != 0 {
		t.Errorf("cpu request = %s, want %s", q.String(), gpuQty.String())
	}
}
