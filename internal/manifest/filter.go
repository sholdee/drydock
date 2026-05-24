package manifest

import (
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ResourceFilter describes rendered Kubernetes resources that should be dropped.
type ResourceFilter struct {
	SkipKinds   []string
	SkipCRDs    bool
	SkipSecrets bool
}

// Drop reports whether obj should be excluded by the filter.
func (f ResourceFilter) Drop(obj *unstructured.Unstructured) bool {
	if obj == nil {
		return false
	}

	kind := obj.GetKind()
	if f.SkipCRDs && kind == "CustomResourceDefinition" {
		return true
	}
	if f.SkipSecrets && kind == "Secret" {
		return true
	}
	for _, skipKind := range f.SkipKinds {
		trimmed := strings.TrimSpace(skipKind)
		if trimmed == "" {
			continue
		}
		if trimmed == kind {
			return true
		}
	}
	return false
}

// Empty reports whether the filter has no effective drop rules.
func (f ResourceFilter) Empty() bool {
	if f.SkipCRDs || f.SkipSecrets {
		return false
	}
	for _, skipKind := range f.SkipKinds {
		if strings.TrimSpace(skipKind) != "" {
			return false
		}
	}
	return true
}
