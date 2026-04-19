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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EnforcementAction is what the operator does when a budget is exceeded.
// +kubebuilder:validation:Enum=Evict;Pause;AlertOnly
type EnforcementAction string

const (
	// ActionEvict deletes offending pods via the eviction subresource, honouring PodDisruptionBudgets.
	ActionEvict EnforcementAction = "Evict"
	// ActionPause stamps offending pods with a pause annotation and leaves them running,
	// allowing a higher-level workflow (Argo, Kueue, Jobset) to react.
	ActionPause EnforcementAction = "Pause"
	// ActionAlertOnly emits events and flips the QuotaExceeded condition without touching pods.
	ActionAlertOnly EnforcementAction = "AlertOnly"
)

// Condition types surfaced in .status.conditions.
const (
	// ConditionReady is True when the controller has reconciled at least once and
	// accounting is trusted.
	ConditionReady = "Ready"
	// ConditionQuotaExceeded is True when consumed GPU-hours meet or exceed the quota
	// over the current rolling window.
	ConditionQuotaExceeded = "QuotaExceeded"
	// ConditionDegraded is True when the controller cannot make forward progress
	// (selector misconfigured, enforcement API errors, etc.).
	ConditionDegraded = "Degraded"
)

// Quota defines how many GPU-hours may be consumed over a rolling window.
type Quota struct {
	// gpuHours is the maximum cumulative GPU-hours permitted in the rolling window.
	// Accepts fractional values (e.g. "0.5", "100", "250.75").
	// Must be > 0; zero or negative values are rejected by the validating webhook.
	// +required
	GPUHours resource.Quantity `json:"gpuHours"`

	// windowHours is the length of the rolling window, in hours. Must be at least 1.
	// +required
	// +kubebuilder:validation:Minimum=1
	WindowHours int32 `json:"windowHours"`
}

// Enforcement describes what happens when the quota is exceeded.
type Enforcement struct {
	// action selects the behaviour applied to offending pods. Exactly one of
	// Evict, Pause, or AlertOnly.
	// +required
	Action EnforcementAction `json:"action"`

	// gracePeriodSeconds is how long a pod is allowed to remain running after being
	// flagged for enforcement. For Evict, this is passed to the eviction subresource.
	// For Pause, the pause annotation is delayed this long. AlertOnly ignores it.
	// +optional
	// +kubebuilder:default=30
	// +kubebuilder:validation:Minimum=0
	GracePeriodSeconds int32 `json:"gracePeriodSeconds,omitempty"`
}

// GPUWorkloadBudgetSpec defines the desired state of GPUWorkloadBudget.
type GPUWorkloadBudgetSpec struct {
	// selector picks the pods whose GPU consumption counts against this budget.
	// An empty selector is rejected by the validating webhook — the Kubernetes
	// default of "empty matches everything" would silently turn a misconfigured
	// budget into a namespace-wide accounting sink.
	// +required
	Selector metav1.LabelSelector `json:"selector"`

	// quota is the cap, in GPU-hours, over the rolling window.
	// +required
	Quota Quota `json:"quota"`

	// enforcement describes what the controller does once the quota is exceeded.
	// +required
	Enforcement Enforcement `json:"enforcement"`

	// gpuResourceName is the extended resource counted against the budget.
	// Defaults to nvidia.com/gpu. When pods do not request this resource,
	// the controller falls back to CPU-second simulation (see docs/accounting-model.md).
	// +optional
	// +kubebuilder:default="nvidia.com/gpu"
	GPUResourceName corev1.ResourceName `json:"gpuResourceName,omitempty"`
}

// GPUWorkloadBudgetStatus is the observed state of GPUWorkloadBudget.
type GPUWorkloadBudgetStatus struct {
	// consumedGpuHours is the cumulative GPU-hours counted against this budget
	// over the current rolling window.
	// +optional
	ConsumedGPUHours resource.Quantity `json:"consumedGpuHours,omitempty"`

	// remainingGpuHours is spec.quota.gpuHours minus consumedGpuHours, floored at zero.
	// +optional
	RemainingGPUHours resource.Quantity `json:"remainingGpuHours,omitempty"`

	// trackedPods is the number of pods currently matched by the selector. Reported
	// so operators can spot an empty selector before it silently fails to account.
	// +optional
	TrackedPods int32 `json:"trackedPods,omitempty"`

	// lastEnforcementAt is the wall-clock time the controller last took an
	// enforcement action against any pod under this budget. Nil until the first
	// action fires.
	// +optional
	LastEnforcementAt *metav1.Time `json:"lastEnforcementAt,omitempty"`

	// observedGeneration is the .metadata.generation the controller last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// conditions carries Ready, QuotaExceeded, and Degraded.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=gwb;gwbs
// +kubebuilder:printcolumn:name="Consumed",type=string,JSONPath=`.status.consumedGpuHours`
// +kubebuilder:printcolumn:name="Remaining",type=string,JSONPath=`.status.remainingGpuHours`
// +kubebuilder:printcolumn:name="Window",type=integer,JSONPath=`.spec.quota.windowHours`
// +kubebuilder:printcolumn:name="Action",type=string,JSONPath=`.spec.enforcement.action`
// +kubebuilder:printcolumn:name="Pods",type=integer,JSONPath=`.status.trackedPods`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// GPUWorkloadBudget is the Schema for the gpuworkloadbudgets API.
type GPUWorkloadBudget struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of GPUWorkloadBudget
	// +required
	Spec GPUWorkloadBudgetSpec `json:"spec"`

	// status defines the observed state of GPUWorkloadBudget
	// +optional
	Status GPUWorkloadBudgetStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// GPUWorkloadBudgetList contains a list of GPUWorkloadBudget.
type GPUWorkloadBudgetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []GPUWorkloadBudget `json:"items"`
}

func init() {
	SchemeBuilder.Register(&GPUWorkloadBudget{}, &GPUWorkloadBudgetList{})
}
