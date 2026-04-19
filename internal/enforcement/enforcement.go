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

// Package enforcement applies the action configured on a GPUWorkloadBudget
// to the worst-offending pod once the rolling-window quota is exceeded
// and the grace period has elapsed.
//
// The package is deliberately small: one Enforcer per action, each
// idempotent under re-entry (the reconciler may see the same offender
// on the next tick before cluster state settles). The reconciler handles
// offender ranking, grace-period bookkeeping, and metric bumping — the
// Enforcer just performs the external side effect and reports what it
// did.
package enforcement

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"

	budgetv1alpha1 "github.com/zxuhan/gpu-k8s-operator/api/v1alpha1"
)

// Enforcer applies one enforcement action per call, against a single
// worst-offender pod. Implementations must be safe to re-invoke with the
// same pod — the reconciler requeues every few seconds while the budget
// is Over, and until cluster state catches up the same pod may be
// nominated again.
type Enforcer interface {
	Enforce(ctx context.Context, budget *budgetv1alpha1.GPUWorkloadBudget, pod *corev1.Pod, now time.Time) (Outcome, error)
}

// Outcome describes what the Enforcer actually did. Separating Acted
// (cluster state mutated) from the call returning nil error lets the
// reconciler distinguish "blocked by PDB, will retry" from "nothing to
// do, already evicted" without plumbing typed errors through the
// interface.
type Outcome struct {
	// Action is the configured enforcement action. Echoed back so the
	// caller doesn't need to re-read spec.enforcement.action.
	Action budgetv1alpha1.EnforcementAction
	// Acted is true when the enforcer mutated external state on this
	// call (eviction submitted, annotation stamped). False means the
	// call was a no-op — pod already terminating, already paused,
	// eviction blocked by PDB — details live in Reason/Message.
	Acted bool
	// PodKey is the targeted pod as "namespace/name", for logging.
	PodKey string
	// Reason is a short machine-friendly token, event-compatible.
	Reason string
	// Message is the human-readable event body.
	Message string
}

// New constructs the Enforcer matching action. Unknown actions fall
// back to AlertOnly — the CRD enum validator rejects anything outside
// the three known values, but the defensive default guards against a
// future API bump that adds a value the running operator doesn't know.
func New(action budgetv1alpha1.EnforcementAction, c client.Client, clientset kubernetes.Interface, recorder record.EventRecorder) Enforcer {
	switch action {
	case budgetv1alpha1.ActionEvict:
		return &evictor{clientset: clientset, recorder: recorder}
	case budgetv1alpha1.ActionPause:
		return &pauser{client: c, recorder: recorder}
	default:
		return &alerter{recorder: recorder}
	}
}

// podKey is the "namespace/name" form used in Outcome.PodKey and in
// log lines. Kept as a helper so the format stays consistent across
// all three enforcers.
func podKey(pod *corev1.Pod) string {
	return pod.Namespace + "/" + pod.Name
}
