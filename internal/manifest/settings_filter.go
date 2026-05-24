package manifest

import (
	"github.com/argoproj/argo-cd/v3/util/glob"
	"github.com/sholdee/drydock/internal/config"
)

var coreExcludedResourceRules = []config.ResourceFilterRule{
	{APIGroups: []string{"events.k8s.io", "metrics.k8s.io"}},
	{APIGroups: []string{""}, Kinds: []string{"Event"}},
	{APIGroups: []string{"coordination.k8s.io"}, Kinds: []string{"Lease"}},
}

type SettingsResourceFilter struct {
	Exclusions []config.ResourceFilterRule
	Inclusions []config.ResourceFilterRule
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
