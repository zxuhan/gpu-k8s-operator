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
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	budgetv1alpha1 "github.com/zxuhan/gpu-k8s-operator/api/v1alpha1"
)

var gpuworkloadbudgetlog = logf.Log.WithName("gpuworkloadbudget-webhook")

// SetupGPUWorkloadBudgetWebhookWithManager registers the webhook for GPUWorkloadBudget in the manager.
func SetupGPUWorkloadBudgetWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &budgetv1alpha1.GPUWorkloadBudget{}).
		WithValidator(&GPUWorkloadBudgetCustomValidator{}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-budget-zxuhan-dev-v1alpha1-gpuworkloadbudget,mutating=false,failurePolicy=fail,sideEffects=None,groups=budget.zxuhan.dev,resources=gpuworkloadbudgets,verbs=create;update,versions=v1alpha1,name=vgpuworkloadbudget-v1alpha1.kb.io,admissionReviewVersions=v1

// GPUWorkloadBudgetCustomValidator validates GPUWorkloadBudget create and update requests.
// OpenAPI schema catches well-typed errors (enum on Action, Minimum on WindowHours);
// this webhook catches the rest: unrepresentable-in-schema constraints like
// "gpuHours > 0" (resource.Quantity has no min-value marker support) and
// "selector must match something" (we reject empty selectors because the k8s
// default of "empty matches everything" is a footgun for a budget object).
type GPUWorkloadBudgetCustomValidator struct{}

var gwbGVK = schema.GroupKind{Group: budgetv1alpha1.GroupVersion.Group, Kind: "GPUWorkloadBudget"}

// ValidateCreate implements webhook.CustomValidator.
func (v *GPUWorkloadBudgetCustomValidator) ValidateCreate(
	_ context.Context, obj *budgetv1alpha1.GPUWorkloadBudget,
) (admission.Warnings, error) {
	gpuworkloadbudgetlog.V(1).Info("validating create", "name", obj.GetName(), "namespace", obj.GetNamespace())
	return nil, toInvalidErr(obj, validateSpec(&obj.Spec, field.NewPath("spec")))
}

// ValidateUpdate implements webhook.CustomValidator. Validation is identical to create —
// in v1alpha1 every field is mutable — but the signature is kept separate so we can
// add immutability checks (e.g. freezing spec.gpuResourceName after first reconcile)
// without restructuring callers.
func (v *GPUWorkloadBudgetCustomValidator) ValidateUpdate(
	_ context.Context, _, newObj *budgetv1alpha1.GPUWorkloadBudget,
) (admission.Warnings, error) {
	gpuworkloadbudgetlog.V(1).Info("validating update", "name", newObj.GetName(), "namespace", newObj.GetNamespace())
	return nil, toInvalidErr(newObj, validateSpec(&newObj.Spec, field.NewPath("spec")))
}

// ValidateDelete is a no-op — the controller handles teardown via a finalizer.
func (v *GPUWorkloadBudgetCustomValidator) ValidateDelete(
	_ context.Context, _ *budgetv1alpha1.GPUWorkloadBudget,
) (admission.Warnings, error) {
	return nil, nil
}

// validateSpec runs the programmatic checks OpenAPI can't express.
// Exported for unit tests via the Validate* hooks above; if we need it from
// elsewhere (e.g. a dry-run CLI) we can expose a wrapper later.
func validateSpec(spec *budgetv1alpha1.GPUWorkloadBudgetSpec, path *field.Path) field.ErrorList {
	var errs field.ErrorList

	// Quota: gpuHours must be strictly positive. Zero would make the budget
	// permanently over-quota on the first scheduled pod; negative is nonsensical.
	quotaPath := path.Child("quota")
	if spec.Quota.GPUHours.Sign() <= 0 {
		errs = append(errs, field.Invalid(
			quotaPath.Child("gpuHours"),
			spec.Quota.GPUHours.String(),
			"must be greater than 0",
		))
	}
	// windowHours has Minimum=1 in OpenAPI, but we double-check in case a future
	// version loosens the marker — the accounting engine divides by this value.
	if spec.Quota.WindowHours <= 0 {
		errs = append(errs, field.Invalid(
			quotaPath.Child("windowHours"),
			spec.Quota.WindowHours,
			"must be greater than 0",
		))
	}

	// Enforcement: the Enum marker on Action rejects unknown strings at the
	// OpenAPI layer, but we still check here so unit tests that bypass the
	// CRD schema surface a real error. GracePeriodSeconds has Minimum=0 in
	// OpenAPI; repeat the check for the same reason.
	enfPath := path.Child("enforcement")
	switch spec.Enforcement.Action {
	case budgetv1alpha1.ActionEvict,
		budgetv1alpha1.ActionPause,
		budgetv1alpha1.ActionAlertOnly:
		// ok
	case "":
		errs = append(errs, field.Required(enfPath.Child("action"), "one of Evict, Pause, AlertOnly"))
	default:
		errs = append(errs, field.NotSupported(
			enfPath.Child("action"),
			string(spec.Enforcement.Action),
			[]string{
				string(budgetv1alpha1.ActionEvict),
				string(budgetv1alpha1.ActionPause),
				string(budgetv1alpha1.ActionAlertOnly),
			},
		))
	}
	if spec.Enforcement.GracePeriodSeconds < 0 {
		errs = append(errs, field.Invalid(
			enfPath.Child("gracePeriodSeconds"),
			spec.Enforcement.GracePeriodSeconds,
			"must be greater than or equal to 0",
		))
	}

	// Selector: reject empty (matches everything in k8s convention); also
	// reject malformed matchExpressions. LabelSelectorAsSelector returns a
	// descriptive error for bad operators and values.
	selPath := path.Child("selector")
	if isEmptyLabelSelector(&spec.Selector) {
		errs = append(errs, field.Required(
			selPath,
			"must not be empty; set matchLabels or matchExpressions to scope the budget",
		))
	} else if _, err := metav1.LabelSelectorAsSelector(&spec.Selector); err != nil {
		errs = append(errs, field.Invalid(selPath, spec.Selector, err.Error()))
	}

	return errs
}

// isEmptyLabelSelector returns true for the Kubernetes "matches everything"
// sentinel — no matchLabels and no matchExpressions.
func isEmptyLabelSelector(s *metav1.LabelSelector) bool {
	if s == nil {
		return true
	}
	return len(s.MatchLabels) == 0 && len(s.MatchExpressions) == 0
}

// toInvalidErr wraps a field.ErrorList into the canonical k8s Invalid status error
// so admission clients see per-field causes rather than one flat string.
func toInvalidErr(obj *budgetv1alpha1.GPUWorkloadBudget, errs field.ErrorList) error {
	if len(errs) == 0 {
		return nil
	}
	name := obj.GetName()
	if name == "" {
		name = fmt.Sprintf("<new %s>", gwbGVK.Kind)
	}
	return apierrors.NewInvalid(gwbGVK, name, errs)
}
