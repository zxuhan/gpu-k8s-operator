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

	budgetv1alpha1 "github.com/zxuhan/gpu-k8s-operator/api/v1alpha1"
)

// alerter emits a Warning event against the budget naming the current
// worst offender but does not modify the pod. It's the operator's
// "dry-run" mode: the metric counter records that enforcement *would*
// have fired, so Prometheus dashboards look identical to production
// rehearsals of Evict/Pause.
type alerter struct {
	recorder record.EventRecorder
}

func (a *alerter) Enforce(_ context.Context, budget *budgetv1alpha1.GPUWorkloadBudget, pod *corev1.Pod, _ time.Time) (Outcome, error) {
	msg := fmt.Sprintf("Quota exceeded; AlertOnly mode — no pods modified. Top offender: %s.", podKey(pod))
	a.recorder.Event(budget, corev1.EventTypeWarning, "QuotaExceeded", msg)
	return Outcome{
		Action:  budgetv1alpha1.ActionAlertOnly,
		Acted:   false,
		PodKey:  podKey(pod),
		Reason:  "QuotaExceeded",
		Message: msg,
	}, nil
}
