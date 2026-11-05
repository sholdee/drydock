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

// Drop mirrors Argo CD's ResourcesFilter.IsExcludedResource
// (util/settings/resources_filter.go at v3.6.1). The cluster is the resolved
// destination server, which Argo CD passes as destCluster.Server
// (controller/state.go CompareAppState). It is never the destination name.
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

// DropUnresolved is Drop for a destination whose server drydock cannot
// resolve offline. It evaluates IsExcludedResource in three-valued logic
// over the unknown server, the way AppProject validation evaluates an
// unresolved destination. It drops id only when Argo CD would drop it for
// every possible server. When the answer depends on the server, it keeps
// id, so a guess never removes a resource from the output.
func (f SettingsResourceFilter) DropUnresolved(id Identity) bool {
	excluded := anyRuleMatchesUnknownCluster(id, coreExcludedResourceRules).or(anyRuleMatchesUnknownCluster(id, f.Exclusions))
	included := anyRuleMatchesUnknownCluster(id, f.Inclusions)
	inclusionsForCluster := tristateFalse
	for _, rule := range f.Inclusions {
		inclusionsForCluster = inclusionsForCluster.or(ruleMatchesUnknownCluster(rule))
	}
	return excluded.or(included.not().and(inclusionsForCluster)) == tristateTrue
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

// tristate is a Kleene truth value for a filter decision that may depend on
// a destination server drydock cannot resolve. The encoding orders the
// values so that AND is min and OR is max.
type tristate uint8

const (
	tristateFalse tristate = iota
	tristateUnknown
	tristateTrue
)

func tristateOf(value bool) tristate {
	if value {
		return tristateTrue
	}
	return tristateFalse
}

func (t tristate) and(other tristate) tristate { return min(t, other) }

func (t tristate) or(other tristate) tristate { return max(t, other) }

func (t tristate) not() tristate { return tristateTrue - t }

func anyRuleMatchesUnknownCluster(id Identity, rules []config.ResourceFilterRule) tristate {
	matched := tristateFalse
	for _, rule := range rules {
		matched = matched.or(tristateOf(resourceRuleMatchesGroup(rule, id.Group) && resourceRuleMatchesKind(rule, id.Kind)).and(ruleMatchesUnknownCluster(rule)))
	}
	return matched
}

// ruleMatchesUnknownCluster is FilteredResource.MatchCluster for a server
// that is unknown but not empty: Argo CD always resolves a destination to a
// cluster with a server.
func ruleMatchesUnknownCluster(rule config.ResourceFilterRule) tristate {
	if len(rule.Clusters) == 0 {
		return tristateTrue
	}
	matched := tristateFalse
	for _, pattern := range rule.Clusters {
		matched = matched.or(clusterGlobMatchesUnknownServer(pattern))
	}
	return matched
}

// clusterGlobMatchesUnknownServer decides util/glob.Match(pattern, server)
// for an unknown non-empty server. A pattern of only stars matches every
// server, and an empty or invalid pattern matches none. Any other pattern
// depends on the server.
func clusterGlobMatchesUnknownServer(pattern string) tristate {
	if pattern == "" {
		return tristateFalse
	}
	if _, err := glob.MatchWithError(pattern, ""); err != nil {
		return tristateFalse
	}
	if strings.Trim(pattern, "*") == "" {
		return tristateTrue
	}
	return tristateUnknown
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
