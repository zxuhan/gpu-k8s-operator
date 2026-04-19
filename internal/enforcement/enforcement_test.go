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
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	kubefake "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"

	budgetv1alpha1 "github.com/zxuhan/gpu-k8s-operator/api/v1alpha1"
)

// enforceT0 is the pinned wall-clock the unit tests evaluate against.
var enforceT0 = time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC)

func newTestBudget(action budgetv1alpha1.EnforcementAction, grace int32) *budgetv1alpha1.GPUWorkloadBudget {
	return &budgetv1alpha1.GPUWorkloadBudget{
		ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "team-a"},
		Spec: budgetv1alpha1.GPUWorkloadBudgetSpec{
			Selector: metav1.LabelSelector{MatchLabels: map[string]string{"team": "a"}},
			Quota:    budgetv1alpha1.Quota{GPUHours: resource.MustParse("1"), WindowHours: 24},
			Enforcement: budgetv1alpha1.Enforcement{
				Action:             action,
				GracePeriodSeconds: grace,
			},
		},
	}
}

// nolint:unparam // name is always "p1" today; kept parameterized for a multi-pod spec planned for Phase 5 integration tests.
func newTestPod(name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "team-a",
			Labels:    map[string]string{"team": "a"},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "c", Image: "x"}},
		},
	}
}

func newK8sScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("corev1 scheme: %v", err)
	}
	if err := budgetv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("budget scheme: %v", err)
	}
	return s
}

func TestAlerter_EmitsEventAndReportsNotActed(t *testing.T) {
	rec := record.NewFakeRecorder(4)
	a := &alerter{recorder: rec}

	budget := newTestBudget(budgetv1alpha1.ActionAlertOnly, 0)
	pod := newTestPod("p1")

	out, err := a.Enforce(context.Background(), budget, pod, enforceT0)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if out.Acted {
		t.Fatalf("alerter must never mutate cluster state")
	}
	if out.Action != budgetv1alpha1.ActionAlertOnly {
		t.Fatalf("action: got %s want AlertOnly", out.Action)
	}
	if out.PodKey != "team-a/p1" {
		t.Fatalf("podKey: got %s", out.PodKey)
	}
	select {
	case evt := <-rec.Events:
		if !strings.Contains(evt, "QuotaExceeded") {
			t.Fatalf("event should include reason QuotaExceeded, got %q", evt)
		}
		if !strings.Contains(evt, "p1") {
			t.Fatalf("event should mention the offender pod, got %q", evt)
		}
	default:
		t.Fatalf("expected an event to be recorded")
	}
}

func TestPauser_StampsAnnotations(t *testing.T) {
	scheme := newK8sScheme(t)
	pod := newTestPod("p1")
	c := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build()
	rec := record.NewFakeRecorder(4)
	p := &pauser{client: c, recorder: rec}

	budget := newTestBudget(budgetv1alpha1.ActionPause, 0)

	out, err := p.Enforce(context.Background(), budget, pod, enforceT0)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !out.Acted {
		t.Fatalf("pauser should act on a never-before-paused pod")
	}

	got := &corev1.Pod{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(pod), got); err != nil {
		t.Fatalf("re-fetch pod: %v", err)
	}
	if got.Annotations[AnnotationPausedAt] != enforceT0.Format(time.RFC3339) {
		t.Fatalf("paused-at annotation wrong: %q", got.Annotations[AnnotationPausedAt])
	}
	if got.Annotations[AnnotationPausedBy] != "team-a/b" {
		t.Fatalf("paused-by annotation wrong: %q", got.Annotations[AnnotationPausedBy])
	}
}

func TestPauser_IdempotentOnAlreadyPaused(t *testing.T) {
	scheme := newK8sScheme(t)
	pod := newTestPod("p1")
	pod.Annotations = map[string]string{AnnotationPausedAt: "2026-04-19T10:00:00Z"}
	c := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build()
	rec := record.NewFakeRecorder(4)
	p := &pauser{client: c, recorder: rec}

	budget := newTestBudget(budgetv1alpha1.ActionPause, 0)

	out, err := p.Enforce(context.Background(), budget, pod, enforceT0)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if out.Acted {
		t.Fatalf("pauser must be idempotent when annotation already present")
	}
	if out.Reason != "AlreadyPaused" {
		t.Fatalf("reason: got %q", out.Reason)
	}
	select {
	case <-rec.Events:
		t.Fatalf("pauser should not emit an event on no-op")
	default:
	}
}

func TestEvictor_SubmitsEviction(t *testing.T) {
	pod := newTestPod("p1")
	cs := kubefake.NewClientset(pod)
	rec := record.NewFakeRecorder(4)
	e := &evictor{clientset: cs, recorder: rec}

	budget := newTestBudget(budgetv1alpha1.ActionEvict, 30)

	out, err := e.Enforce(context.Background(), budget, pod, enforceT0)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !out.Acted {
		t.Fatalf("evictor should act on a fresh running pod; outcome=%+v", out)
	}

	// The fake clientset records the Create on the /eviction subresource.
	var sawEvict bool
	var grace *int64
	for _, act := range cs.Actions() {
		if act.GetVerb() == "create" && act.GetResource().Resource == "pods" && act.GetSubresource() == "eviction" {
			sawEvict = true
			ca, ok := act.(clienttesting.CreateAction)
			if !ok {
				t.Fatalf("create action type: got %T", act)
			}
			ev, ok := ca.GetObject().(*policyv1.Eviction)
			if !ok {
				t.Fatalf("eviction object type: got %T", ca.GetObject())
			}
			if ev.DeleteOptions != nil {
				grace = ev.DeleteOptions.GracePeriodSeconds
			}
		}
	}
	if !sawEvict {
		t.Fatalf("no eviction subresource create recorded")
	}
	if grace == nil || *grace != 30 {
		t.Fatalf("grace period: got %v want 30", grace)
	}
}

func TestEvictor_SkipsTerminatingPod(t *testing.T) {
	cs := kubefake.NewClientset()
	rec := record.NewFakeRecorder(4)
	e := &evictor{clientset: cs, recorder: rec}

	pod := newTestPod("p1")
	now := metav1.NewTime(enforceT0)
	pod.DeletionTimestamp = &now
	budget := newTestBudget(budgetv1alpha1.ActionEvict, 30)

	out, err := e.Enforce(context.Background(), budget, pod, enforceT0)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if out.Acted {
		t.Fatalf("evictor must skip already-terminating pods")
	}
	if out.Reason != "AlreadyTerminating" {
		t.Fatalf("reason: got %q", out.Reason)
	}
	for _, act := range cs.Actions() {
		if act.GetSubresource() == "eviction" {
			t.Fatalf("no eviction call should have been made for a terminating pod")
		}
	}
}

func TestEvictor_RecoversFromPDBBlock(t *testing.T) {
	cs := kubefake.NewClientset()
	// Prepend a reactor that returns 429 TooManyRequests on evictions,
	// simulating a PodDisruptionBudget denying the request.
	cs.PrependReactor("create", "pods/eviction", func(_ clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewTooManyRequests("blocked by PDB", 10)
	})
	rec := record.NewFakeRecorder(4)
	e := &evictor{clientset: cs, recorder: rec}

	pod := newTestPod("p1")
	budget := newTestBudget(budgetv1alpha1.ActionEvict, 30)

	out, err := e.Enforce(context.Background(), budget, pod, enforceT0)
	if err != nil {
		t.Fatalf("429 must surface as an outcome, not an error: %v", err)
	}
	if out.Acted {
		t.Fatalf("blocked eviction must report Acted=false")
	}
	if out.Reason != "EvictionBlocked" {
		t.Fatalf("reason: got %q", out.Reason)
	}
	select {
	case evt := <-rec.Events:
		if !strings.Contains(evt, "EvictionBlocked") {
			t.Fatalf("expected EvictionBlocked event, got %q", evt)
		}
	default:
		t.Fatalf("expected a blocked-eviction event")
	}
}

func TestEvictor_RecoversFromMissingPod(t *testing.T) {
	cs := kubefake.NewClientset()
	cs.PrependReactor("create", "pods/eviction", func(_ clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, "p1")
	})
	rec := record.NewFakeRecorder(4)
	e := &evictor{clientset: cs, recorder: rec}

	pod := newTestPod("p1")
	budget := newTestBudget(budgetv1alpha1.ActionEvict, 30)

	out, err := e.Enforce(context.Background(), budget, pod, enforceT0)
	if err != nil {
		t.Fatalf("NotFound must surface as an outcome, not an error: %v", err)
	}
	if out.Acted || out.Reason != "PodAlreadyGone" {
		t.Fatalf("unexpected outcome: %+v", out)
	}
}

func TestNew_DefaultsToAlertOnlyForUnknownAction(t *testing.T) {
	rec := record.NewFakeRecorder(1)
	// kubernetes.Interface is nil-safe here because New doesn't invoke it;
	// the default branch returns an alerter that ignores the clientset.
	var cs kubernetes.Interface
	scheme := newK8sScheme(t)
	c := fakeclient.NewClientBuilder().WithScheme(scheme).Build()

	got := New(budgetv1alpha1.EnforcementAction("FutureAction"), c, cs, rec)
	if _, ok := got.(*alerter); !ok {
		t.Fatalf("unknown action should fall back to alerter, got %T", got)
	}
}
