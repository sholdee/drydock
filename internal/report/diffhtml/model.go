package diffhtml

import (
	"fmt"
	"slices"
	"strings"

	"github.com/sholdee/drydock/internal/diff"
)

const (
	defaultResourceRawBytesLimit     = 20 * 1024
	defaultResourceChangedLinesLimit = 400
	lazyResourceRawBytesThreshold    = 250 * 1024
	lazyResourceParsedRowsThreshold  = 2000
	hardResourceRawBytesLimit        = 500 * 1024
	hardResourceParsedRowsLimit      = 20000
)

type appGroup struct {
	id      string
	entries []groupEntry
	added   int
	removed int
}

type groupEntry struct {
	index   int
	result  diff.Result
	metrics resourceDiffMetrics
}

type resourceChangeCounts struct {
	changed int
	added   int
	removed int
}

type resourceDiffMetrics struct {
	rawBytes     int
	addedLines   int
	removedLines int
	changedLines int
	parsedRows   int
}

func groupedResults(results []diff.Result) []appGroup {
	byID := make(map[string]*appGroup)
	for index, result := range results {
		id := applicationID(result.Parent)
		group := byID[id]
		if group == nil {
			group = &appGroup{id: id}
			byID[id] = group
		}
		metrics := measureDiff(result)
		group.entries = append(group.entries, groupEntry{index: index, result: result, metrics: metrics})
		group.added += metrics.addedLines
		group.removed += metrics.removedLines
	}

	groups := make([]appGroup, 0, len(byID))
	for _, group := range byID {
		groups = append(groups, *group)
	}
	slices.SortFunc(groups, func(left, right appGroup) int {
		leftChanged := left.added + left.removed
		rightChanged := right.added + right.removed
		if leftChanged != rightChanged {
			return rightChanged - leftChanged
		}
		return strings.Compare(left.id, right.id)
	})
	return groups
}

func selectDefaultResourceID(groups []appGroup, selector DefaultResourceSelector) string {
	entries := renderedEntries(groups)
	if len(entries) == 0 {
		return ""
	}
	if hasDefaultResourceSelector(selector) {
		for _, entry := range entries {
			if matchesDefaultResourceSelector(selector, entry.result) {
				return resourceID(entry)
			}
		}
	}
	if entry, ok := heuristicDefaultResource(entries); ok {
		return resourceID(entry)
	}
	return resourceID(entries[0])
}

func renderedEntries(groups []appGroup) []groupEntry {
	entries := make([]groupEntry, 0)
	for _, group := range groups {
		entries = append(entries, group.entries...)
	}
	return entries
}

func resourceID(entry groupEntry) string {
	return fmt.Sprintf("resource-%d", entry.index)
}

func hasDefaultResourceSelector(selector DefaultResourceSelector) bool {
	return selector.ParentNamespace != "" ||
		selector.ParentName != "" ||
		selector.Group != "" ||
		selector.Kind != "" ||
		selector.Namespace != "" ||
		selector.Name != ""
}

func matchesDefaultResourceSelector(selector DefaultResourceSelector, result diff.Result) bool {
	return selectorMatches(selector.ParentNamespace, result.Parent.Namespace) &&
		selectorMatches(selector.ParentName, result.Parent.Name) &&
		selectorMatches(selector.Group, result.Resource.Group) &&
		selectorMatches(selector.Kind, result.Resource.Kind) &&
		selectorMatches(selector.Namespace, result.Resource.Namespace) &&
		selectorMatches(selector.Name, result.Resource.Name)
}

func selectorMatches(want, got string) bool {
	return want == "" || want == got
}

func heuristicDefaultResource(entries []groupEntry) (groupEntry, bool) {
	var best groupEntry
	var bestScore defaultResourceScore
	var found bool
	for _, entry := range entries {
		score := scoreDefaultResource(entry.result, entry.metrics)
		if score.rank == 0 {
			continue
		}
		if !found || score.beats(bestScore) {
			best = entry
			bestScore = score
			found = true
		}
	}
	return best, found
}

type defaultResourceScore struct {
	rank         int
	readable     bool
	signal       bool
	rawBytes     int
	changedLines int
	parsedRows   int
}

func (score defaultResourceScore) beats(other defaultResourceScore) bool {
	if score.rank != other.rank {
		return score.rank < other.rank
	}
	if score.readable != other.readable {
		return score.readable
	}
	if score.readable {
		if score.signal != other.signal {
			return score.signal
		}
		return score.changedLines > other.changedLines
	}
	if score.rawBytes != other.rawBytes {
		return score.rawBytes < other.rawBytes
	}
	if score.changedLines != other.changedLines {
		return score.changedLines < other.changedLines
	}
	if score.parsedRows != other.parsedRows {
		return score.parsedRows < other.parsedRows
	}
	return score.signal && !other.signal
}

func scoreDefaultResource(result diff.Result, metrics resourceDiffMetrics) defaultResourceScore {
	score := defaultResourceScore{
		readable:     isReadableDefaultResource(result, metrics),
		rawBytes:     metrics.rawBytes,
		changedLines: metrics.changedLines,
		parsedRows:   metrics.parsedRows,
	}
	switch result.Change {
	case diff.ChangeModified:
		score.signal = hasRolloutImpactSignal(result.Diff)
		switch {
		case isWorkloadController(result.Resource.Kind):
			score.rank = 1
		case isTrafficExposureResource(result.Resource.Kind):
			score.rank = 2
		case isAutoscalingPolicyResource(result.Resource.Kind):
			score.rank = 3
		case isConfigResource(result.Resource.Kind):
			score.rank = 4
		default:
			score.rank = 5
		}
		return score
	case diff.ChangeAdded:
		score.rank = 6
		return score
	case diff.ChangeRemoved:
		score.rank = 7
		return score
	default:
		return defaultResourceScore{}
	}
}

func isReadableDefaultResource(result diff.Result, metrics resourceDiffMetrics) bool {
	return !isNoisyDefaultResource(result) && !isOversizedDefaultResource(metrics)
}

func isNoisyDefaultResource(result diff.Result) bool {
	return result.Resource.Kind == "CustomResourceDefinition"
}

func isOversizedDefaultResource(metrics resourceDiffMetrics) bool {
	return metrics.rawBytes > defaultResourceRawBytesLimit ||
		metrics.changedLines > defaultResourceChangedLinesLimit
}

func isWorkloadController(kind string) bool {
	switch kind {
	case "Deployment", "StatefulSet", "DaemonSet", "Job", "CronJob":
		return true
	default:
		return false
	}
}

func hasRolloutImpactSignal(text string) bool {
	lower := strings.ToLower(text)
	for _, signal := range []string{
		"helm.sh/chart",
		"checksum/",
	} {
		if strings.Contains(lower, signal) {
			return true
		}
	}
	for _, signal := range []string{
		"image:",
		"replicas:",
		"resources:",
		"env:",
	} {
		if containsFieldSignal(lower, signal) {
			return true
		}
	}
	return false
}

func containsFieldSignal(text, signal string) bool {
	for offset := 0; offset < len(text); {
		index := strings.Index(text[offset:], signal)
		if index == -1 {
			return false
		}
		index += offset
		if index == 0 || !isFieldNameCharacter(text[index-1]) {
			return true
		}
		offset = index + len(signal)
	}
	return false
}

func isFieldNameCharacter(char byte) bool {
	return char >= 'a' && char <= 'z' ||
		char >= '0' && char <= '9' ||
		char == '_' ||
		char == '-'
}

func isTrafficExposureResource(kind string) bool {
	switch kind {
	case "Service", "Ingress", "Gateway", "HTTPRoute", "TLSRoute", "TCPRoute", "UDPRoute", "GatewayClass":
		return true
	default:
		return false
	}
}

func isAutoscalingPolicyResource(kind string) bool {
	switch kind {
	case "HorizontalPodAutoscaler", "VerticalPodAutoscaler", "PodDisruptionBudget", "NetworkPolicy":
		return true
	default:
		return false
	}
}

func isConfigResource(kind string) bool {
	switch kind {
	case "ConfigMap", "Secret":
		return true
	default:
		return false
	}
}

func applicationID(parent diff.Parent) string {
	if parent.Namespace != "" {
		return parent.Namespace + "/" + parent.Name
	}
	return parent.Name
}

func totalLineChanges(groups []appGroup) (int, int) {
	var added, removed int
	for _, group := range groups {
		added += group.added
		removed += group.removed
	}
	return added, removed
}

func countResourceChanges(results []diff.Result) resourceChangeCounts {
	var counts resourceChangeCounts
	for _, result := range results {
		switch result.Change {
		case diff.ChangeAdded:
			counts.added++
		case diff.ChangeRemoved:
			counts.removed++
		case diff.ChangeModified:
			counts.changed++
		default:
			counts.changed++
		}
	}
	return counts
}

func lineChanges(text string) (int, int) {
	var added, removed int
	for line := range strings.SplitSeq(text, "\n") {
		switch {
		case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
		case strings.HasPrefix(line, "+"):
			added++
		case strings.HasPrefix(line, "-"):
			removed++
		}
	}
	return added, removed
}

func measureDiff(result diff.Result) resourceDiffMetrics {
	added, removed := lineChanges(result.Diff)
	hunks := parseUnifiedDiff(result.Diff)
	var parsedRows int
	for _, hunk := range hunks {
		parsedRows += len(hunk.Rows)
	}
	return resourceDiffMetrics{
		rawBytes:     len(result.Diff),
		addedLines:   added,
		removedLines: removed,
		changedLines: added + removed,
		parsedRows:   parsedRows,
	}
}

func isLazyResource(metrics resourceDiffMetrics) bool {
	return metrics.rawBytes >= lazyResourceRawBytesThreshold ||
		metrics.parsedRows >= lazyResourceParsedRowsThreshold
}

func isHardGuardedResource(metrics resourceDiffMetrics) bool {
	return metrics.rawBytes > hardResourceRawBytesLimit ||
		metrics.parsedRows > hardResourceParsedRowsLimit
}

func resourceLabel(resource diff.Resource) string {
	var parts []string
	if resource.Group != "" {
		parts = append(parts, resource.Group)
	}
	if resource.Kind != "" {
		parts = append(parts, resource.Kind)
	}
	name := resource.Name
	if resource.Namespace != "" {
		name = resource.Namespace + "/" + name
	}
	if name != "" {
		parts = append(parts, name)
	}
	return strings.Join(parts, " ")
}

func plural(count int, singular, multiple string) string {
	if count == 1 {
		return fmt.Sprintf("%d %s", count, singular)
	}
	return fmt.Sprintf("%d %s", count, multiple)
}
