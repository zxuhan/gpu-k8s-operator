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
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"

	budgetv1alpha1 "github.com/zxuhan/gpu-k8s-operator/api/v1alpha1"
)

const (
	// AnnotationPausedAt is stamped with the RFC3339 timestamp of the
	// pause decision. Higher-level controllers (Argo Workflows, Kueue,
	// JobSet) are expected to watch this annotation and halt follow-up
	// work; this operator deliberately does not kill the running
	// container, so a pod that has already consumed resources isn't
	// discarded mid-flight.
	AnnotationPausedAt = "budget.zxuhan.dev/paused-at"
	// AnnotationPausedBy records the "namespace/name" of the budget
	// whose quota triggered the pause. Multiple budgets can match the
	// same pod; recording the attributing budget makes incident
	// archaeology tractable.
	AnnotationPausedBy = "budget.zxuhan.dev/paused-by"
)

// pauser stamps the two pause annotations onto the offender via a
// Merge patch — Updates would race with any concurrent scheduler or
// mutator touching the same pod. Idempotent: if the pod is already
// annotated, the enforcer reports Acted=false rather than re-stamping
// (and thereby spamming events and counter bumps).
type pauser struct {
	client   client.Client
	recorder record.EventRecorder
}

func (p *pauser) Enforce(ctx context.Context, budget *budgetv1alpha1.GPUWorkloadBudget, pod *corev1.Pod, now time.Time) (Outcome, error) {
	if _, already := pod.Annotations[AnnotationPausedAt]; already {
		return Outcome{
			Action:  budgetv1alpha1.ActionPause,
			Acted:   false,
			PodKey:  podKey(pod),
			Reason:  "AlreadyPaused",
			Message: fmt.Sprintf("Pod %s already carries the pause annotation.", podKey(pod)),
		}, nil
	}

	base := pod.DeepCopy()
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	pod.Annotations[AnnotationPausedAt] = now.UTC().Format(time.RFC3339)
	pod.Annotations[AnnotationPausedBy] = budget.Namespace + "/" + budget.Name

	if err := p.client.Patch(ctx, pod, client.MergeFrom(base)); err != nil {
		return Outcome{}, fmt.Errorf("stamp pause annotation on %s: %w", podKey(pod), err)
	}

	msg := fmt.Sprintf("Paused pod %s after quota exceeded.", podKey(pod))
	p.recorder.Event(budget, corev1.EventTypeWarning, "Paused", msg)
	return Outcome{
		Action:  budgetv1alpha1.ActionPause,
		Acted:   true,
		PodKey:  podKey(pod),
		Reason:  "Paused",
		Message: msg,
	}, nil
}
