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

package v1alpha1

import (
	"context"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	budgetv1alpha1 "github.com/zxuhan/gpu-k8s-operator/api/v1alpha1"
)

// validBudget is the baseline a test mutates one field at a time. Keeping
// every entry in the table self-sufficient would triple the line count and
// drown the intent of each case.
func validBudget() *budgetv1alpha1.GPUWorkloadBudget {
	return &budgetv1alpha1.GPUWorkloadBudget{
		ObjectMeta: metav1.ObjectMeta{Name: "team-vision", Namespace: "ml-platform"},
		Spec: budgetv1alpha1.GPUWorkloadBudgetSpec{
			Selector: metav1.LabelSelector{
				MatchLabels: map[string]string{"gpu-budget/team": "vision"},
			},
			Quota: budgetv1alpha1.Quota{
				GPUHours:    resource.MustParse("100"),
				WindowHours: 168,
			},
			Enforcement: budgetv1alpha1.Enforcement{
				Action:             budgetv1alpha1.ActionEvict,
				GracePeriodSeconds: 30,
			},
			GPUResourceName: "nvidia.com/gpu",
		},
	}
}

var _ = Describe("GPUWorkloadBudget validating webhook", func() {
	var validator GPUWorkloadBudgetCustomValidator

	BeforeEach(func() {
		validator = GPUWorkloadBudgetCustomValidator{}
	})

	Context("ValidateCreate", func() {
		DescribeTable("accepts or rejects specs as expected",
			func(mutate func(b *budgetv1alpha1.GPUWorkloadBudget), wantErrSubstr string) {
				obj := validBudget()
				if mutate != nil {
					mutate(obj)
				}
				warnings, err := validator.ValidateCreate(context.Background(), obj)
				Expect(warnings).To(BeNil())
				if wantErrSubstr == "" {
					Expect(err).NotTo(HaveOccurred())
					return
				}
				Expect(err).To(HaveOccurred())
				Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected Invalid status error, got %T: %v", err, err)
				Expect(strings.ToLower(err.Error())).To(ContainSubstring(strings.ToLower(wantErrSubstr)))
			},
			Entry("baseline happy path", func(_ *budgetv1alpha1.GPUWorkloadBudget) {}, ""),
			Entry("Pause action accepted", func(b *budgetv1alpha1.GPUWorkloadBudget) {
				b.Spec.Enforcement.Action = budgetv1alpha1.ActionPause
			}, ""),
			Entry("AlertOnly action accepted", func(b *budgetv1alpha1.GPUWorkloadBudget) {
				b.Spec.Enforcement.Action = budgetv1alpha1.ActionAlertOnly
			}, ""),
			Entry("zero grace period accepted", func(b *budgetv1alpha1.GPUWorkloadBudget) {
				b.Spec.Enforcement.GracePeriodSeconds = 0
			}, ""),
			Entry("fractional gpuHours accepted", func(b *budgetv1alpha1.GPUWorkloadBudget) {
				b.Spec.Quota.GPUHours = resource.MustParse("0.25")
			}, ""),
			Entry("matchExpressions-only selector accepted", func(b *budgetv1alpha1.GPUWorkloadBudget) {
				b.Spec.Selector = metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{{
						Key:      "gpu-budget/team",
						Operator: metav1.LabelSelectorOpIn,
						Values:   []string{"vision", "speech"},
					}},
				}
			}, ""),

			Entry("zero gpuHours rejected", func(b *budgetv1alpha1.GPUWorkloadBudget) {
				b.Spec.Quota.GPUHours = resource.MustParse("0")
			}, "spec.quota.gpuHours"),
			Entry("negative gpuHours rejected", func(b *budgetv1alpha1.GPUWorkloadBudget) {
				b.Spec.Quota.GPUHours = resource.MustParse("-5")
			}, "spec.quota.gpuHours"),
			Entry("zero windowHours rejected", func(b *budgetv1alpha1.GPUWorkloadBudget) {
				b.Spec.Quota.WindowHours = 0
			}, "spec.quota.windowHours"),
			Entry("negative windowHours rejected", func(b *budgetv1alpha1.GPUWorkloadBudget) {
				b.Spec.Quota.WindowHours = -1
			}, "spec.quota.windowHours"),
			Entry("empty action rejected as required", func(b *budgetv1alpha1.GPUWorkloadBudget) {
				b.Spec.Enforcement.Action = ""
			}, "spec.enforcement.action"),
			Entry("unsupported action rejected", func(b *budgetv1alpha1.GPUWorkloadBudget) {
				b.Spec.Enforcement.Action = "Delete"
			}, "spec.enforcement.action"),
			Entry("negative grace period rejected", func(b *budgetv1alpha1.GPUWorkloadBudget) {
				b.Spec.Enforcement.GracePeriodSeconds = -1
			}, "spec.enforcement.gracePeriodSeconds"),
			Entry("empty selector rejected", func(b *budgetv1alpha1.GPUWorkloadBudget) {
				b.Spec.Selector = metav1.LabelSelector{}
			}, "spec.selector"),
			Entry("malformed selector operator rejected", func(b *budgetv1alpha1.GPUWorkloadBudget) {
				b.Spec.Selector = metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{{
						Key:      "team",
						Operator: "Contains", // not a valid LabelSelectorOperator
						Values:   []string{"vision"},
					}},
				}
			}, "spec.selector"),
		)

		It("aggregates multiple errors into one Invalid status", func() {
			obj := validBudget()
			obj.Spec.Quota.GPUHours = resource.MustParse("-1")
			obj.Spec.Quota.WindowHours = 0
			obj.Spec.Enforcement.Action = "Nuke"

			_, err := validator.ValidateCreate(context.Background(), obj)
			Expect(err).To(HaveOccurred())
			statusErr, ok := err.(*apierrors.StatusError)
			Expect(ok).To(BeTrue(), "expected *apierrors.StatusError, got %T", err)
			Expect(statusErr.Status().Details).NotTo(BeNil())
			Expect(statusErr.Status().Details.Causes).To(HaveLen(3))
		})
	})

	Context("ValidateUpdate", func() {
		It("applies the same rules as create (v1alpha1 has no immutable fields yet)", func() {
			obj := validBudget()
			old := validBudget()
			obj.Spec.Quota.GPUHours = resource.MustParse("-1")

			_, err := validator.ValidateUpdate(context.Background(), old, obj)
			Expect(err).To(HaveOccurred())
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
		})

		It("admits a valid update", func() {
			obj := validBudget()
			obj.Spec.Quota.GPUHours = resource.MustParse("250")
			old := validBudget()

			warnings, err := validator.ValidateUpdate(context.Background(), old, obj)
			Expect(warnings).To(BeNil())
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("ValidateDelete", func() {
		It("is a no-op (finalizer owns teardown)", func() {
			warnings, err := validator.ValidateDelete(context.Background(), validBudget())
			Expect(warnings).To(BeNil())
			Expect(err).NotTo(HaveOccurred())
		})
	})
})
