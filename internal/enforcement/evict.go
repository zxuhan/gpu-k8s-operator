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

package enforcement

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/record"

	budgetv1alpha1 "github.com/zxuhan/gpu-k8s-operator/api/v1alpha1"
)

// evictor submits a policy/v1 Eviction against the offender. Using the
// Eviction subresource (rather than a raw pod delete) is deliberate:
// the apiserver consults any matching PodDisruptionBudget before
// accepting the eviction, so the operator cannot wedge a system that
// depends on PDBs for safety.
type evictor struct {
	clientset kubernetes.Interface
	recorder  record.EventRecorder
}

func (e *evictor) Enforce(ctx context.Context, budget *budgetv1alpha1.GPUWorkloadBudget, pod *corev1.Pod, _ time.Time) (Outcome, error) {
	if pod.DeletionTimestamp != nil {
		// Already on its way out. Re-submitting the eviction would at
		// best be a no-op and at worst produce a confusing log line.
		return Outcome{
			Action:  budgetv1alpha1.ActionEvict,
			Acted:   false,
			PodKey:  podKey(pod),
			Reason:  "AlreadyTerminating",
			Message: fmt.Sprintf("Pod %s is already terminating.", podKey(pod)),
		}, nil
	}

	grace := int64(budget.Spec.Enforcement.GracePeriodSeconds)
	eviction := &policyv1.Eviction{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pod.Name,
			Namespace: pod.Namespace,
		},
		DeleteOptions: &metav1.DeleteOptions{GracePeriodSeconds: &grace},
	}

	if err := e.clientset.PolicyV1().Evictions(pod.Namespace).Evict(ctx, eviction); err != nil {
		switch {
		case apierrors.IsNotFound(err):
			// Raced with deletion from some other controller. Report as
			// a non-error skip so the reconciler doesn't back off and
			// re-queue tightly.
			return Outcome{
				Action:  budgetv1alpha1.ActionEvict,
				Acted:   false,
				PodKey:  podKey(pod),
				Reason:  "PodAlreadyGone",
				Message: fmt.Sprintf("Pod %s disappeared before eviction completed.", podKey(pod)),
			}, nil
		case apierrors.IsTooManyRequests(err):
			// PDB denied the eviction. Surface to the operator via an
			// event and let the reconciler retry on its next tick —
			// the PDB may relax as other pods finish.
			msg := fmt.Sprintf("Eviction of %s blocked by PodDisruptionBudget; will retry.", podKey(pod))
			e.recorder.Event(budget, corev1.EventTypeWarning, "EvictionBlocked", msg)
			return Outcome{
				Action:  budgetv1alpha1.ActionEvict,
				Acted:   false,
				PodKey:  podKey(pod),
				Reason:  "EvictionBlocked",
				Message: msg,
			}, nil
		default:
			return Outcome{}, fmt.Errorf("evict %s: %w", podKey(pod), err)
		}
	}

	msg := fmt.Sprintf("Evicted pod %s after quota exceeded.", podKey(pod))
	e.recorder.Event(budget, corev1.EventTypeWarning, "Evicted", msg)
	return Outcome{
		Action:  budgetv1alpha1.ActionEvict,
		Acted:   true,
		PodKey:  podKey(pod),
		Reason:  "Evicted",
		Message: msg,
	}, nil
}
