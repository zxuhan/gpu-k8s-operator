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

// gwb-bench turns a GPUWorkloadBudget status snapshot plus the scenario
// flags that produced it into a pair of artefacts the repo commits:
// results.json (machine-readable accuracy/delta) and SUMMARY.md (a
// human-readable table). The CLI exists as a separate binary from the
// workload generator so `hack/bench.sh` can replay a single saved
// status.json against different scenario parameters without re-running
// the kind cluster, which matters when the README's numbers are
// contested and someone wants to re-derive them from the archived
// inputs.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/zxuhan/gpu-k8s-operator/test/bench"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "report":
		if err := reportCmd(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `gwb-bench — compute expected-vs-reported GPU-hours for a bench run.

Usage:
  gwb-bench report [flags]

Flags (all required unless noted):
  --count         int      total pods the generator launched
  --rate          float    pods per second
  --runtime       duration per-pod sleep (e.g. 30s)
  --gpus          float    GPUs requested per pod (fractional ok, e.g. 0.001)
  --elapsed       duration wall-clock elapsed between first-pod create and snapshot
  --status-json   path     file containing GWB JSON (either full object or its .status)
  --out-dir       path     directory for SUMMARY.md and results.json (created if absent)
  --label         string   optional; echoed into SUMMARY.md for provenance

Exit codes:
  0 success · 1 runtime error · 2 flag/argument error
`)
}

type reportFlags struct {
	Count      int
	Rate       float64
	Runtime    time.Duration
	GPUs       float64
	Elapsed    time.Duration
	StatusJSON string
	OutDir     string
	Label      string
}

func reportCmd(args []string) error {
	var f reportFlags
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	fs.IntVar(&f.Count, "count", 0, "total pods launched")
	fs.Float64Var(&f.Rate, "rate", 0, "pods per second")
	fs.DurationVar(&f.Runtime, "runtime", 0, "per-pod sleep")
	fs.Float64Var(&f.GPUs, "gpus", 0, "GPUs per pod")
	fs.DurationVar(&f.Elapsed, "elapsed", 0, "elapsed since first pod created")
	fs.StringVar(&f.StatusJSON, "status-json", "", "path to GWB status JSON")
	fs.StringVar(&f.OutDir, "out-dir", "", "output directory for artefacts")
	fs.StringVar(&f.Label, "label", "", "provenance label to embed in SUMMARY.md")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := f.validate(); err != nil {
		return err
	}

	scenario := bench.Scenario{
		Count:   f.Count,
		Rate:    f.Rate,
		Runtime: f.Runtime,
		GPUs:    f.GPUs,
	}
	if err := scenario.Validate(); err != nil {
		return fmt.Errorf("invalid scenario: %w", err)
	}

	reported, tracked, err := readReported(f.StatusJSON)
	if err != nil {
		return fmt.Errorf("read status: %w", err)
	}
	expected := bench.ExpectedGPUHours(scenario, f.Elapsed)
	ratio := bench.AccuracyRatio(reported, expected)

	res := result{
		Scenario:         scenario,
		ElapsedSeconds:   f.Elapsed.Seconds(),
		ReportedGPUHours: reported,
		ExpectedGPUHours: expected,
		DeltaGPUHours:    reported - expected,
		AccuracyRatio:    ratio,
		TrackedPods:      tracked,
		Label:            f.Label,
		GeneratedAt:      time.Now().UTC().Format(time.RFC3339),
	}

	if err := os.MkdirAll(f.OutDir, 0o755); err != nil {
		return fmt.Errorf("mkdir out-dir: %w", err)
	}
	if err := writeResultsJSON(filepath.Join(f.OutDir, "results.json"), res); err != nil {
		return err
	}
	if err := writeSummaryMD(filepath.Join(f.OutDir, "SUMMARY.md"), res); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(os.Stdout,
		"reported=%.6f expected=%.6f delta=%+.6f accuracy=%.4f tracked=%d\n",
		res.ReportedGPUHours, res.ExpectedGPUHours, res.DeltaGPUHours,
		res.AccuracyRatio, res.TrackedPods); err != nil {
		return err
	}
	return nil
}

func (f reportFlags) validate() error {
	switch {
	case f.Count <= 0:
		return fmt.Errorf("--count must be > 0")
	case f.Rate <= 0:
		return fmt.Errorf("--rate must be > 0")
	case f.Runtime <= 0:
		return fmt.Errorf("--runtime must be > 0")
	case f.GPUs <= 0:
		return fmt.Errorf("--gpus must be > 0")
	case f.Elapsed <= 0:
		return fmt.Errorf("--elapsed must be > 0")
	case f.StatusJSON == "":
		return fmt.Errorf("--status-json is required")
	case f.OutDir == "":
		return fmt.Errorf("--out-dir is required")
	}
	return nil
}

// readReported extracts consumedGpuHours (as hours, float) and
// trackedPods from a JSON document. Accepts either the full GWB object
// (looks at .status) or a bare status object — whichever `kubectl -o
// jsonpath` the user found easier.
func readReported(path string) (hours float64, tracked int32, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, err
	}
	var any map[string]json.RawMessage
	if err := json.Unmarshal(raw, &any); err != nil {
		return 0, 0, fmt.Errorf("parse JSON: %w", err)
	}
	statusBlob, ok := any["status"]
	if !ok {
		// Treat the whole document as the status subresource.
		statusBlob = raw
	}
	var status struct {
		ConsumedGPUHours *string `json:"consumedGpuHours,omitempty"`
		TrackedPods      *int32  `json:"trackedPods,omitempty"`
	}
	if err := json.Unmarshal(statusBlob, &status); err != nil {
		return 0, 0, fmt.Errorf("parse status: %w", err)
	}
	if status.ConsumedGPUHours != nil {
		q, err := resource.ParseQuantity(*status.ConsumedGPUHours)
		if err != nil {
			return 0, 0, fmt.Errorf("parse consumedGpuHours %q: %w", *status.ConsumedGPUHours, err)
		}
		// AsApproximateFloat64 is fine here: GPU-hours are measured in
		// units of 1/3600 (one pod-second); float64 has ~15 decimal
		// digits and the biggest scenarios we bench are ~1k pods, so
		// rounding is 10+ orders of magnitude below the signal.
		hours = q.AsApproximateFloat64()
	}
	if status.TrackedPods != nil {
		tracked = *status.TrackedPods
	}
	return hours, tracked, nil
}

type result struct {
	Scenario         bench.Scenario `json:"scenario"`
	ElapsedSeconds   float64        `json:"elapsedSeconds"`
	ReportedGPUHours float64        `json:"reportedGpuHours"`
	ExpectedGPUHours float64        `json:"expectedGpuHours"`
	DeltaGPUHours    float64        `json:"deltaGpuHours"`
	AccuracyRatio    float64        `json:"accuracyRatio"`
	TrackedPods      int32          `json:"trackedPods"`
	Label            string         `json:"label,omitempty"`
	GeneratedAt      string         `json:"generatedAt"`
}

func writeResultsJSON(path string, r result) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// writeSummaryMD renders the Markdown table the README cross-references.
// Column set and precision are chosen to be diff-friendly across runs:
// six decimal places on GPU-hours (= 3.6ms resolution) and four on the
// accuracy ratio (enough to tell 0.9999 apart from 1.0000 without
// flapping under sub-millisecond jitter).
func writeSummaryMD(path string, r result) error {
	var b bytes.Buffer
	fmt.Fprintf(&b, "# Benchmark Summary\n\n")
	fmt.Fprintf(&b, "Generated: %s\n\n", r.GeneratedAt)
	if r.Label != "" {
		fmt.Fprintf(&b, "Label: `%s`\n\n", r.Label)
	}
	fmt.Fprintf(&b, "## Scenario\n\n")
	fmt.Fprintf(&b, "| Parameter | Value |\n|---|---|\n")
	fmt.Fprintf(&b, "| count | %d |\n", r.Scenario.Count)
	fmt.Fprintf(&b, "| rate (pods/s) | %g |\n", r.Scenario.Rate)
	fmt.Fprintf(&b, "| runtime | %s |\n", r.Scenario.Runtime)
	fmt.Fprintf(&b, "| gpus per pod | %g |\n", r.Scenario.GPUs)
	fmt.Fprintf(&b, "| elapsed at snapshot | %.1fs |\n\n", r.ElapsedSeconds)
	fmt.Fprintf(&b, "## Result\n\n")
	fmt.Fprintf(&b, "| Metric | Value |\n|---|---|\n")
	fmt.Fprintf(&b, "| reported GPU-hours | %.6f |\n", r.ReportedGPUHours)
	fmt.Fprintf(&b, "| expected GPU-hours | %.6f |\n", r.ExpectedGPUHours)
	fmt.Fprintf(&b, "| delta (reported − expected) | %+.6f |\n", r.DeltaGPUHours)
	fmt.Fprintf(&b, "| accuracy ratio | %.4f |\n", r.AccuracyRatio)
	fmt.Fprintf(&b, "| tracked pods | %d |\n\n", r.TrackedPods)
	fmt.Fprintf(&b,
		"A negative delta is the common case on kind: kubelet start-up "+
			"lag means the controller observes a pod a fraction of a "+
			"second after create-time. See docs/benchmark-methodology.md "+
			"for the accuracy model.\n")
	return os.WriteFile(path, b.Bytes(), 0o644)
}
