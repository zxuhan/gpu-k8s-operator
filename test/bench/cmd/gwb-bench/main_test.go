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
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeStatus(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, "status.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadReported_FullGWBObject(t *testing.T) {
	// Shape of `kubectl get gwb -o json` — the generator should dig
	// through .status without the user having to jsonpath it out.
	body := `{
	  "metadata": {"name": "bench"},
	  "spec": {},
	  "status": {
	    "consumedGpuHours": "500m",
	    "trackedPods": 42
	  }
	}`
	dir := t.TempDir()
	path := writeStatus(t, dir, body)
	hours, tracked, err := readReported(path)
	if err != nil {
		t.Fatal(err)
	}
	if hours != 0.5 {
		t.Errorf("hours = %v, want 0.5", hours)
	}
	if tracked != 42 {
		t.Errorf("tracked = %d, want 42", tracked)
	}
}

func TestReadReported_BareStatus(t *testing.T) {
	// Shape of `kubectl get gwb -o jsonpath='{.status}' | jq` — still
	// accepted so scripts can keep the artefact small.
	body := `{"consumedGpuHours": "2", "trackedPods": 3}`
	dir := t.TempDir()
	path := writeStatus(t, dir, body)
	hours, tracked, err := readReported(path)
	if err != nil {
		t.Fatal(err)
	}
	if hours != 2 {
		t.Errorf("hours = %v, want 2", hours)
	}
	if tracked != 3 {
		t.Errorf("tracked = %d, want 3", tracked)
	}
}

func TestReadReported_MissingFieldsYieldZeros(t *testing.T) {
	// An early-phase snapshot (status not yet populated) shouldn't
	// explode — we want the report to still render with reported=0.
	body := `{"status": {}}`
	dir := t.TempDir()
	path := writeStatus(t, dir, body)
	hours, tracked, err := readReported(path)
	if err != nil {
		t.Fatal(err)
	}
	if hours != 0 || tracked != 0 {
		t.Errorf("got (%v,%d), want (0,0)", hours, tracked)
	}
}

func TestReadReported_InvalidQuantity(t *testing.T) {
	body := `{"consumedGpuHours": "not-a-quantity"}`
	dir := t.TempDir()
	path := writeStatus(t, dir, body)
	if _, _, err := readReported(path); err == nil {
		t.Fatal("expected error for bad quantity")
	}
}

func TestReportCmd_WritesArtefacts(t *testing.T) {
	// End-to-end: feed a plausible status, scenario flags that match
	// the numbers, and check the two files land with a sane shape.
	outDir := t.TempDir()
	// 82m = 0.082 GPU-hours — close to (but deliberately not exactly)
	// the 0.082083… expected, so we can verify Delta comes out non-zero.
	statusPath := writeStatus(t, t.TempDir(),
		`{"status": {"consumedGpuHours": "82m", "trackedPods": 10}}`)

	args := []string{
		"--count=10",
		"--rate=10",
		"--runtime=30s",
		"--gpus=1",
		"--elapsed=30s",
		"--status-json=" + statusPath,
		"--out-dir=" + outDir,
		"--label=unit-test",
	}
	if err := reportCmd(args); err != nil {
		t.Fatalf("reportCmd: %v", err)
	}

	summary, err := os.ReadFile(filepath.Join(outDir, "SUMMARY.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(summary) == 0 {
		t.Fatal("SUMMARY.md is empty")
	}

	rawJSON, err := os.ReadFile(filepath.Join(outDir, "results.json"))
	if err != nil {
		t.Fatal(err)
	}
	var r result
	if err := json.Unmarshal(rawJSON, &r); err != nil {
		t.Fatalf("results.json is not valid JSON: %v", err)
	}
	if r.Scenario.Count != 10 || r.Scenario.Rate != 10 || r.Scenario.GPUs != 1 {
		t.Errorf("scenario round-trip lost values: %+v", r.Scenario)
	}
	if r.TrackedPods != 10 {
		t.Errorf("TrackedPods = %d, want 10", r.TrackedPods)
	}
	// Expected for 10 pods @ 10/s, 30s sleep, 1 GPU, observed at 30s
	// is 295.5 GPU-seconds = 295.5/3600 hours ≈ 0.082083.
	const want = 295.5 / 3600.0
	if diff := r.ExpectedGPUHours - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("ExpectedGPUHours = %v, want ~%v", r.ExpectedGPUHours, want)
	}
	if r.Label != "unit-test" {
		t.Errorf("Label = %q, want unit-test", r.Label)
	}
}

func TestReportCmd_RejectsMissingRequiredFlags(t *testing.T) {
	err := reportCmd([]string{"--count=1", "--rate=1", "--runtime=1s", "--gpus=1"})
	if err == nil {
		t.Fatal("expected error for missing --elapsed/--status-json/--out-dir")
	}
}
