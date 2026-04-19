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
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	budgetv1alpha1 "github.com/zxuhan/gpu-k8s-operator/api/v1alpha1"
)

var _ = Describe("GPUWorkloadBudget Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-resource"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default", // TODO(user):Modify as needed
		}
		gpuworkloadbudget := &budgetv1alpha1.GPUWorkloadBudget{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind GPUWorkloadBudget")
			err := k8sClient.Get(ctx, typeNamespacedName, gpuworkloadbudget)
			if err != nil && errors.IsNotFound(err) {
				obj := &budgetv1alpha1.GPUWorkloadBudget{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					Spec: budgetv1alpha1.GPUWorkloadBudgetSpec{
						Selector: metav1.LabelSelector{
							MatchLabels: map[string]string{"gpu-budget/team": "test"},
						},
						Quota: budgetv1alpha1.Quota{
							GPUHours:    resource.MustParse("10"),
							WindowHours: 24,
						},
						Enforcement: budgetv1alpha1.Enforcement{
							Action:             budgetv1alpha1.ActionAlertOnly,
							GracePeriodSeconds: 30,
						},
					},
				}
				Expect(k8sClient.Create(ctx, obj)).To(Succeed())
			}
		})

		AfterEach(func() {
			obj := &budgetv1alpha1.GPUWorkloadBudget{}
			err := k8sClient.Get(ctx, typeNamespacedName, obj)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance GPUWorkloadBudget")
			Expect(k8sClient.Delete(ctx, obj)).To(Succeed())
		})
		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			controllerReconciler := &GPUWorkloadBudgetReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			// TODO(user): Add more specific assertions depending on your controller's reconciliation logic.
			// Example: If you expect a certain status condition after reconciliation, verify it here.
		})
	})
})
