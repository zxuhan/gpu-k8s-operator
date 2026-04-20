//go:build e2e
// +build e2e

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

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/zxuhan/gpu-k8s-operator/test/utils"
)

// e2eTestNamespace is an isolated namespace for CR-lifecycle scenarios.
// Kept separate from the controller's own namespace so teardown here
// cannot accidentally evict the operator.
const e2eTestNamespace = "gwb-e2e"

// declareGWBLifecycleContext registers the project-specific e2e
// scenarios as a sibling Context under the Ordered "Manager" Describe
// defined in e2e_test.go. It runs after the metrics/webhook-readiness
// checks, so every spec in here can assume the webhook endpoints are
// serving and the controller is reconciling.
func declareGWBLifecycleContext() {
	Context("GPUWorkloadBudget lifecycle", Ordered, func() {
		BeforeAll(func() {
			By("creating the e2e test namespace")
			cmd := exec.Command("kubectl", "create", "ns", e2eTestNamespace)
			if _, err := utils.Run(cmd); err != nil &&
				!strings.Contains(err.Error(), "AlreadyExists") {
				Fail(fmt.Sprintf("create namespace: %v", err))
			}
		})

		AfterAll(func() {
			// Delete every budget synchronously so the reconciler's
			// finalizer can drain while the controller pod is still
			// running. The parent Describe's AfterAll tears down the
			// operator + CRDs right after this hook; skipping it leaves
			// orphan finalizers that deadlock the CRD delete.
			By("deleting GPUWorkloadBudgets before the controller goes away")
			cmd := exec.Command("kubectl", "delete", "gwb", "--all",
				"-n", e2eTestNamespace,
				"--wait=true", "--timeout=60s", "--ignore-not-found")
			_, _ = utils.Run(cmd)

			By("tearing down the e2e test namespace")
			cmd = exec.Command("kubectl", "delete", "ns", e2eTestNamespace,
				"--ignore-not-found", "--wait=false")
			_, _ = utils.Run(cmd)
		})

		It("rejects an empty selector via the validating webhook", func() {
			yaml := gwbYAML(gwbFixture{
				Name:      "bad-empty-selector",
				Namespace: e2eTestNamespace,
				// Selector intentionally empty — the webhook must refuse it
				// because an empty selector silently matches every pod.
				SelectorApp: "",
				GPUHours:    "1",
				WindowHours: 1,
				Action:      "AlertOnly",
			})
			Eventually(func(g Gomega) {
				out, err := applyYAMLRaw(yaml)
				g.Expect(err).To(HaveOccurred(), "expected webhook rejection")
				g.Expect(out).To(ContainSubstring("spec.selector"))
				g.Expect(out).To(ContainSubstring("must not be empty"))
			}, 2*time.Minute, 5*time.Second).Should(Succeed())
		})

		It("rejects non-positive gpuHours via the validating webhook", func() {
			yaml := gwbYAML(gwbFixture{
				Name:        "bad-zero-quota",
				Namespace:   e2eTestNamespace,
				SelectorApp: "gwb-e2e-worker",
				GPUHours:    "0",
				WindowHours: 1,
				Action:      "AlertOnly",
			})
			out, err := applyYAMLRaw(yaml)
			Expect(err).To(HaveOccurred(), "expected webhook rejection")
			Expect(out).To(ContainSubstring("spec.quota.gpuHours"))
			Expect(out).To(ContainSubstring("must be greater than 0"))
		})

		It("rejects an unknown enforcement action at admission", func() {
			yaml := gwbYAML(gwbFixture{
				Name:        "bad-action",
				Namespace:   e2eTestNamespace,
				SelectorApp: "gwb-e2e-worker",
				GPUHours:    "1",
				WindowHours: 1,
				Action:      "YellAtOps",
			})
			out, err := applyYAMLRaw(yaml)
			Expect(err).To(HaveOccurred(), "expected rejection of unknown action")
			// The OpenAPI enum fires before the webhook for a syntactically
			// invalid value; either layer's message is acceptable, we just
			// need the offending field referenced.
			Expect(out).To(ContainSubstring("action"))
		})

		It("reconciles a valid budget to Ready=True with zero pods", func() {
			yaml := gwbYAML(gwbFixture{
				Name:        "ready-check",
				Namespace:   e2eTestNamespace,
				SelectorApp: "gwb-e2e-ready-worker",
				GPUHours:    "10",
				WindowHours: 1,
				// cpu as the tracked resource lets us account pods without
				// scheduling real GPUs — kind clusters don't advertise
				// nvidia.com/gpu. See docs/accounting-model.md.
				GPUResource: "cpu",
				Action:      "AlertOnly",
			})
			Expect(applyYAML(yaml)).To(Succeed())

			By("waiting for the Ready condition to flip True")
			Eventually(func(g Gomega) {
				status, err := readyConditionStatus("ready-check")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(status).To(Equal("True"))
			}, 2*time.Minute, 2*time.Second).Should(Succeed())

			By("asserting trackedPods == 0 while no matching pod exists")
			Eventually(func(g Gomega) {
				tracked, err := budgetJSONPath("ready-check",
					"{.status.trackedPods}")
				g.Expect(err).NotTo(HaveOccurred())
				// The field is omitted when zero — jsonpath returns "".
				g.Expect(tracked).To(Or(Equal(""), Equal("0")))
			}, 1*time.Minute, 2*time.Second).Should(Succeed())
		})

		It("accounts for a live pod matching the selector", func() {
			yaml := gwbYAML(gwbFixture{
				Name:        "accounting",
				Namespace:   e2eTestNamespace,
				SelectorApp: "gwb-e2e-acct-worker",
				// Large quota keeps enforcement out of this test — we only
				// exercise the accounting path here; enforcement gets its
				// own spec.
				GPUHours:    "10",
				WindowHours: 1,
				GPUResource: "cpu",
				Action:      "AlertOnly",
			})
			Expect(applyYAML(yaml)).To(Succeed())

			By("launching a worker pod matching the selector")
			Expect(applyYAML(workerPodYAML(workerFixture{
				Name:      "acct-worker",
				Namespace: e2eTestNamespace,
				Label:     "gwb-e2e-acct-worker",
				CPU:       "100m",
			}))).To(Succeed())

			By("waiting for trackedPods to reflect the worker")
			Eventually(func(g Gomega) {
				tracked, err := budgetJSONPath("accounting",
					"{.status.trackedPods}")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(tracked).To(Equal("1"))
			}, 3*time.Minute, 2*time.Second).Should(Succeed())

			By("waiting for non-zero consumedGpuHours")
			Eventually(func(g Gomega) {
				consumed, err := budgetJSONPath("accounting",
					"{.status.consumedGpuHours}")
				g.Expect(err).NotTo(HaveOccurred())
				// quantity strings like "1m", "12345u", "0" — anything
				// other than empty or the literal "0" means the gauge has
				// moved above zero.
				g.Expect(consumed).NotTo(BeEmpty())
				g.Expect(consumed).NotTo(Equal("0"))
			}, 3*time.Minute, 2*time.Second).Should(Succeed())
		})

		It("fires AlertOnly enforcement once the budget is exceeded", func() {
			yaml := gwbYAML(gwbFixture{
				Name:        "alert-enforce",
				Namespace:   e2eTestNamespace,
				SelectorApp: "gwb-e2e-alert-worker",
				// Tiny quota + cpu accounting + grace=5s means we blow
				// through the budget within seconds once the pod is
				// Running.
				GPUHours:     "1m",
				WindowHours:  1,
				GPUResource:  "cpu",
				Action:       "AlertOnly",
				GracePeriodS: 5,
			})
			Expect(applyYAML(yaml)).To(Succeed())

			By("launching a worker pod requesting one full CPU")
			Expect(applyYAML(workerPodYAML(workerFixture{
				Name:      "alert-worker",
				Namespace: e2eTestNamespace,
				Label:     "gwb-e2e-alert-worker",
				CPU:       "1",
			}))).To(Succeed())

			By("waiting for QuotaExceeded=True")
			Eventually(func(g Gomega) {
				status, err := conditionStatus("alert-enforce", "QuotaExceeded")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(status).To(Equal("True"))
			}, 3*time.Minute, 2*time.Second).Should(Succeed())

			By("waiting for LastEnforcementAt to be stamped past the grace period")
			Eventually(func(g Gomega) {
				out, err := budgetJSONPath("alert-enforce",
					"{.status.lastEnforcementAt}")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).NotTo(BeEmpty())
			}, 3*time.Minute, 2*time.Second).Should(Succeed())

			By("verifying a QuotaExceeded warning event was emitted against the CR")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "events",
					"-n", e2eTestNamespace,
					"--field-selector",
					"involvedObject.kind=GPUWorkloadBudget,involvedObject.name=alert-enforce,type=Warning",
					"-o", "jsonpath={.items[*].reason}")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(ContainSubstring("QuotaExceeded"))
			}, 2*time.Minute, 2*time.Second).Should(Succeed())

			By("verifying the worker pod itself was NOT evicted or annotated (AlertOnly contract)")
			cmd := exec.Command("kubectl", "get", "pod", "alert-worker",
				"-n", e2eTestNamespace,
				"-o", "jsonpath={.metadata.annotations['budget\\.zxuhan\\.dev/paused-at']}")
			out, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(out).To(BeEmpty(),
				"AlertOnly must not stamp a pause annotation")
		})
	})
}

// gwbFixture is a tiny DSL for rendering GPUWorkloadBudget YAMLs
// without pulling in a templating library. Unset fields are elided.
type gwbFixture struct {
	Name         string
	Namespace    string
	SelectorApp  string // empty → empty selector (used to probe webhook)
	GPUHours     string
	WindowHours  int
	GPUResource  string // empty → rely on CRD default (nvidia.com/gpu)
	Action       string
	GracePeriodS int
}

func gwbYAML(f gwbFixture) string {
	var selector string
	if f.SelectorApp == "" {
		selector = "  selector: {}"
	} else {
		selector = fmt.Sprintf("  selector:\n    matchLabels:\n      app: %s", f.SelectorApp)
	}
	grace := ""
	if f.GracePeriodS > 0 {
		grace = fmt.Sprintf("    gracePeriodSeconds: %d\n", f.GracePeriodS)
	}
	gpuRes := ""
	if f.GPUResource != "" {
		gpuRes = fmt.Sprintf("  gpuResourceName: %q\n", f.GPUResource)
	}
	return fmt.Sprintf(`apiVersion: budget.zxuhan.dev/v1alpha1
kind: GPUWorkloadBudget
metadata:
  name: %s
  namespace: %s
spec:
%s
  quota:
    gpuHours: %q
    windowHours: %d
  enforcement:
    action: %s
%s%s`,
		f.Name, f.Namespace, selector, f.GPUHours, f.WindowHours, f.Action, grace, gpuRes)
}

type workerFixture struct {
	Name      string
	Namespace string
	Label     string
	CPU       string
}

// workerPodYAML renders a minimal long-running pod that matches a
// selector via app=<Label>. busybox `sleep` keeps the container alive
// long enough for the accounting engine to observe it without pulling
// a heavyweight image.
func workerPodYAML(f workerFixture) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: %s
  namespace: %s
  labels:
    app: %s
spec:
  restartPolicy: Never
  terminationGracePeriodSeconds: 1
  containers:
  - name: worker
    image: busybox:1.36
    command: ["sh", "-c", "sleep 3600"]
    resources:
      requests:
        cpu: %q
`, f.Name, f.Namespace, f.Label, f.CPU)
}

// applyYAML runs `kubectl apply` against the given manifest, returning
// an error with the kubectl output if the apply fails.
func applyYAML(manifest string) error {
	path, err := writeTempManifest(manifest)
	if err != nil {
		return err
	}
	defer os.Remove(path)
	cmd := exec.Command("kubectl", "apply", "-f", path)
	_, err = utils.Run(cmd)
	return err
}

// applyYAMLRaw is the same as applyYAML but returns the raw combined
// output regardless of success so callers can pattern-match webhook
// error messages.
func applyYAMLRaw(manifest string) (string, error) {
	path, err := writeTempManifest(manifest)
	if err != nil {
		return "", err
	}
	defer os.Remove(path)
	cmd := exec.Command("kubectl", "apply", "-f", path)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func writeTempManifest(manifest string) (string, error) {
	f, err := os.CreateTemp("", "gwb-e2e-*.yaml")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	if _, err := f.WriteString(manifest); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("write temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("close temp file: %w", err)
	}
	return f.Name(), nil
}

// budgetJSONPath runs `kubectl get gwb <name> -n <ns> -o jsonpath=<path>`
// and returns the raw string. Empty strings are common (jsonpath returns
// "" when a field is absent) and callers are expected to interpret them.
func budgetJSONPath(name, jsonpath string) (string, error) {
	cmd := exec.Command("kubectl", "get", "gwb", name,
		"-n", e2eTestNamespace,
		"-o", "jsonpath="+jsonpath)
	out, err := utils.Run(cmd)
	return strings.TrimSpace(out), err
}

// readyConditionStatus is a convenience wrapper around budgetJSONPath
// for the Ready status. Keeps the Eventually blocks above readable.
func readyConditionStatus(name string) (string, error) {
	return conditionStatus(name, "Ready")
}

// conditionStatus returns the Status field of the named condition, or
// empty string if the condition has not yet been written.
func conditionStatus(name, condType string) (string, error) {
	return budgetJSONPath(name,
		fmt.Sprintf(`{.status.conditions[?(@.type=="%s")].status}`, condType))
}
