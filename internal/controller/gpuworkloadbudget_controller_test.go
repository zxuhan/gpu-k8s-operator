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
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	testclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	budgetv1alpha1 "github.com/zxuhan/gpu-k8s-operator/api/v1alpha1"
)

// reconcileT0 is the pinned "now" every reconciler spec evaluates against.
// Using a fixed clock lets assertions compare against exact GPU-hour
// numbers rather than floating-point windows.
var reconcileT0 = time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC)

// nolint:unparam // windowHours is always 24 today, but Phase 4 enforcement tests vary it.
func newBudget(name, namespace string, gpuHours string, windowHours int32, team string) *budgetv1alpha1.GPUWorkloadBudget {
	return &budgetv1alpha1.GPUWorkloadBudget{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: budgetv1alpha1.GPUWorkloadBudgetSpec{
			Selector: metav1.LabelSelector{MatchLabels: map[string]string{"gpu-budget/team": team}},
			Quota: budgetv1alpha1.Quota{
				GPUHours:    resource.MustParse(gpuHours),
				WindowHours: windowHours,
			},
			Enforcement: budgetv1alpha1.Enforcement{
				Action:             budgetv1alpha1.ActionAlertOnly,
				GracePeriodSeconds: 30,
			},
			GPUResourceName: corev1.ResourceName("nvidia.com/gpu"),
		},
	}
}

// createRunningGPUPod installs a pod with the team label, a single
// container requesting gpus units of nvidia.com/gpu, and a ContainerStatus
// claiming it started startHoursAgo ago relative to reconcileT0. Status
// is written as a second step because k8sClient.Create drops
// .status on initial submission.
// nolint:unparam // returned *Pod is unused today; terminated-pod specs in Phase 4 will patch its status.
func createRunningGPUPod(ctx context.Context, c client.Client, namespace, name, team string, gpus int, startHoursAgo float64) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    map[string]string{"gpu-budget/team": team},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:  "train",
				Image: "busybox",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceName("nvidia.com/gpu"): *resource.NewQuantity(int64(gpus), resource.DecimalSI)},
					Limits:   corev1.ResourceList{corev1.ResourceName("nvidia.com/gpu"): *resource.NewQuantity(int64(gpus), resource.DecimalSI)},
				},
			}},
		},
	}
	Expect(c.Create(ctx, pod)).To(Succeed())

	started := reconcileT0.Add(-time.Duration(startHoursAgo * float64(time.Hour)))
	pod.Status = corev1.PodStatus{
		Phase:     corev1.PodRunning,
		StartTime: &metav1.Time{Time: started},
		ContainerStatuses: []corev1.ContainerStatus{{
			Name:  "train",
			State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(started)}},
		}},
	}
	Expect(c.Status().Update(ctx, pod)).To(Succeed())
	return pod
}

// fetchCondition pulls the typed condition by type from status, or
// returns nil if missing. Keeps assertion blocks compact.
func fetchCondition(budget *budgetv1alpha1.GPUWorkloadBudget, condType string) *metav1.Condition {
	return meta.FindStatusCondition(budget.Status.Conditions, condType)
}

var _ = Describe("GPUWorkloadBudget Controller", func() {
	var (
		ctx        context.Context
		namespace  string
		budgetKey  types.NamespacedName
		reconciler *GPUWorkloadBudgetReconciler
	)

	// Each spec runs in its own fresh namespace. envtest keeps state
	// across tests by default, so a "delete in AfterEach" dance is
	// easier to get wrong than a cheap namespace-per-spec.
	BeforeEach(func() {
		ctx = context.Background()
		namespace = fmt.Sprintf("recon-%d", time.Now().UnixNano())
		Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})).To(Succeed())

		reconciler = &GPUWorkloadBudgetReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
			Clock:  testclock.NewFakePassiveClock(reconcileT0),
		}
	})

	AfterEach(func() {
		ns := &corev1.Namespace{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: namespace}, ns); err == nil {
			Expect(k8sClient.Delete(ctx, ns)).To(Succeed())
		}
	})

	It("populates status with zero consumption when no pods match", func() {
		budget := newBudget("empty", namespace, "100", 24, "vision")
		Expect(k8sClient.Create(ctx, budget)).To(Succeed())
		budgetKey = client.ObjectKeyFromObject(budget)

		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: budgetKey})
		Expect(err).NotTo(HaveOccurred())

		got := &budgetv1alpha1.GPUWorkloadBudget{}
		Expect(k8sClient.Get(ctx, budgetKey, got)).To(Succeed())
		Expect(got.Status.TrackedPods).To(BeEquivalentTo(0))
		Expect(got.Status.ConsumedGPUHours.MilliValue()).To(BeEquivalentTo(0))
		Expect(got.Status.RemainingGPUHours.MilliValue()).To(BeEquivalentTo(100_000))

		ready := fetchCondition(got, budgetv1alpha1.ConditionReady)
		Expect(ready).NotTo(BeNil())
		Expect(ready.Status).To(Equal(metav1.ConditionTrue))

		over := fetchCondition(got, budgetv1alpha1.ConditionQuotaExceeded)
		Expect(over).NotTo(BeNil())
		Expect(over.Status).To(Equal(metav1.ConditionFalse))

		Expect(controllerutil.ContainsFinalizer(got, finalizerName)).To(BeTrue())
	})

	It("sums GPU-hours across matching pods and exposes them via status", func() {
		budget := newBudget("vision", namespace, "100", 24, "vision")
		Expect(k8sClient.Create(ctx, budget)).To(Succeed())
		budgetKey = client.ObjectKeyFromObject(budget)

		// 2 GPUs × 3h = 6, 4 GPUs × 1h = 4, 1 GPU outside window → 0.
		// Total: 10 GPU-hours.
		createRunningGPUPod(ctx, k8sClient, namespace, "pod-a", "vision", 2, 3)
		createRunningGPUPod(ctx, k8sClient, namespace, "pod-b", "vision", 4, 1)
		// Pod that started 30h ago but clipped to 24h window, so it
		// contributes 24 × 1 = 24 GPU-hours -> total 34. Test against that.
		createRunningGPUPod(ctx, k8sClient, namespace, "pod-c-clipped", "vision", 1, 30)
		// Pod in a different team — must be ignored.
		createRunningGPUPod(ctx, k8sClient, namespace, "pod-d-other-team", "speech", 8, 1)

		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: budgetKey})
		Expect(err).NotTo(HaveOccurred())

		got := &budgetv1alpha1.GPUWorkloadBudget{}
		Expect(k8sClient.Get(ctx, budgetKey, got)).To(Succeed())

		Expect(got.Status.TrackedPods).To(BeEquivalentTo(3), "should only count the three vision pods")
		// 2×3 + 4×1 + 1×24 = 34 GPU-hours.
		Expect(got.Status.ConsumedGPUHours.MilliValue()).To(BeEquivalentTo(34_000))
		Expect(got.Status.RemainingGPUHours.MilliValue()).To(BeEquivalentTo(66_000))
		Expect(got.Status.ObservedGeneration).To(Equal(got.Generation))
	})

	It("flips QuotaExceeded once consumed meets quota", func() {
		budget := newBudget("tight", namespace, "5", 24, "vision")
		Expect(k8sClient.Create(ctx, budget)).To(Succeed())
		budgetKey = client.ObjectKeyFromObject(budget)

		// 2 GPUs × 3h = 6 GPU-hours > 5 quota.
		createRunningGPUPod(ctx, k8sClient, namespace, "pod-a", "vision", 2, 3)

		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: budgetKey})
		Expect(err).NotTo(HaveOccurred())

		got := &budgetv1alpha1.GPUWorkloadBudget{}
		Expect(k8sClient.Get(ctx, budgetKey, got)).To(Succeed())

		Expect(got.Status.ConsumedGPUHours.MilliValue()).To(BeEquivalentTo(6_000))
		// Remaining floored at 0 even when over.
		Expect(got.Status.RemainingGPUHours.MilliValue()).To(BeEquivalentTo(0))
		over := fetchCondition(got, budgetv1alpha1.ConditionQuotaExceeded)
		Expect(over).NotTo(BeNil())
		Expect(over.Status).To(Equal(metav1.ConditionTrue))
	})

	It("cleans up finalizer on delete", func() {
		budget := newBudget("teardown", namespace, "10", 24, "vision")
		Expect(k8sClient.Create(ctx, budget)).To(Succeed())
		budgetKey = client.ObjectKeyFromObject(budget)

		// First reconcile adds the finalizer.
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: budgetKey})
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Delete(ctx, budget)).To(Succeed())

		// Reconcile sees DeletionTimestamp and drops the finalizer.
		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: budgetKey})
		Expect(err).NotTo(HaveOccurred())

		got := &budgetv1alpha1.GPUWorkloadBudget{}
		err = k8sClient.Get(ctx, budgetKey, got)
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), "object should be GC'd once finalizer is removed")
	})

	It("returns a successful zero result when the object is already gone", func() {
		_, err := reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: "missing", Namespace: namespace},
		})
		Expect(err).NotTo(HaveOccurred())
	})
})
