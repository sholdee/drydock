package diff

import "strings"

const (
	IgnoreResourceStatusAll  = "all"
	IgnoreResourceStatusCRD  = "crd"
	IgnoreResourceStatusNone = "none"
)

type CompareOptions struct {
	IgnoreAggregatedRoles     bool
	IgnoreResourceStatusField string
}

func mergeCompareOptions(left, right CompareOptions) CompareOptions {
	return CompareOptions{
		IgnoreAggregatedRoles:     left.IgnoreAggregatedRoles || right.IgnoreAggregatedRoles,
		IgnoreResourceStatusField: mergeStatusMode(left.IgnoreResourceStatusField, right.IgnoreResourceStatusField),
	}
}

func mergeStatusMode(left, right string) string {
	leftRank := statusModeRank(left)
	rightRank := statusModeRank(right)
	if leftRank >= rightRank {
		return statusModeForRank(leftRank)
	}
	return statusModeForRank(rightRank)
}

func statusModeRank(value string) int {
	switch normalizedStatusMode(value) {
	case IgnoreResourceStatusNone:
		return 0
	case IgnoreResourceStatusCRD:
		return 1
	default:
		return 2
	}
}

func statusModeForRank(rank int) string {
	switch rank {
	case 0:
		return IgnoreResourceStatusNone
	case 1:
		return IgnoreResourceStatusCRD
	default:
		return IgnoreResourceStatusAll
	}
}

func normalizeCompareOptionsObject(object map[string]any, resource Resource, opts CompareOptions) map[string]any {
	if object == nil {
		return nil
	}
	if shouldRemoveStatus(resource, opts.IgnoreResourceStatusField) {
		delete(object, "status")
	}
	if opts.IgnoreAggregatedRoles {
		normalizeAggregatedRole(object, resource)
	}
	return object
}

func compareOptionsRequireObject(resource Resource, opts CompareOptions) bool {
	return shouldRemoveStatus(resource, opts.IgnoreResourceStatusField) || opts.IgnoreAggregatedRoles
}

func shouldRemoveStatus(resource Resource, value string) bool {
	switch normalizedStatusMode(value) {
	case IgnoreResourceStatusNone:
		return false
	case IgnoreResourceStatusCRD:
		return resource.Group == "apiextensions.k8s.io" && resource.Kind == "CustomResourceDefinition"
	default:
		return true
	}
}

func normalizedStatusMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "none", "off", "false":
		return IgnoreResourceStatusNone
	case "crd":
		return IgnoreResourceStatusCRD
	case "", "all":
		return IgnoreResourceStatusAll
	default:
		return IgnoreResourceStatusAll
	}
}

func normalizeAggregatedRole(object map[string]any, resource Resource) {
	if resource.Group != "rbac.authorization.k8s.io" || (resource.Kind != "Role" && resource.Kind != "ClusterRole") {
		return
	}
	if _, ok := stringMapField(object, "aggregationRule"); !ok {
		return
	}
	delete(object, "rules")
}
