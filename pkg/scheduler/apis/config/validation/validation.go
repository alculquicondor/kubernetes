/*
Copyright 2018 The Kubernetes Authors.

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

package validation

import (
	"errors"
	"fmt"

	v1 "k8s.io/api/core/v1"
	metav1validation "k8s.io/apimachinery/pkg/apis/meta/v1/validation"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	componentbasevalidation "k8s.io/component-base/config/validation"
	v1helper "k8s.io/kubernetes/pkg/apis/core/v1/helper"
	"k8s.io/kubernetes/pkg/scheduler/apis/config"
)

var (
	supportedScheduleActions = sets.NewString(string(v1.DoNotSchedule), string(v1.ScheduleAnyway))
)

// ValidateKubeSchedulerConfiguration ensures validation of the KubeSchedulerConfiguration struct
func ValidateKubeSchedulerConfiguration(cc *config.KubeSchedulerConfiguration) field.ErrorList {
	allErrs := field.ErrorList{}
	allErrs = append(allErrs, componentbasevalidation.ValidateClientConnectionConfiguration(&cc.ClientConnection, field.NewPath("clientConnection"))...)
	allErrs = append(allErrs, validateKubeSchedulerLeaderElectionConfiguration(&cc.LeaderElection, field.NewPath("leaderElection"))...)
	if len(cc.SchedulerName) == 0 {
		allErrs = append(allErrs, field.Required(field.NewPath("schedulerName"), ""))
	}
	allErrs = append(allErrs, validateTopologySpreadConstraints(cc.TopologySpreadConstraints, field.NewPath("topologySpreadConstraints"))...)
	for _, msg := range validation.IsValidSocketAddr(cc.HealthzBindAddress) {
		allErrs = append(allErrs, field.Invalid(field.NewPath("healthzBindAddress"), cc.HealthzBindAddress, msg))
	}
	for _, msg := range validation.IsValidSocketAddr(cc.MetricsBindAddress) {
		allErrs = append(allErrs, field.Invalid(field.NewPath("metricsBindAddress"), cc.MetricsBindAddress, msg))
	}
	if cc.HardPodAffinitySymmetricWeight < 0 || cc.HardPodAffinitySymmetricWeight > 100 {
		allErrs = append(allErrs, field.Invalid(field.NewPath("hardPodAffinitySymmetricWeight"), cc.HardPodAffinitySymmetricWeight, "not in valid range [0-100]"))
	}
	if cc.PercentageOfNodesToScore < 0 || cc.PercentageOfNodesToScore > 100 {
		allErrs = append(allErrs, field.Invalid(field.NewPath("percentageOfNodesToScore"),
			cc.PercentageOfNodesToScore, "not in valid range [0-100]"))
	}
	if cc.PodInitialBackoffSeconds <= 0 {
		allErrs = append(allErrs, field.Invalid(field.NewPath("podInitialBackoffSeconds"),
			cc.PodInitialBackoffSeconds, "must be greater than 0"))
	}
	if cc.PodMaxBackoffSeconds < cc.PodInitialBackoffSeconds {
		allErrs = append(allErrs, field.Invalid(field.NewPath("podMaxBackoffSeconds"),
			cc.PodMaxBackoffSeconds, "must be greater than or equal to PodInitialBackoffSeconds"))
	}
	return allErrs
}

// validateKubeSchedulerLeaderElectionConfiguration ensures validation of the KubeSchedulerLeaderElectionConfiguration struct
func validateKubeSchedulerLeaderElectionConfiguration(cc *config.KubeSchedulerLeaderElectionConfiguration, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	if !cc.LeaderElectionConfiguration.LeaderElect {
		return allErrs
	}
	allErrs = append(allErrs, componentbasevalidation.ValidateLeaderElectionConfiguration(&cc.LeaderElectionConfiguration, field.NewPath("leaderElectionConfiguration"))...)
	return allErrs
}

func validateTopologySpreadConstraints(constraints []v1.TopologySpreadConstraint, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	for i, c := range constraints {
		p := fldPath.Index(i)
		if c.MaxSkew <= 0 {
			f := p.Child("maxSkew")
			allErrs = append(allErrs, field.Invalid(f, c.MaxSkew, "must be greater than zero"))
		}
		allErrs = append(allErrs, validateConstraintTopologyKey(c.TopologyKey, p.Child("topologyKey"))...)
		if err := validateConstraintWhenUnsatisfiable(c.WhenUnsatisfiable, p.Child("whenUnsatisfiable")); err != nil {
			allErrs = append(allErrs, err)
		}
		if c.LabelSelector != nil {
			f := field.Forbidden(p.Child("labelSelector"), "cluster-level constraint must not define a selector, as they are deduced for each Pod")
			allErrs = append(allErrs, f)
		}
		if err := validateSpreadConstraintNotRepeat(fldPath, constraints, i); err != nil {
			allErrs = append(allErrs, err)
		}
	}
	return allErrs
}

func validateConstraintTopologyKey(v string, p *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	if len(v) == 0 {
		allErrs = append(allErrs, field.Required(p, "can not be empty"))
	} else {
		allErrs = append(allErrs, metav1validation.ValidateLabelName(v, p)...)
	}
	return allErrs
}

func validateConstraintWhenUnsatisfiable(v v1.UnsatisfiableConstraintAction, p *field.Path) *field.Error {
	if len(v) == 0 {
		return field.Required(p, "can not be empty")
	}
	if !supportedScheduleActions.Has(string(v)) {
		f := p.Child("whenUnsatisfiable")
		return field.NotSupported(f, v, supportedScheduleActions.List())
	}
	return nil
}

// validateSpreadConstraintNotRepeat tests that if `constraints[idx]` duplicates
// with `constraints[idx+1:]` on TopologyKey and WhenUnsatisfiable fields.
func validateSpreadConstraintNotRepeat(path *field.Path, constraints []v1.TopologySpreadConstraint, idx int) *field.Error {
	c := &constraints[idx]
	for i := idx + 1; i < len(constraints); i++ {
		other := &constraints[i]
		if c.TopologyKey == other.TopologyKey && c.WhenUnsatisfiable == other.WhenUnsatisfiable {
			return field.Duplicate(path.Index(idx), path.Index(i))
		}
	}
	return nil
}

// ValidatePolicy checks for errors in the Config
// It does not return early so that it can find as many errors as possible
func ValidatePolicy(policy config.Policy) error {
	var validationErrors []error

	priorities := make(map[string]config.PriorityPolicy, len(policy.Priorities))
	for _, priority := range policy.Priorities {
		if priority.Weight <= 0 || priority.Weight >= config.MaxWeight {
			validationErrors = append(validationErrors, fmt.Errorf("Priority %s should have a positive weight applied to it or it has overflown", priority.Name))
		}
		validationErrors = append(validationErrors, validateCustomPriorities(priorities, priority))
	}

	binders := 0
	extenderManagedResources := sets.NewString()
	for _, extender := range policy.Extenders {
		if len(extender.PrioritizeVerb) > 0 && extender.Weight <= 0 {
			validationErrors = append(validationErrors, fmt.Errorf("Priority for extender %s should have a positive weight applied to it", extender.URLPrefix))
		}
		if extender.BindVerb != "" {
			binders++
		}
		for _, resource := range extender.ManagedResources {
			errs := validateExtendedResourceName(v1.ResourceName(resource.Name))
			if len(errs) != 0 {
				validationErrors = append(validationErrors, errs...)
			}
			if extenderManagedResources.Has(resource.Name) {
				validationErrors = append(validationErrors, fmt.Errorf("Duplicate extender managed resource name %s", string(resource.Name)))
			}
			extenderManagedResources.Insert(resource.Name)
		}
	}
	if binders > 1 {
		validationErrors = append(validationErrors, fmt.Errorf("Only one extender can implement bind, found %v", binders))
	}
	return utilerrors.NewAggregate(validationErrors)
}

// validateCustomPriorities validates that:
// 1. RequestedToCapacityRatioRedeclared custom priority cannot be declared multiple times,
// 2. LabelPreference/ServiceAntiAffinity custom priorities can be declared multiple times,
// however the weights for each custom priority type should be the same.
func validateCustomPriorities(priorities map[string]config.PriorityPolicy, priority config.PriorityPolicy) error {
	verifyRedeclaration := func(priorityType string) error {
		if existing, alreadyDeclared := priorities[priorityType]; alreadyDeclared {
			return fmt.Errorf("Priority %q redeclares custom priority %q, from:%q", priority.Name, priorityType, existing.Name)
		}
		priorities[priorityType] = priority
		return nil
	}
	verifyDifferentWeights := func(priorityType string) error {
		if existing, alreadyDeclared := priorities[priorityType]; alreadyDeclared {
			if existing.Weight != priority.Weight {
				return fmt.Errorf("%s  priority %q has a different weight with %q", priorityType, priority.Name, existing.Name)
			}
		}
		priorities[priorityType] = priority
		return nil
	}
	if priority.Argument != nil {
		if priority.Argument.LabelPreference != nil {
			if err := verifyDifferentWeights("LabelPreference"); err != nil {
				return err
			}
		} else if priority.Argument.ServiceAntiAffinity != nil {
			if err := verifyDifferentWeights("ServiceAntiAffinity"); err != nil {
				return err
			}
		} else if priority.Argument.RequestedToCapacityRatioArguments != nil {
			if err := verifyRedeclaration("RequestedToCapacityRatio"); err != nil {
				return err
			}
		} else {
			return fmt.Errorf("No priority arguments set for priority %s", priority.Name)
		}
	}
	return nil
}

// validateExtendedResourceName checks whether the specified name is a valid
// extended resource name.
func validateExtendedResourceName(name v1.ResourceName) []error {
	var validationErrors []error
	for _, msg := range validation.IsQualifiedName(string(name)) {
		validationErrors = append(validationErrors, errors.New(msg))
	}
	if len(validationErrors) != 0 {
		return validationErrors
	}
	if !v1helper.IsExtendedResourceName(name) {
		validationErrors = append(validationErrors, fmt.Errorf("%s is an invalid extended resource name", name))
	}
	return validationErrors
}
