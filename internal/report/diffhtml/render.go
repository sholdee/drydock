package diffhtml

import (
	"bytes"
	"fmt"
	"html"
	"io"
	"slices"
	"strings"

	"github.com/sholdee/drydock/internal/app"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/diff"
)

const defaultTitle = "drydock diff"

type Options struct {
	Title           string
	DefaultResource DefaultResourceSelector
}

type DefaultResourceSelector struct {
	ParentNamespace string
	ParentName      string
	Group           string
	Kind            string
	Namespace       string
	Name            string
}

type appGroup struct {
	id      string
	entries []groupEntry
	added   int
	removed int
}

type groupEntry struct {
	index  int
	result diff.Result
}

type resourceChangeCounts struct {
	changed int
	added   int
	removed int
}

func Render(result app.DiffResult, options Options) ([]byte, error) {
	title := strings.TrimSpace(options.Title)
	if title == "" {
		title = defaultTitle
	}

	groups := groupedResults(result.Results)
	added, removed := totalLineChanges(groups)
	resourceCounts := countResourceChanges(result.Results)
	defaultResourceID := selectDefaultResourceID(groups, options.DefaultResource)

	var builder bytes.Buffer
	builder.WriteString("<!doctype html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n")
	builder.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	fmt.Fprintf(&builder, "<title>%s</title>\n", escape(title))
	fmt.Fprintf(&builder, "<link rel=\"icon\" type=\"image/svg+xml\" href=\"%s\">\n", drydockFaviconHref)
	builder.WriteString("<style>")
	builder.WriteString(reviewStyles)
	builder.WriteString("</style>\n</head>\n")
	renderBodyOpen(&builder, defaultResourceID)
	builder.WriteString("<header class=\"report-header\">\n")
	builder.WriteString("<button class=\"nav-toggle\" type=\"button\" data-sidebar-toggle aria-controls=\"diff-tree\" aria-expanded=\"true\" aria-label=\"Toggle changed resources\"><span aria-hidden=\"true\">☰</span></button>\n")
	builder.WriteString("<div class=\"header-copy\">\n")
	fmt.Fprintf(&builder, "<h1>%s</h1>\n", escape(title))
	renderSummary(&builder, len(groups), len(result.Results), resourceCounts, added, removed)
	builder.WriteString("</div>\n")
	builder.WriteString("<div class=\"header-actions\">\n")
	builder.WriteString(drydockLogo)
	builder.WriteString("\n")
	builder.WriteString("</div>\n")
	builder.WriteString("</header>\n")
	builder.WriteString("<div class=\"review-layout\">\n")
	renderTree(&builder, groups)
	builder.WriteString("<div class=\"sidebar-resizer\" data-sidebar-resizer role=\"separator\" aria-orientation=\"vertical\" aria-label=\"Resize changed resources sidebar\" aria-valuemin=\"240\" aria-valuemax=\"480\" aria-valuenow=\"320\" tabindex=\"0\"></div>\n")
	builder.WriteString("<div class=\"sidebar-backdrop\" data-sidebar-backdrop></div>\n")
	builder.WriteString("<main class=\"review-main\">\n")
	if len(groups) == 0 {
		builder.WriteString("<p class=\"no-diff\">No rendered manifest differences detected.</p>\n")
	} else {
		renderGroups(&builder, groups)
	}
	renderDiagnostics(&builder, result.Diagnostics)
	builder.WriteString("</main>\n</div>\n<script>")
	builder.WriteString(reviewScript)
	builder.WriteString("</script>\n</body>\n</html>\n")
	return builder.Bytes(), nil
}

func renderBodyOpen(builder *bytes.Buffer, defaultResourceID string) {
	builder.WriteString("<body data-view=\"side-by-side\" data-sidebar=\"auto\"")
	if defaultResourceID != "" {
		fmt.Fprintf(builder, " data-default-resource=\"%s\"", escape(defaultResourceID))
	}
	builder.WriteString(">\n")
}

func Write(w io.Writer, result app.DiffResult, options Options) error {
	data, err := Render(result, options)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
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
		group.entries = append(group.entries, groupEntry{index: index, result: result})
		added, removed := lineChanges(result.Diff)
		group.added += added
		group.removed += removed
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
		score := scoreDefaultResource(entry.result)
		if score.rank == 0 {
			continue
		}
		added, removed := lineChanges(entry.result.Diff)
		score.changed = added + removed
		if !found || score.beats(bestScore) {
			best = entry
			bestScore = score
			found = true
		}
	}
	return best, found
}

type defaultResourceScore struct {
	rank    int
	signal  bool
	changed int
}

func (score defaultResourceScore) beats(other defaultResourceScore) bool {
	if score.rank != other.rank {
		return score.rank < other.rank
	}
	if score.signal != other.signal {
		return score.signal
	}
	return score.changed > other.changed
}

func scoreDefaultResource(result diff.Result) defaultResourceScore {
	switch result.Change {
	case diff.ChangeModified:
		score := defaultResourceScore{signal: hasRolloutImpactSignal(result.Diff)}
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
		return defaultResourceScore{rank: 6}
	case diff.ChangeRemoved:
		return defaultResourceScore{rank: 7}
	default:
		return defaultResourceScore{}
	}
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

func renderSummary(builder *bytes.Buffer, apps, resources int, resourceCounts resourceChangeCounts, addedLines, removedLines int) {
	type badge struct {
		class string
		label string
	}
	var badges []badge
	var ariaParts []string
	appendBadge := func(count int, class, label string) {
		if count == 0 {
			return
		}
		badges = append(badges, badge{class: class, label: label})
		ariaParts = append(ariaParts, label)
	}
	appendBadge(apps, "summary-badge-neutral", plural(apps, "app", "apps"))
	appendBadge(resources, "summary-badge-neutral", plural(resources, "resource", "resources"))
	appendBadge(resourceCounts.changed, "summary-badge-modified summary-badge-detail", fmt.Sprintf("%d changed", resourceCounts.changed))
	appendBadge(resourceCounts.added, "summary-badge-added summary-badge-detail", fmt.Sprintf("%d added", resourceCounts.added))
	appendBadge(resourceCounts.removed, "summary-badge-removed summary-badge-detail", fmt.Sprintf("%d deleted", resourceCounts.removed))
	appendBadge(addedLines, "summary-badge-added", fmt.Sprintf("+%d", addedLines))
	appendBadge(removedLines, "summary-badge-removed", fmt.Sprintf("-%d", removedLines))
	if len(badges) == 0 {
		return
	}
	fmt.Fprintf(builder, "<div class=\"summary\" aria-label=\"%s\">", escape(strings.Join(ariaParts, ", ")))
	for _, badge := range badges {
		fmt.Fprintf(builder, "<span class=\"summary-badge %s\">%s</span>", escape(badge.class), escape(badge.label))
	}
	builder.WriteString("</div>\n")
}

func renderDiagnostics(builder *bytes.Buffer, diagnostics []diagnostic.Diagnostic) {
	if len(diagnostics) == 0 {
		return
	}
	builder.WriteString("<details class=\"diagnostics\">\n")
	fmt.Fprintf(builder, "<summary>%s</summary>\n<ul>\n", escape(diagnosticSummary(diagnostics)))
	for _, diag := range diagnostics {
		builder.WriteString("<li>")
		fmt.Fprintf(builder, "<span class=\"severity\">%s</span>", escape(string(diag.Severity)))
		if diag.Category != "" {
			fmt.Fprintf(builder, " <span class=\"category\">%s</span>", escape(diag.Category))
		}
		fmt.Fprintf(builder, " <span class=\"message\">%s</span>", escape(singleLine(diag.Message)))
		builder.WriteString("</li>\n")
	}
	builder.WriteString("</ul>\n</details>\n")
}

func renderTree(builder *bytes.Buffer, groups []appGroup) {
	builder.WriteString("<aside class=\"tree\" id=\"diff-tree\">\n")
	builder.WriteString("<input class=\"tree-search\" data-tree-search type=\"search\" placeholder=\"Search resources (/)\" aria-label=\"Search resources\">\n")
	for _, group := range groups {
		fmt.Fprintf(builder, "<details class=\"tree-app\" data-tree-app=\"%s\" open>\n", escape(group.id))
		fmt.Fprintf(builder, "<summary><span class=\"tree-app-name\">%s</span>%s</summary>\n", escape(group.id), treeDeltaMarkup(group.added, group.removed))
		treeLabelCounts := treeResourceLabelCounts(group.entries)
		for _, entry := range group.entries {
			label := resourceLabel(entry.result.Resource)
			treeLabel := treeResourceLabel(entry.result.Resource, treeLabelCounts)
			added, removed := lineChanges(entry.result.Diff)
			searchText := strings.ToLower(strings.Join([]string{
				group.id,
				label,
				string(entry.result.Change),
			}, " "))
			fmt.Fprintf(builder, "<button class=\"tree-resource\" type=\"button\" data-target-resource=\"resource-%d\" data-search-text=\"%s\" title=\"%s\" aria-label=\"%s\">%s<span class=\"tree-resource-label\">%s</span>%s</button>\n",
				entry.index,
				escape(searchText),
				escape(label),
				escape(treeResourceAriaLabel(label, entry.result.Change, added, removed)),
				treeStatusDotMarkup(entry.result.Change),
				escape(treeLabel),
				treeDeltaMarkup(added, removed),
			)
		}
		builder.WriteString("</details>\n")
	}
	builder.WriteString("</aside>\n")
}

func treeResourceLabelCounts(entries []groupEntry) map[string]int {
	counts := make(map[string]int, len(entries))
	for _, entry := range entries {
		counts[treeResourceBaseLabel(entry.result.Resource)]++
	}
	return counts
}

func treeResourceLabel(resource diff.Resource, labelCounts map[string]int) string {
	label := treeResourceBaseLabel(resource)
	if labelCounts[label] <= 1 {
		return label
	}
	return treeResourceNamespacedLabel(resource)
}

func treeResourceBaseLabel(resource diff.Resource) string {
	return treeResourceLabelWithName(resource.Name, resource.Kind)
}

func treeResourceNamespacedLabel(resource diff.Resource) string {
	name := resource.Name
	if resource.Namespace != "" {
		name = resource.Namespace + "/" + name
	}
	return treeResourceLabelWithName(name, resource.Kind)
}

func treeResourceLabelWithName(name, kind string) string {
	kind = shortTreeResourceKind(kind)
	if name == "" {
		return kind
	}
	if kind == "" {
		return name
	}
	return kind + " · " + name
}

func shortTreeResourceKind(kind string) string {
	switch kind {
	case "HorizontalPodAutoscaler":
		return "HPA"
	case "PodDisruptionBudget":
		return "PDB"
	default:
		return kind
	}
}

func treeResourceAriaLabel(label string, change diff.Change, added, removed int) string {
	parts := []string{label, string(change)}
	if added > 0 {
		parts = append(parts, fmt.Sprintf("plus %d", added))
	}
	if removed > 0 {
		parts = append(parts, fmt.Sprintf("minus %d", removed))
	}
	return strings.Join(parts, ", ")
}

func treeDeltaMarkup(added, removed int) string {
	if added == 0 && removed == 0 {
		return ""
	}
	var builder bytes.Buffer
	builder.WriteString("<span class=\"tree-delta\" aria-hidden=\"true\">")
	if added > 0 {
		fmt.Fprintf(&builder, "<span class=\"tree-delta-added\">+%d</span>", added)
	}
	if removed > 0 {
		fmt.Fprintf(&builder, "<span class=\"tree-delta-removed\">-%d</span>", removed)
	}
	builder.WriteString("</span>")
	return builder.String()
}

func treeStatusDotMarkup(change diff.Change) string {
	switch change {
	case diff.ChangeAdded:
		return "<span class=\"tree-status-dot tree-status-added\" aria-hidden=\"true\"></span>"
	case diff.ChangeRemoved:
		return "<span class=\"tree-status-dot tree-status-removed\" aria-hidden=\"true\"></span>"
	case diff.ChangeModified:
		return "<span class=\"tree-status-dot tree-status-modified\" aria-hidden=\"true\"></span>"
	default:
		return "<span class=\"tree-status-dot tree-status-modified\" aria-hidden=\"true\"></span>"
	}
}

func renderViewToggle(builder *bytes.Buffer) {
	builder.WriteString("<button class=\"view-toggle\" type=\"button\" data-view-toggle aria-label=\"Toggle diff layout\">Unified</button>\n")
}

func renderGroups(builder *bytes.Buffer, groups []appGroup) {
	builder.WriteString("<section class=\"applications\">\n")
	for _, group := range groups {
		for _, entry := range group.entries {
			renderResource(builder, entry)
		}
	}
	builder.WriteString("</section>\n")
}

func renderResource(builder *bytes.Buffer, entry groupEntry) {
	result := entry.result
	hunks := parseUnifiedDiff(result.Diff)
	fmt.Fprintf(builder, "<article class=\"resource\" data-resource-id=\"resource-%d\" data-result-index=\"%d\" data-change=\"%s\">\n",
		entry.index,
		entry.index,
		escape(string(result.Change)),
	)
	builder.WriteString("<header class=\"resource-header\">\n")
	builder.WriteString("<div class=\"resource-title\">\n")
	fmt.Fprintf(builder, "<h3>%s</h3>\n", escape(resourceLabel(result.Resource)))
	builder.WriteString("</div>\n")
	renderViewToggle(builder)
	builder.WriteString("</header>\n")
	if result.Change == diff.ChangeAdded || result.Change == diff.ChangeRemoved {
		renderOneSidedTable(builder, hunks, result.Change, "side-by-side")
		renderOneSidedTable(builder, hunks, result.Change, "unified")
	} else {
		renderSideBySideTable(builder, hunks)
		renderUnifiedTable(builder, hunks)
	}
	builder.WriteString("</article>\n")
}

func renderOneSidedTable(builder *bytes.Buffer, hunks []diffHunk, change diff.Change, viewClass string) {
	rowKind := string(change)
	fmt.Fprintf(builder, "<table class=\"diff-table %s one-sided\">\n<tbody>\n", escape(viewClass))
	for _, hunk := range hunks {
		fmt.Fprintf(builder, "<tr class=\"hunk-header\"><th colspan=\"2\">%s</th></tr>\n", escape(hunk.Header))
		for _, row := range hunk.Rows {
			if row.Kind != rowKind {
				continue
			}
			number := row.LeftNumber
			text := row.LeftText
			if change == diff.ChangeAdded {
				number = row.RightNumber
				text = row.RightText
			}
			fmt.Fprintf(builder, "<tr class=\"diff-row %s\">", escape(row.Kind))
			renderLineNumberCell(builder, number)
			builder.WriteString("<td class=\"line-code\">")
			renderHighlightedText(builder, text, nil, rowKind)
			builder.WriteString("</td>")
			builder.WriteString("</tr>\n")
		}
	}
	builder.WriteString("</tbody>\n</table>\n")
}

func renderSideBySideTable(builder *bytes.Buffer, hunks []diffHunk) {
	builder.WriteString("<table class=\"diff-table side-by-side\">\n<tbody>\n")
	for _, hunk := range hunks {
		leftHighlights, rightHighlights := pairedHighlights(hunk.Rows)
		fmt.Fprintf(builder, "<tr class=\"hunk-header\"><th colspan=\"4\">%s</th></tr>\n", escape(hunk.Header))
		for index, row := range hunk.Rows {
			fmt.Fprintf(builder, "<tr class=\"diff-row %s\">", escape(row.Kind))
			renderLineNumberCell(builder, row.LeftNumber)
			renderLineCodeOpen(builder, row.LeftNumber == 0)
			renderHighlightedText(builder, row.LeftText, leftHighlights[index], "removed")
			builder.WriteString("</td>")
			renderLineNumberCell(builder, row.RightNumber)
			renderLineCodeOpen(builder, row.RightNumber == 0)
			renderHighlightedText(builder, row.RightText, rightHighlights[index], "added")
			builder.WriteString("</td>")
			builder.WriteString("</tr>\n")
		}
	}
	builder.WriteString("</tbody>\n</table>\n")
}

func renderUnifiedTable(builder *bytes.Buffer, hunks []diffHunk) {
	builder.WriteString("<table class=\"diff-table unified\">\n<tbody>\n")
	for _, hunk := range hunks {
		leftHighlights, rightHighlights := pairedHighlights(hunk.Rows)
		fmt.Fprintf(builder, "<tr class=\"hunk-header\"><th colspan=\"2\">%s</th></tr>\n", escape(hunk.Header))
		for index, row := range hunk.Rows {
			number := row.RightNumber
			text := row.RightText
			highlights := rightHighlights[index]
			highlightClass := "added"
			if row.Kind == "removed" {
				number = row.LeftNumber
				text = row.LeftText
				highlights = leftHighlights[index]
				highlightClass = "removed"
			}
			fmt.Fprintf(builder, "<tr class=\"diff-row %s\">", escape(row.Kind))
			renderLineNumberCell(builder, number)
			builder.WriteString("<td class=\"line-code\">")
			renderHighlightedText(builder, text, highlights, highlightClass)
			builder.WriteString("</td>")
			builder.WriteString("</tr>\n")
		}
	}
	builder.WriteString("</tbody>\n</table>\n")
}

func renderLineNumberCell(builder *bytes.Buffer, number int) {
	if number == 0 {
		builder.WriteString("<td class=\"line-number line-number-blank\"></td>")
		return
	}
	fmt.Fprintf(builder, "<td class=\"line-number\">%d</td>", number)
}

func renderLineCodeOpen(builder *bytes.Buffer, blank bool) {
	if blank {
		builder.WriteString("<td class=\"line-code line-code-blank\">")
		return
	}
	builder.WriteString("<td class=\"line-code\">")
}

type highlightRange struct {
	start int
	end   int
}

func pairedHighlights(rows []diffRow) (map[int][]highlightRange, map[int][]highlightRange) {
	leftHighlights := make(map[int][]highlightRange)
	rightHighlights := make(map[int][]highlightRange)
	for index := 0; index < len(rows); {
		if rows[index].Kind != "removed" {
			index++
			continue
		}
		removedStart := index
		for index < len(rows) && rows[index].Kind == "removed" {
			index++
		}
		addedStart := index
		for index < len(rows) && rows[index].Kind == "added" {
			index++
		}
		removedCount := addedStart - removedStart
		addedCount := index - addedStart
		pairCount := min(removedCount, addedCount)
		for offset := range pairCount {
			leftIndex := removedStart + offset
			rightIndex := addedStart + offset
			left, right := changedRanges(rows[leftIndex].LeftText, rows[rightIndex].RightText)
			if len(left) > 0 {
				leftHighlights[leftIndex] = left
			}
			if len(right) > 0 {
				rightHighlights[rightIndex] = right
			}
		}
	}
	return leftHighlights, rightHighlights
}

func changedRanges(left, right string) ([]highlightRange, []highlightRange) {
	leftRunes := []rune(left)
	rightRunes := []rune(right)
	if string(leftRunes) == string(rightRunes) {
		return nil, nil
	}

	prefix := 0
	for prefix < len(leftRunes) && prefix < len(rightRunes) && leftRunes[prefix] == rightRunes[prefix] {
		prefix++
	}

	suffix := 0
	for suffix < len(leftRunes)-prefix &&
		suffix < len(rightRunes)-prefix &&
		leftRunes[len(leftRunes)-1-suffix] == rightRunes[len(rightRunes)-1-suffix] {
		suffix++
	}

	leftEnd := len(leftRunes) - suffix
	rightEnd := len(rightRunes) - suffix
	var leftRanges, rightRanges []highlightRange
	if prefix < leftEnd {
		leftRanges = append(leftRanges, highlightRange{start: prefix, end: leftEnd})
	}
	if prefix < rightEnd {
		rightRanges = append(rightRanges, highlightRange{start: prefix, end: rightEnd})
	}
	return leftRanges, rightRanges
}

func renderHighlightedText(builder *bytes.Buffer, text string, highlights []highlightRange, class string) {
	if len(highlights) == 0 {
		builder.WriteString(escape(text))
		return
	}
	runes := []rune(text)
	cursor := 0
	for _, highlight := range highlights {
		if highlight.start < cursor {
			highlight.start = cursor
		}
		if highlight.end > len(runes) {
			highlight.end = len(runes)
		}
		if highlight.start > cursor {
			builder.WriteString(escape(string(runes[cursor:highlight.start])))
		}
		if highlight.start < highlight.end {
			fmt.Fprintf(builder, "<span class=\"inline-change %s\">%s</span>",
				escape(class),
				escape(string(runes[highlight.start:highlight.end])),
			)
			cursor = highlight.end
		}
	}
	if cursor < len(runes) {
		builder.WriteString(escape(string(runes[cursor:])))
	}
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

func diagnosticSummary(diagnostics []diagnostic.Diagnostic) string {
	if len(diagnostics) == 1 {
		severity := strings.TrimSpace(string(diagnostics[0].Severity))
		if severity != "" {
			return "Diagnostics: 1 " + severity
		}
		return "Diagnostics: 1 message"
	}
	return fmt.Sprintf("Diagnostics: %d messages", len(diagnostics))
}

func singleLine(text string) string {
	text = strings.ReplaceAll(text, "\r\n", " ")
	text = strings.ReplaceAll(text, "\n", " ")
	text = strings.ReplaceAll(text, "\r", " ")
	return text
}

func escape(text string) string {
	return html.EscapeString(text)
}
