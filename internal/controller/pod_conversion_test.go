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
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

var convT0 = time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC)

const nvidiaGPU = corev1.ResourceName("nvidia.com/gpu")

func containerWithGPU(name string, count string) corev1.Container {
	return corev1.Container{
		Name: name,
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				nvidiaGPU: resource.MustParse(count),
			},
		},
	}
}

func TestPodToAccounting_RunningPod(t *testing.T) {
	start := convT0.Add(-2 * time.Hour)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{UID: types.UID("uid-1")},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{containerWithGPU("train", "2")},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:  "train",
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(start)}},
			}},
		},
	}
	got, ok := podToAccounting(pod, nvidiaGPU)
	if !ok {
		t.Fatalf("expected ok=true for running GPU pod")
	}
	if got.ID != "uid-1" {
		t.Errorf("ID = %q; want uid-1", got.ID)
	}
	if !got.Start.Equal(start) {
		t.Errorf("Start = %v; want %v", got.Start, start)
	}
	if got.End != nil {
		t.Errorf("End = %v; want nil", *got.End)
	}
	if got.GPUs != 2 {
		t.Errorf("GPUs = %v; want 2", got.GPUs)
	}
}

func TestPodToAccounting_SucceededPod(t *testing.T) {
	start := convT0.Add(-5 * time.Hour)
	end := convT0.Add(-3 * time.Hour)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{UID: types.UID("uid-done")},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{containerWithGPU("train", "1")},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodSucceeded,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: "train",
				State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
					StartedAt:  metav1.NewTime(start),
					FinishedAt: metav1.NewTime(end),
				}},
			}},
		},
	}
	got, ok := podToAccounting(pod, nvidiaGPU)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if got.End == nil {
		t.Fatalf("End = nil; want %v", end)
	}
	if !got.End.Equal(end) {
		t.Errorf("End = %v; want %v", *got.End, end)
	}
	if !got.Start.Equal(start) {
		t.Errorf("Start = %v; want %v", got.Start, start)
	}
}

func TestPodToAccounting_SumsMultipleContainers(t *testing.T) {
	start := convT0.Add(-1 * time.Hour)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{UID: types.UID("multi")},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				containerWithGPU("a", "1"),
				containerWithGPU("b", "3"),
			},
		},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "a", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(start)}}},
				{Name: "b", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(start.Add(30 * time.Second))}}},
			},
		},
	}
	got, ok := podToAccounting(pod, nvidiaGPU)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if got.GPUs != 4 {
		t.Errorf("GPUs = %v; want 4", got.GPUs)
	}
	if !got.Start.Equal(start) {
		t.Errorf("Start = %v; want earliest %v", got.Start, start)
	}
}

func TestPodToAccounting_NoGPURequestsSkipped(t *testing.T) {
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "cpu-only"}},
		},
		Status: corev1.PodStatus{
			Phase:     corev1.PodRunning,
			StartTime: &metav1.Time{Time: convT0.Add(-time.Hour)},
		},
	}
	if _, ok := podToAccounting(pod, nvidiaGPU); ok {
		t.Fatal("expected ok=false for a pod that does not request GPUs")
	}
}

func TestPodToAccounting_PendingPodSkipped(t *testing.T) {
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{containerWithGPU("train", "1")},
		},
		Status: corev1.PodStatus{Phase: corev1.PodPending},
	}
	if _, ok := podToAccounting(pod, nvidiaGPU); ok {
		t.Fatal("expected ok=false when container has no StartedAt")
	}
}

func TestPodToAccounting_FallsBackToPodStartTime(t *testing.T) {
	start := convT0.Add(-10 * time.Minute)
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{containerWithGPU("train", "1")},
		},
		Status: corev1.PodStatus{
			Phase:     corev1.PodRunning,
			StartTime: &metav1.Time{Time: start},
			// ContainerStatuses intentionally empty — pod accepted, container not yet running.
		},
	}
	got, ok := podToAccounting(pod, nvidiaGPU)
	if !ok {
		t.Fatal("expected ok=true via StartTime fallback")
	}
	if !got.Start.Equal(start) {
		t.Errorf("Start = %v; want %v", got.Start, start)
	}
}

func TestPodToAccounting_FailedPodWithoutFinishedAtUsesDeletion(t *testing.T) {
	start := convT0.Add(-2 * time.Hour)
	deletion := convT0.Add(-30 * time.Minute)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			UID:               types.UID("crashed"),
			DeletionTimestamp: &metav1.Time{Time: deletion},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{containerWithGPU("train", "1")},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodFailed,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: "train",
				// Simulate kubelet-GC'd terminated state: LastTerminationState preserves StartedAt.
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
					StartedAt: metav1.NewTime(start),
				}},
			}},
		},
	}
	got, ok := podToAccounting(pod, nvidiaGPU)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if got.End == nil {
		t.Fatalf("End = nil; want deletion time %v", deletion)
	}
	if !got.End.Equal(deletion) {
		t.Errorf("End = %v; want %v", *got.End, deletion)
	}
}

func TestPodToAccounting_FractionalCPUSimGPUs(t *testing.T) {
	start := convT0.Add(-time.Hour)
	cpuSim := corev1.ResourceName("simulated.gpu/unit")
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name: "sim",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{cpuSim: resource.MustParse("250m")},
				},
			}},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:  "sim",
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(start)}},
			}},
		},
	}
	got, ok := podToAccounting(pod, cpuSim)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if got.GPUs < 0.2499 || got.GPUs > 0.2501 {
		t.Errorf("GPUs = %v; want ~0.25", got.GPUs)
	}
}
