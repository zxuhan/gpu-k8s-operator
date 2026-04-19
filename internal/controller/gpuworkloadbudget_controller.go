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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	budgetv1alpha1 "github.com/zxuhan/gpu-k8s-operator/api/v1alpha1"
	"github.com/zxuhan/gpu-k8s-operator/internal/accounting"
	"github.com/zxuhan/gpu-k8s-operator/internal/enforcement"
)

const (
	// finalizerName guards the CR so we can clear per-budget metric
	// series on delete. The reconciler itself is stateless — nothing
	// else needs teardown — but an unbounded cardinality leak in Prom
	// is an operator-hostile failure mode worth one extra Patch.
	finalizerName = "budget.zxuhan.dev/finalizer"

	// defaultRequeueInterval drives the rolling-window refresh when no
	// pod event arrives. 30s is the same cadence the README claims the
	// scrape runs at, keeping "what Prom sees" and "what the CR says"
	// within one tick of each other.
	defaultRequeueInterval = 30 * time.Second

	// overBudgetRequeueInterval is the tighter cadence used while the
	// budget is over quota. Grace-period expiry is evaluated on every
	// reconcile; 10s balances responsiveness against API server load —
	// eviction delays above that are indistinguishable from noise for a
	// quota whose window is measured in hours.
	overBudgetRequeueInterval = 10 * time.Second
)

// GPUWorkloadBudgetReconciler reconciles a GPUWorkloadBudget object.
type GPUWorkloadBudgetReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// Clock is overridable so envtest specs can pin "now" to a
	// deterministic instant. Production wires in clock.RealClock.
	Clock clock.PassiveClock
	// Clientset is the typed kubernetes client used by the Evict
	// enforcer to submit the policy/v1 Eviction subresource. Required
	// when spec.enforcement.action = Evict; nil is tolerated otherwise.
	Clientset kubernetes.Interface
	// Recorder emits Kubernetes Events against the GPUWorkloadBudget
	// when enforcement fires. Required when enforcement is enabled.
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=budget.zxuhan.dev,resources=gpuworkloadbudgets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=budget.zxuhan.dev,resources=gpuworkloadbudgets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=budget.zxuhan.dev,resources=gpuworkloadbudgets/finalizers,verbs=update
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=core,resources=pods/eviction,verbs=create
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch

// Reconcile evaluates a single GPUWorkloadBudget against the live pod
// state, writes the derived accounting back to .status, and requeues
// itself so the rolling window keeps advancing when no pod event fires.
//
// Errors update the Degraded condition rather than silently failing:
// an operator inspecting `kubectl describe gwb` should always see why
// the numbers are stale.
func (r *GPUWorkloadBudgetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx).WithValues("budget", req.NamespacedName)

	budget := &budgetv1alpha1.GPUWorkloadBudget{}
	if err := r.Get(ctx, req.NamespacedName, budget); err != nil {
		if apierrors.IsNotFound(err) {
			// Object is gone — DeleteFunc on the work queue already
			// scrubbed metrics via the finalizer. Nothing else to do.
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("fetch budget: %w", err)
	}

	if !budget.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, budget)
	}

	if !controllerutil.ContainsFinalizer(budget, finalizerName) {
		controllerutil.AddFinalizer(budget, finalizerName)
		if err := r.Update(ctx, budget); err != nil {
			return ctrl.Result{}, fmt.Errorf("add finalizer: %w", err)
		}
		// Fall through — we have the object already and can reconcile
		// its status in the same pass. Rescheduling would just add a
		// round-trip.
	}

	now := r.now()
	selector, err := metav1.LabelSelectorAsSelector(&budget.Spec.Selector)
	if err != nil {
		log.Error(err, "invalid selector")
		return r.markDegradedAndRequeue(ctx, budget, "InvalidSelector", err.Error())
	}

	pods := &corev1.PodList{}
	if err := r.List(ctx, pods, client.InNamespace(budget.Namespace), client.MatchingLabelsSelector{Selector: selector}); err != nil {
		log.Error(err, "list pods for budget")
		return r.markDegradedAndRequeue(ctx, budget, "ListPodsFailed", err.Error())
	}

	accPods := make([]accounting.Pod, 0, len(pods.Items))
	for i := range pods.Items {
		p, ok := podToAccounting(&pods.Items[i], gpuResourceName(budget))
		if !ok {
			continue
		}
		accPods = append(accPods, p)
	}

	result := accounting.Budget{
		Window: time.Duration(budget.Spec.Quota.WindowHours) * time.Hour,
		Quota:  budget.Spec.Quota.GPUHours.AsApproximateFloat64(),
	}.Compute(now, accPods)

	if err := r.writeStatus(ctx, budget, result, now); err != nil {
		return ctrl.Result{}, err
	}

	r.publishMetrics(budget, result)

	if result.Over {
		if err := r.maybeEnforce(ctx, budget, pods.Items, now); err != nil {
			log.Error(err, "enforcement failed")
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: overBudgetRequeueInterval}, nil
	}

	return ctrl.Result{RequeueAfter: defaultRequeueInterval}, nil
}

// finalize handles the deletion path: scrub the metric series, drop
// the finalizer so the API server can GC the object. Phase 4 will
// expand this to clean up pod annotations set by the Pause action.
func (r *GPUWorkloadBudgetReconciler) finalize(ctx context.Context, budget *budgetv1alpha1.GPUWorkloadBudget) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(budget, finalizerName) {
		return ctrl.Result{}, nil
	}
	clearBudgetMetrics(budget.Namespace, budget.Name)
	controllerutil.RemoveFinalizer(budget, finalizerName)
	if err := r.Update(ctx, budget); err != nil {
		return ctrl.Result{}, fmt.Errorf("remove finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

// writeStatus maps the accounting Result onto status fields and patches
// the subresource. A Patch (rather than Update) avoids spurious 409s
// when a parallel reconcile races to update the same object.
//
// now is threaded in so the LastTransitionTime on condition flips is
// driven by the overridable clock — envtest specs assert exact
// grace-period expiry by comparing LTT to the pinned reconcileT0.
func (r *GPUWorkloadBudgetReconciler) writeStatus(ctx context.Context, budget *budgetv1alpha1.GPUWorkloadBudget, result accounting.Result, now time.Time) error {
	patch := client.MergeFrom(budget.DeepCopy())
	ltt := metav1.NewTime(now)

	budget.Status.ConsumedGPUHours = gpuHoursToQuantity(result.Consumed)
	budget.Status.RemainingGPUHours = gpuHoursToQuantity(result.Remaining)
	// int32 is the CRD type; TrackedPods fits comfortably in the range
	// any sane cluster will ever produce.
	// nolint:gosec // see above
	budget.Status.TrackedPods = int32(result.TrackedPods)
	budget.Status.ObservedGeneration = budget.Generation

	meta.SetStatusCondition(&budget.Status.Conditions, metav1.Condition{
		Type:               budgetv1alpha1.ConditionReady,
		Status:             metav1.ConditionTrue,
		Reason:             "Reconciled",
		Message:            "Accounting computed successfully.",
		LastTransitionTime: ltt,
	})
	quotaExceededStatus := metav1.ConditionFalse
	quotaExceededReason := "WithinBudget"
	quotaExceededMessage := fmt.Sprintf("Consumed %.3f of %.3f GPU-hours.", result.Consumed, result.Consumed+result.Remaining)
	if result.Over {
		quotaExceededStatus = metav1.ConditionTrue
		quotaExceededReason = "QuotaReached"
		quotaExceededMessage = fmt.Sprintf("Consumed %.3f GPU-hours meets or exceeds quota.", result.Consumed)
	}
	meta.SetStatusCondition(&budget.Status.Conditions, metav1.Condition{
		Type:               budgetv1alpha1.ConditionQuotaExceeded,
		Status:             quotaExceededStatus,
		Reason:             quotaExceededReason,
		Message:            quotaExceededMessage,
		LastTransitionTime: ltt,
	})
	// A successful accounting cycle clears any prior Degraded flag.
	meta.SetStatusCondition(&budget.Status.Conditions, metav1.Condition{
		Type:               budgetv1alpha1.ConditionDegraded,
		Status:             metav1.ConditionFalse,
		Reason:             "ReconcileSucceeded",
		Message:            "Controller reconciled the budget without error.",
		LastTransitionTime: ltt,
	})

	if err := r.Status().Patch(ctx, budget, patch); err != nil {
		return fmt.Errorf("patch status: %w", err)
	}
	return nil
}

// markDegradedAndRequeue records a user-facing reason on the CR when
// the reconciler can't produce a meaningful accounting — typically a
// malformed selector or an API server outage. A short requeue lets the
// controller recover as soon as the environment heals, without holding
// the work queue.
func (r *GPUWorkloadBudgetReconciler) markDegradedAndRequeue(ctx context.Context, budget *budgetv1alpha1.GPUWorkloadBudget, reason, message string) (ctrl.Result, error) {
	patch := client.MergeFrom(budget.DeepCopy())
	meta.SetStatusCondition(&budget.Status.Conditions, metav1.Condition{
		Type:    budgetv1alpha1.ConditionDegraded,
		Status:  metav1.ConditionTrue,
		Reason:  reason,
		Message: message,
	})
	meta.SetStatusCondition(&budget.Status.Conditions, metav1.Condition{
		Type:    budgetv1alpha1.ConditionReady,
		Status:  metav1.ConditionFalse,
		Reason:  reason,
		Message: message,
	})
	budget.Status.ObservedGeneration = budget.Generation
	if err := r.Status().Patch(ctx, budget, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch degraded status: %w", err)
	}
	return ctrl.Result{RequeueAfter: defaultRequeueInterval}, nil
}

// maybeEnforce is the post-status-write enforcement gate. It decides
// whether the operator acts this tick based on three inputs:
//
//  1. The QuotaExceeded condition must be True — writeStatus just set
//     it, so result.Over alone is enough; the condition lookup guards
//     against a future refactor.
//  2. now must be at-or-past LastTransitionTime + gracePeriodSeconds.
//     Grace derives from the condition's LTT, not from a separate
//     status field — that way pod churn that drops us briefly below
//     quota and back above it legitimately re-starts the clock.
//  3. A worst-offender pod must exist among the matched set. Pending
//     pods, already-terminating pods, and pods with zero window
//     contribution are filtered out.
//
// When all three hold, the configured Enforcer runs. Successful
// invocations bump the enforcement counter and stamp
// status.LastEnforcementAt — even AlertOnly, which does not mutate a
// pod, records the attempt so operators can audit cadence.
func (r *GPUWorkloadBudgetReconciler) maybeEnforce(ctx context.Context, budget *budgetv1alpha1.GPUWorkloadBudget, pods []corev1.Pod, now time.Time) error {
	cond := meta.FindStatusCondition(budget.Status.Conditions, budgetv1alpha1.ConditionQuotaExceeded)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		return nil
	}
	grace := time.Duration(budget.Spec.Enforcement.GracePeriodSeconds) * time.Second
	if now.Before(cond.LastTransitionTime.Add(grace)) {
		return nil
	}

	window := time.Duration(budget.Spec.Quota.WindowHours) * time.Hour
	from := now.Add(-window)
	gpuRes := gpuResourceName(budget)

	offender := pickWorstOffender(pods, gpuRes, from, now)
	if offender == nil {
		return nil
	}

	enforcer := enforcement.New(budget.Spec.Enforcement.Action, r.Client, r.Clientset, r.Recorder)
	outcome, err := enforcer.Enforce(ctx, budget, offender, now)
	if err != nil {
		return fmt.Errorf("enforce %s: %w", budget.Spec.Enforcement.Action, err)
	}

	enforcementActionsTotal.WithLabelValues(budget.Namespace, budget.Name, string(outcome.Action)).Inc()

	patch := client.MergeFrom(budget.DeepCopy())
	t := metav1.NewTime(now)
	budget.Status.LastEnforcementAt = &t
	if err := r.Status().Patch(ctx, budget, patch); err != nil {
		return fmt.Errorf("patch last enforcement time: %w", err)
	}
	return nil
}

// pickWorstOffender returns the pod that contributed the most GPU-hours
// to the current window, breaking ties alphabetically on pod name for
// determinism (so envtest assertions and human muscle memory agree on
// "which pod will the operator evict next?"). Pods with a
// DeletionTimestamp are skipped — enforcing against a pod that's
// already terminating would waste an event, and the actual consumption
// will resolve naturally as kubelet finishes teardown.
func pickWorstOffender(pods []corev1.Pod, gpuRes corev1.ResourceName, from, now time.Time) *corev1.Pod {
	var best *corev1.Pod
	var bestHours float64
	for i := range pods {
		p := &pods[i]
		if p.DeletionTimestamp != nil {
			continue
		}
		accPod, ok := podToAccounting(p, gpuRes)
		if !ok {
			continue
		}
		hours := accounting.WindowUsage(accPod, from, now)
		if hours <= 0 {
			continue
		}
		if best == nil || hours > bestHours || (hours == bestHours && p.Name < best.Name) {
			best = p
			bestHours = hours
		}
	}
	return best
}

// publishMetrics mirrors the Result onto the Prometheus gauges.
// gwb_consumed_gpu_hours and gwb_remaining_gpu_hours give the same
// numbers as the CR status — duplicated deliberately so an operator
// can alert on them without a custom-resource exporter.
func (r *GPUWorkloadBudgetReconciler) publishMetrics(budget *budgetv1alpha1.GPUWorkloadBudget, result accounting.Result) {
	consumedGPUHours.WithLabelValues(budget.Namespace, budget.Name).Set(result.Consumed)
	remainingGPUHours.WithLabelValues(budget.Namespace, budget.Name).Set(result.Remaining)
	trackedPods.WithLabelValues(budget.Namespace, budget.Name).Set(float64(result.TrackedPods))
}

// now returns the current instant, via the overridable clock if one is
// configured. Envtest specs pin the clock so they can assert against
// exact GPU-hour numbers without racing the real time.
func (r *GPUWorkloadBudgetReconciler) now() time.Time {
	if r.Clock != nil {
		return r.Clock.Now().UTC()
	}
	return time.Now().UTC()
}

// SetupWithManager wires up the CR reconcile and a secondary pod watch.
// The pod watch uses a map function to translate a pod event into the
// set of budgets whose selector picks that pod up — that's what lets
// the controller react to pod termination inside a window without
// polling every pod on every reconcile.
func (r *GPUWorkloadBudgetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&budgetv1alpha1.GPUWorkloadBudget{}).
		Watches(
			&corev1.Pod{},
			handler.EnqueueRequestsFromMapFunc(r.mapPodToBudgets),
		).
		Named("gpuworkloadbudget").
		Complete(r)
}

// mapPodToBudgets finds the budgets whose selector admits this pod and
// enqueues a reconcile for each. Budgets in other namespaces are
// skipped — a GPUWorkloadBudget is namespaced by design.
func (r *GPUWorkloadBudgetReconciler) mapPodToBudgets(ctx context.Context, obj client.Object) []reconcile.Request {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return nil
	}
	budgets := &budgetv1alpha1.GPUWorkloadBudgetList{}
	if err := r.List(ctx, budgets, client.InNamespace(pod.Namespace)); err != nil {
		logf.FromContext(ctx).Error(err, "list budgets for pod mapping", "pod", client.ObjectKeyFromObject(pod))
		return nil
	}
	podLabels := labels.Set(pod.Labels)
	var out []reconcile.Request
	for i := range budgets.Items {
		b := &budgets.Items[i]
		selector, err := metav1.LabelSelectorAsSelector(&b.Spec.Selector)
		if err != nil {
			continue
		}
		if !selector.Matches(podLabels) {
			continue
		}
		out = append(out, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: b.Namespace, Name: b.Name}})
	}
	return out
}

// gpuResourceName returns the configured GPU resource, falling back to
// the nvidia.com/gpu default when the spec hasn't been defaulted yet
// (e.g. in envtest where the CRD default may not have been applied).
func gpuResourceName(budget *budgetv1alpha1.GPUWorkloadBudget) corev1.ResourceName {
	if budget.Spec.GPUResourceName == "" {
		return corev1.ResourceName("nvidia.com/gpu")
	}
	return budget.Spec.GPUResourceName
}

// gpuHoursToQuantity converts a float GPU-hour count to the CRD's
// resource.Quantity, rounded to the nearest millihour. 1 millihour is
// 3.6 seconds of one-GPU work — finer precision than any real-world
// quota cares about, and it keeps the serialized string short (e.g.
// "12345m" rather than "12.345678").
func gpuHoursToQuantity(h float64) resource.Quantity {
	if h < 0 {
		h = 0
	}
	milli := int64(h*1000 + 0.5)
	return *resource.NewMilliQuantity(milli, resource.DecimalSI)
}
