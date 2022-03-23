package v1

import (
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/minio/pkg/wildcard"
	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// Rule defines a validation, mutation, or generation control for matching resources.
// Each rules contains a match declaration to select resources, and an optional exclude
// declaration to specify which resources to exclude.
type Rule struct {
	// Name is a label to identify the rule, It must be unique within the policy.
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name,omitempty" yaml:"name,omitempty"`

	// Context defines variables and data sources that can be used during rule execution.
	// +optional
	Context []ContextEntry `json:"context,omitempty" yaml:"context,omitempty"`

	// MatchResources defines when this policy rule should be applied. The match
	// criteria can include resource information (e.g. kind, name, namespace, labels)
	// and admission review request information like the user name or role.
	// At least one kind is required.
	MatchResources MatchResources `json:"match,omitempty" yaml:"match,omitempty"`

	// ExcludeResources defines when this policy rule should not be applied. The exclude
	// criteria can include resource information (e.g. kind, name, namespace, labels)
	// and admission review request information like the name or role.
	// +optional
	ExcludeResources MatchResources `json:"exclude,omitempty" yaml:"exclude,omitempty"`

	// Preconditions are used to determine if a policy rule should be applied by evaluating a
	// set of conditions. The declaration can contain nested `any` or `all` statements. A direct list
	// of conditions (without `any` or `all` statements is supported for backwards compatibility but
	// will be deprecated in the next major release.
	// See: https://kyverno.io/docs/writing-policies/preconditions/
	// +optional
	RawAnyAllConditions *apiextv1.JSON `json:"preconditions,omitempty" yaml:"preconditions,omitempty"`

	// Mutation is used to modify matching resources.
	// +optional
	Mutation Mutation `json:"mutate,omitempty" yaml:"mutate,omitempty"`

	// Validation is used to validate matching resources.
	// +optional
	Validation Validation `json:"validate,omitempty" yaml:"validate,omitempty"`

	// Generation is used to create new resources.
	// +optional
	Generation Generation `json:"generate,omitempty" yaml:"generate,omitempty"`

	// VerifyImages is used to verify image signatures and mutate them to add a digest
	// +optional
	VerifyImages []*ImageVerification `json:"verifyImages,omitempty" yaml:"verifyImages,omitempty"`
}

// HasMutate checks for mutate rule
func (r *Rule) HasMutate() bool {
	return !reflect.DeepEqual(r.Mutation, Mutation{})
}

// HasVerifyImages checks for verifyImages rule
func (r *Rule) HasVerifyImages() bool {
	return r.VerifyImages != nil && !reflect.DeepEqual(r.VerifyImages, ImageVerification{})
}

// HasValidate checks for validate rule
func (r *Rule) HasValidate() bool {
	return !reflect.DeepEqual(r.Validation, Validation{})
}

// HasGenerate checks for generate rule
func (r *Rule) HasGenerate() bool {
	return !reflect.DeepEqual(r.Generation, Generation{})
}

// MatchKinds returns a slice of all kinds to match
func (r *Rule) MatchKinds() []string {
	matchKinds := r.MatchResources.ResourceDescription.Kinds
	for _, value := range r.MatchResources.All {
		matchKinds = append(matchKinds, value.ResourceDescription.Kinds...)
	}
	for _, value := range r.MatchResources.Any {
		matchKinds = append(matchKinds, value.ResourceDescription.Kinds...)
	}

	return matchKinds
}

// ExcludeKinds returns a slice of all kinds to exclude
func (r *Rule) ExcludeKinds() []string {
	excludeKinds := r.ExcludeResources.ResourceDescription.Kinds
	for _, value := range r.ExcludeResources.All {
		excludeKinds = append(excludeKinds, value.ResourceDescription.Kinds...)
	}
	for _, value := range r.ExcludeResources.Any {
		excludeKinds = append(excludeKinds, value.ResourceDescription.Kinds...)
	}
	return excludeKinds
}

func (r *Rule) GetAnyAllConditions() apiextensions.JSON {
	return FromJSON(r.RawAnyAllConditions)
}

func (r *Rule) SetAnyAllConditions(in apiextensions.JSON) {
	r.RawAnyAllConditions = ToJSON(in)
}

// ValidateRuleType checks only one type of rule is defined per rule
func (r *Rule) ValidateRuleType(path *field.Path) field.ErrorList {
	var errs field.ErrorList
	ruleTypes := []bool{r.HasMutate(), r.HasValidate(), r.HasGenerate(), r.HasVerifyImages()}
	count := 0
	for _, v := range ruleTypes {
		if v {
			count++
		}
	}
	if count == 0 {
		errs = append(errs, field.Invalid(path, r, fmt.Sprintf("No operation defined in the rule '%s'.(supported operations: mutate,validate,generate,verifyImages)", r.Name)))
	} else if count != 1 {
		errs = append(errs, field.Invalid(path, r, fmt.Sprintf("Multiple operations defined in the rule '%s', only one operation (mutate,validate,generate,verifyImages) is allowed per rule", r.Name)))
	}
	return errs
}

// ValidateMathExcludeConflict checks if the resultant of match and exclude block is not an empty set
func (r *Rule) ValidateMathExcludeConflict(path *field.Path) (errs field.ErrorList) {
	if len(r.ExcludeResources.All) > 0 || len(r.MatchResources.All) > 0 {
		return errs
	}
	// if both have any then no resource should be common
	if len(r.MatchResources.Any) > 0 && len(r.ExcludeResources.Any) > 0 {
		for _, rmr := range r.MatchResources.Any {
			for _, rer := range r.ExcludeResources.Any {
				if reflect.DeepEqual(rmr, rer) {
					return append(errs, field.Invalid(path, r, "Rule is matching an empty set"))
				}
			}
		}
		return errs
	}
	if reflect.DeepEqual(r.ExcludeResources, MatchResources{}) {
		return errs
	}
	excludeRoles := sets.NewString(r.ExcludeResources.Roles...)
	excludeClusterRoles := sets.NewString(r.ExcludeResources.ClusterRoles...)
	excludeKinds := sets.NewString(r.ExcludeResources.Kinds...)
	excludeNamespaces := sets.NewString(r.ExcludeResources.Namespaces...)
	excludeSubjects := sets.NewString()
	for _, subject := range r.ExcludeResources.Subjects {
		subjectRaw, _ := json.Marshal(subject)
		excludeSubjects.Insert(string(subjectRaw))
	}
	excludeSelectorMatchExpressions := sets.NewString()
	if r.ExcludeResources.Selector != nil {
		for _, matchExpression := range r.ExcludeResources.Selector.MatchExpressions {
			matchExpressionRaw, _ := json.Marshal(matchExpression)
			excludeSelectorMatchExpressions.Insert(string(matchExpressionRaw))
		}
	}
	excludeNamespaceSelectorMatchExpressions := sets.NewString()
	if r.ExcludeResources.NamespaceSelector != nil {
		for _, matchExpression := range r.ExcludeResources.NamespaceSelector.MatchExpressions {
			matchExpressionRaw, _ := json.Marshal(matchExpression)
			excludeNamespaceSelectorMatchExpressions.Insert(string(matchExpressionRaw))
		}
	}
	if len(excludeRoles) > 0 {
		if len(r.MatchResources.Roles) == 0 || !excludeRoles.HasAll(r.MatchResources.Roles...) {
			return errs
		}
	}
	if len(excludeClusterRoles) > 0 {
		if len(r.MatchResources.ClusterRoles) == 0 || !excludeClusterRoles.HasAll(r.MatchResources.ClusterRoles...) {
			return errs
		}
	}
	if len(excludeSubjects) > 0 {
		if len(r.MatchResources.Subjects) == 0 {
			return errs
		}
		for _, subject := range r.MatchResources.UserInfo.Subjects {
			subjectRaw, _ := json.Marshal(subject)
			if !excludeSubjects.Has(string(subjectRaw)) {
				return errs
			}
		}
	}
	if r.ExcludeResources.Name != "" {
		if !wildcard.Match(r.ExcludeResources.Name, r.MatchResources.Name) {
			return errs
		}
	}
	if len(r.ExcludeResources.Names) > 0 {
		excludeSlice := r.ExcludeResources.Names
		matchSlice := r.MatchResources.Names

		// if exclude block has something and match doesn't it means we
		// have a non empty set
		if len(r.MatchResources.Names) == 0 {
			return errs
		}

		// if *any* name in match and exclude conflicts
		// we want user to fix that
		for _, matchName := range matchSlice {
			for _, excludeName := range excludeSlice {
				if wildcard.Match(excludeName, matchName) {
					return append(errs, field.Invalid(path, r, "Rule is matching an empty set"))
				}
			}
		}
		return errs
	}
	if len(excludeNamespaces) > 0 {
		if len(r.MatchResources.Namespaces) == 0 || !excludeNamespaces.HasAll(r.MatchResources.Namespaces...) {
			return errs
		}
	}
	if len(excludeKinds) > 0 {
		if len(r.MatchResources.Kinds) == 0 || !excludeKinds.HasAll(r.MatchResources.Kinds...) {
			return errs
		}
	}
	if r.MatchResources.Selector != nil && r.ExcludeResources.Selector != nil {
		if len(excludeSelectorMatchExpressions) > 0 {
			if len(r.MatchResources.Selector.MatchExpressions) == 0 {
				return errs
			}
			for _, matchExpression := range r.MatchResources.Selector.MatchExpressions {
				matchExpressionRaw, _ := json.Marshal(matchExpression)
				if !excludeSelectorMatchExpressions.Has(string(matchExpressionRaw)) {
					return errs
				}
			}
		}
		if len(r.ExcludeResources.Selector.MatchLabels) > 0 {
			if len(r.MatchResources.Selector.MatchLabels) == 0 {
				return errs
			}
			for label, value := range r.MatchResources.Selector.MatchLabels {
				if r.ExcludeResources.Selector.MatchLabels[label] != value {
					return errs
				}
			}
		}
	}
	if r.MatchResources.NamespaceSelector != nil && r.ExcludeResources.NamespaceSelector != nil {
		if len(excludeNamespaceSelectorMatchExpressions) > 0 {
			if len(r.MatchResources.NamespaceSelector.MatchExpressions) == 0 {
				return errs
			}
			for _, matchExpression := range r.MatchResources.NamespaceSelector.MatchExpressions {
				matchExpressionRaw, _ := json.Marshal(matchExpression)
				if !excludeNamespaceSelectorMatchExpressions.Has(string(matchExpressionRaw)) {
					return errs
				}
			}
		}
		if len(r.ExcludeResources.NamespaceSelector.MatchLabels) > 0 {
			if len(r.MatchResources.NamespaceSelector.MatchLabels) == 0 {
				return errs
			}
			for label, value := range r.MatchResources.NamespaceSelector.MatchLabels {
				if r.ExcludeResources.NamespaceSelector.MatchLabels[label] != value {
					return errs
				}
			}
		}
	}
	if (r.MatchResources.Selector == nil && r.ExcludeResources.Selector != nil) ||
		(r.MatchResources.Selector != nil && r.ExcludeResources.Selector == nil) {
		return errs
	}
	if (r.MatchResources.NamespaceSelector == nil && r.ExcludeResources.NamespaceSelector != nil) ||
		(r.MatchResources.NamespaceSelector != nil && r.ExcludeResources.NamespaceSelector == nil) {
		return errs
	}
	if r.MatchResources.Annotations != nil && r.ExcludeResources.Annotations != nil {
		if !(reflect.DeepEqual(r.MatchResources.Annotations, r.ExcludeResources.Annotations)) {
			return errs
		}
	}
	if (r.MatchResources.Annotations == nil && r.ExcludeResources.Annotations != nil) ||
		(r.MatchResources.Annotations != nil && r.ExcludeResources.Annotations == nil) {
		return errs
	}
	return append(errs, field.Invalid(path, r, "Rule is matching an empty set"))
}

// Validate implements programmatic validation
func (r *Rule) Validate(path *field.Path, namespaced bool, clusterResources sets.String) field.ErrorList {
	var errs field.ErrorList
	errs = append(errs, r.ValidateRuleType(path)...)
	errs = append(errs, r.ValidateMathExcludeConflict(path)...)
	errs = append(errs, r.MatchResources.Validate(path.Child("match"), namespaced, clusterResources)...)
	errs = append(errs, r.ExcludeResources.Validate(path.Child("exclude"), namespaced, clusterResources)...)
	return errs
}
