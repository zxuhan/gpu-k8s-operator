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
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/zxuhan/gpu-k8s-operator/internal/accounting"
)

// podToAccounting translates a corev1.Pod into the minimum shape the
// accounting engine needs. The two interesting pieces are the interval
// [Start, End) and the GPU count held across that interval.
//
// Pending pods (no observed Start) and pods requesting zero of the
// configured GPU resource return ok=false so the caller can skip them
// without inflating TrackedPods for workloads the budget doesn't cover.
//
// The GPU count is the sum of resources.requests[gpuResourceName] across
// the pod's regular containers. Init containers are excluded — they run
// to completion before the main workload and are not typical GPU
// consumers; including them would double-count ephemeral setup work.
func podToAccounting(pod *corev1.Pod, gpuResourceName corev1.ResourceName) (accounting.Pod, bool) {
	gpus := gpuRequestTotal(pod, gpuResourceName)
	if gpus <= 0 {
		return accounting.Pod{}, false
	}

	start, ok := earliestContainerStart(pod)
	if !ok {
		return accounting.Pod{}, false
	}

	var end *time.Time
	if e, ok := podEnd(pod); ok {
		end = &e
	}

	return accounting.Pod{
		ID:    string(pod.UID),
		Start: start.UTC(),
		End:   end,
		GPUs:  gpus,
	}, true
}

// gpuRequestTotal sums the requested count of the configured GPU
// resource across the pod's regular containers. resource.Quantity's
// AsApproximateFloat64 gives us a float suitable for the accounting
// engine without a strconv round-trip.
func gpuRequestTotal(pod *corev1.Pod, name corev1.ResourceName) float64 {
	var total float64
	for i := range pod.Spec.Containers {
		q, ok := pod.Spec.Containers[i].Resources.Requests[name]
		if !ok {
			continue
		}
		total += q.AsApproximateFloat64()
	}
	return total
}

// earliestContainerStart finds the first time any container in the pod
// actually started running. Using container StartedAt rather than
// pod.status.startTime matters because startTime fires as soon as the
// kubelet *accepts* the pod — a pod that spends 20 minutes pulling a
// multi-gig CUDA image would over-count by the pull duration.
//
// Falls back to pod.status.startTime if no container has started yet;
// callers receive ok=false if neither signal is available (truly Pending).
func earliestContainerStart(pod *corev1.Pod) (time.Time, bool) {
	var earliest time.Time
	found := false
	observe := func(t time.Time) {
		if t.IsZero() {
			return
		}
		if !found || t.Before(earliest) {
			earliest = t
			found = true
		}
	}
	for i := range pod.Status.ContainerStatuses {
		s := &pod.Status.ContainerStatuses[i]
		if s.State.Running != nil {
			observe(s.State.Running.StartedAt.Time)
		}
		if s.State.Terminated != nil {
			observe(s.State.Terminated.StartedAt.Time)
		}
		if s.LastTerminationState.Terminated != nil {
			observe(s.LastTerminationState.Terminated.StartedAt.Time)
		}
	}
	if found {
		return earliest, true
	}
	if pod.Status.StartTime != nil {
		return pod.Status.StartTime.Time, true
	}
	return time.Time{}, false
}

// podEnd returns the wall-clock instant the pod stopped consuming GPU,
// or ok=false if it is still running. The contract matches
// accounting.Pod.End: nil means "running at the observation instant".
//
// Strategy:
//   - Phase==Succeeded|Failed is a hard signal the pod is done; use the
//     latest container FinishedAt. If kubelet already GC'd the container
//     statuses (seen on older clusters after pod deletion) fall back to
//     pod.DeletionTimestamp — the grace period error is bounded and
//     matches the post-restart recovery model documented in
//     accounting.go.
//   - A still-Running pod with a DeletionTimestamp is mid-shutdown; we
//     leave End nil so the pod keeps accumulating until a terminal phase
//     is observed. Over-counting by a grace period is safer than
//     under-counting.
func podEnd(pod *corev1.Pod) (time.Time, bool) {
	if pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed {
		return time.Time{}, false
	}
	var latest time.Time
	found := false
	for i := range pod.Status.ContainerStatuses {
		t := pod.Status.ContainerStatuses[i].State.Terminated
		if t == nil || t.FinishedAt.IsZero() {
			continue
		}
		if !found || t.FinishedAt.After(latest) {
			latest = t.FinishedAt.Time
			found = true
		}
	}
	if found {
		return latest.UTC(), true
	}
	if pod.DeletionTimestamp != nil {
		return pod.DeletionTimestamp.UTC(), true
	}
	return time.Time{}, false
}
