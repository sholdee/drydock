package manifest

import (
	"strings"

	"github.com/argoproj/argo-cd/v3/util/glob"
	"github.com/sholdee/drydock/internal/config"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
)

var coreExcludedResourceRules = []config.ResourceFilterRule{
	{APIGroups: []string{"events.k8s.io", "metrics.k8s.io"}},
	{APIGroups: []string{""}, Kinds: []string{"Event"}},
	{APIGroups: []string{"coordination.k8s.io"}, Kinds: []string{"Lease"}},
}

type SettingsResourceFilter struct {
	Exclusions []config.ResourceFilterRule
	Inclusions []config.ResourceFilterRule
	// Selectors are the argocd-cm resource.selectors rules. They never drop
	// a resource: Argo CD only narrows its list/watch calls with them.
	Selectors []config.ResourceFilterRule
}

func (f SettingsResourceFilter) Drop(id Identity, cluster string) bool {
	if matchesAnyResourceRule(id, cluster, coreExcludedResourceRules) {
		return true
	}
	if matchesAnyResourceRule(id, cluster, f.Exclusions) {
		return true
	}
	if matchesAnyResourceRule(id, cluster, f.Inclusions) {
		return false
	}
	if anyRuleMatchesCluster(f.Inclusions, cluster) {
		return true
	}
	return false
}

func matchesAnyResourceRule(id Identity, cluster string, rules []config.ResourceFilterRule) bool {
	for _, rule := range rules {
		if resourceRuleMatches(rule, id, cluster) {
			return true
		}
	}
	return false
}

func resourceRuleMatches(rule config.ResourceFilterRule, id Identity, cluster string) bool {
	return resourceRuleMatchesGroup(rule, id.Group) &&
		resourceRuleMatchesKind(rule, id.Kind) &&
		resourceRuleMatchesCluster(rule, cluster)
}

func resourceRuleMatchesGroup(rule config.ResourceFilterRule, group string) bool {
	for _, apiGroup := range rule.APIGroups {
		if glob.Match(apiGroup, group) {
			return true
		}
	}
	return len(rule.APIGroups) == 0
}

func resourceRuleMatchesKind(rule config.ResourceFilterRule, kind string) bool {
	for _, ruleKind := range rule.Kinds {
		if ruleKind == "*" || ruleKind == kind {
			return true
		}
	}
	return len(rule.Kinds) == 0
}

func resourceRuleMatchesCluster(rule config.ResourceFilterRule, cluster string) bool {
	for _, ruleCluster := range rule.Clusters {
		if glob.Match(ruleCluster, cluster) {
			return true
		}
	}
	return len(rule.Clusters) == 0
}

func anyRuleMatchesCluster(rules []config.ResourceFilterRule, cluster string) bool {
	for _, rule := range rules {
		if resourceRuleMatchesCluster(rule, cluster) {
			return true
		}
	}
	return false
}

// LabelSelector mirrors Argo CD's ResourcesFilter.GetLabelSelector
// (util/settings/resources_filter.go at v3.6.1): the selectors of every
// resource.selectors rule matching the group, kind, and cluster, ANDed by
// joining them with commas. Rules with a blank selector are skipped, and an
// empty result means no selector applies.
func (f SettingsResourceFilter) LabelSelector(id Identity, cluster string) string {
	selectors := make([]string, 0)
	for _, rule := range f.Selectors {
		selector := strings.TrimSpace(rule.Selector)
		if selector == "" || !resourceRuleMatches(rule, id, cluster) {
			continue
		}
		selectors = append(selectors, selector)
	}
	return strings.Join(selectors, ",")
}

// SelectorMismatch reports the applicable resource.selectors selector when
// obj's labels do not match it. Argo CD keeps such a desired object managed
// but never watches it (controller/state.go CompareAppState at v3.6.1), so
// it stays OutOfSync. A selector that does not parse reports no mismatch,
// as upstream: settings loading has already rejected it.
func (f SettingsResourceFilter) SelectorMismatch(obj *unstructured.Unstructured, cluster string) (string, bool) {
	if obj == nil {
		return "", false
	}
	selectorText := f.LabelSelector(IdentityOf(obj), cluster)
	if selectorText == "" {
		return "", false
	}
	selector, err := labels.Parse(selectorText)
	if err != nil || selector.Matches(labels.Set(obj.GetLabels())) {
		return "", false
	}
	return selectorText, true
}
