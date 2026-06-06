package diffhtml

import (
	"bytes"
	"fmt"
	"html"
	"slices"
	"strings"

	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/diff"
)

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
			added, removed := entry.metrics.addedLines, entry.metrics.removedLines
			large := isLazyResource(entry.metrics)
			searchParts := []string{
				group.id,
				label,
				string(entry.result.Change),
			}
			if large {
				searchParts = append(searchParts, "large")
			}
			searchText := strings.ToLower(strings.Join(searchParts, " "))
			fmt.Fprintf(builder, "<button class=\"tree-resource\" type=\"button\" data-target-resource=\"resource-%d\" data-search-text=\"%s\" title=\"%s\" aria-label=\"%s\">%s<span class=\"tree-resource-label\">%s</span>%s</button>\n",
				entry.index,
				escape(searchText),
				escape(label),
				escape(treeResourceAriaLabel(label, entry.result.Change, added, removed, large)),
				treeStatusDotMarkup(entry.result.Change),
				escape(treeLabel),
				treeResourceMetaMarkup(added, removed, large),
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

func treeResourceAriaLabel(label string, change diff.Change, added, removed int, large bool) string {
	parts := []string{label, string(change)}
	if large {
		parts = append(parts, "large")
	}
	if added > 0 {
		parts = append(parts, fmt.Sprintf("plus %d", added))
	}
	if removed > 0 {
		parts = append(parts, fmt.Sprintf("minus %d", removed))
	}
	return strings.Join(parts, ", ")
}

func treeResourceMetaMarkup(added, removed int, large bool) string {
	delta := treeDeltaMarkup(added, removed)
	if !large {
		return delta
	}
	return "<span class=\"tree-resource-meta\"><span class=\"tree-large-badge\">large</span>" + delta + "</span>"
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

func renderGroups(builder *bytes.Buffer, groups []appGroup) error {
	builder.WriteString("<section class=\"applications\">\n")
	for _, group := range groups {
		for _, entry := range group.entries {
			if err := renderResource(builder, entry); err != nil {
				return err
			}
		}
	}
	builder.WriteString("</section>\n")
	return nil
}

func renderResource(builder *bytes.Buffer, entry groupEntry) error {
	result := entry.result
	lazy := isLazyResource(entry.metrics)
	hardGuarded := isHardGuardedResource(entry.metrics)
	lazyAttributes := ""
	if hardGuarded {
		lazyAttributes = " data-lazy-diff=\"blocked\" data-lazy-state=\"blocked\""
	} else if lazy {
		lazyAttributes = " data-lazy-diff=\"true\" data-lazy-state=\"pending\""
	}
	fmt.Fprintf(builder, "<article class=\"resource\" data-resource-id=\"resource-%d\" data-result-index=\"%d\" data-change=\"%s\"%s>\n",
		entry.index,
		entry.index,
		escape(string(result.Change)),
		lazyAttributes,
	)
	builder.WriteString("<header class=\"resource-header\">\n")
	builder.WriteString("<div class=\"resource-title\">\n")
	renderResourceHeading(builder, result.Resource)
	builder.WriteString("</div>\n")
	renderViewToggle(builder)
	builder.WriteString("</header>\n")
	switch {
	case hardGuarded:
		renderTooLargeResource(builder, entry)
	default:
		hunks := parseUnifiedDiff(result.Diff)
		switch {
		case lazy:
			if err := renderLazyResource(builder, entry, hunks); err != nil {
				return err
			}
		case result.Change == diff.ChangeAdded || result.Change == diff.ChangeRemoved:
			renderOneSidedTable(builder, hunks, result.Change, "side-by-side")
			renderOneSidedTable(builder, hunks, result.Change, "unified")
		default:
			renderSideBySideTable(builder, hunks)
			renderUnifiedTable(builder, hunks)
		}
	}
	builder.WriteString("</article>\n")
	return nil
}

func renderLazyResource(builder *bytes.Buffer, entry groupEntry, hunks []diffHunk) error {
	result := entry.result
	metricsText := lazyDiffMetricText(entry.metrics)
	label := resourceLabel(result.Resource)
	fmt.Fprintf(builder, "<div class=\"lazy-diff-placeholder\" data-lazy-placeholder aria-live=\"polite\">\n")
	fmt.Fprintf(builder, "<p>Large diff: %s.</p>\n", escape(metricsText))
	fmt.Fprintf(builder, "<div class=\"lazy-diff-actions\"><button class=\"lazy-render-button\" type=\"button\" data-lazy-render aria-label=\"Render diff for %s. Large diff: %s.\">Render diff</button></div>\n",
		escape(label),
		escape(metricsText),
	)
	builder.WriteString("</div>\n")
	payload, err := lazyDiffPayloadJSON(result, entry.metrics, hunks)
	if err != nil {
		return err
	}
	fmt.Fprintf(builder, "<script type=\"application/json\" data-diff-payload=\"resource-%d\">%s</script>\n", entry.index, payload)
	return nil
}

func renderTooLargeResource(builder *bytes.Buffer, entry groupEntry) {
	metricsText := lazyDiffMetricText(entry.metrics)
	label := resourceLabel(entry.result.Resource)
	reason := fmt.Sprintf("Diff too large for in-page rendering: %s.", metricsText)
	builder.WriteString("<div class=\"lazy-diff-placeholder lazy-diff-placeholder-blocked\" data-lazy-placeholder>\n")
	fmt.Fprintf(builder, "<p>%s</p>\n", escape(reason))
	fmt.Fprintf(builder, "<div class=\"lazy-diff-actions\"><button class=\"lazy-render-button\" type=\"button\" disabled aria-disabled=\"true\" aria-label=\"Cannot render diff for %s. %s\" title=\"%s\">Render diff unavailable</button></div>\n",
		escape(label),
		escape(reason),
		escape(reason),
	)
	builder.WriteString("</div>\n")
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

var allowedSyntaxClasses = map[string]struct{}{
	yamlKeyClass:         {},
	yamlStringClass:      {},
	yamlNumberClass:      {},
	yamlBoolClass:        {},
	yamlNullClass:        {},
	yamlCommentClass:     {},
	yamlDocClass:         {},
	yamlAnchorClass:      {},
	yamlAliasClass:       {},
	yamlTagClass:         {},
	yamlPunctuationClass: {},
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
	renderComposedHighlightedText(builder, text, highlights, class, lexYAMLLine(text))
}

func renderComposedHighlightedText(builder *bytes.Buffer, text string, highlights []highlightRange, class string, syntax []syntaxRange) {
	runes := []rune(text)
	highlights = normalizedHighlightRanges(highlights, len(runes))
	syntax = normalizedSyntaxRanges(syntax, len(runes))

	if len(highlights) == 0 {
		renderSyntaxSegment(builder, runes, 0, len(runes), syntax)
		return
	}

	cursor := 0
	for _, highlight := range highlights {
		if highlight.start > cursor {
			renderSyntaxSegment(builder, runes, cursor, highlight.start, syntax)
		}
		if highlight.start < highlight.end {
			fmt.Fprintf(builder, "<span class=\"inline-change %s\">", escape(class))
			renderSyntaxSegment(builder, runes, highlight.start, highlight.end, syntax)
			builder.WriteString("</span>")
			cursor = highlight.end
		}
	}
	if cursor < len(runes) {
		renderSyntaxSegment(builder, runes, cursor, len(runes), syntax)
	}
}

func normalizedHighlightRanges(highlights []highlightRange, textLength int) []highlightRange {
	if len(highlights) == 0 || textLength == 0 {
		return nil
	}
	ranges := append([]highlightRange(nil), highlights...)
	slices.SortFunc(ranges, func(left, right highlightRange) int {
		if left.start != right.start {
			return left.start - right.start
		}
		return left.end - right.end
	})

	normalized := make([]highlightRange, 0, len(ranges))
	cursor := 0
	for _, current := range ranges {
		current.start = clampOffset(current.start, textLength)
		current.end = clampOffset(current.end, textLength)
		if current.start < cursor {
			current.start = cursor
		}
		if current.start >= current.end {
			continue
		}
		normalized = append(normalized, current)
		cursor = current.end
	}
	return normalized
}

func normalizedSyntaxRanges(syntax []syntaxRange, textLength int) []syntaxRange {
	if len(syntax) == 0 || textLength == 0 {
		return nil
	}
	ranges := append([]syntaxRange(nil), syntax...)
	slices.SortFunc(ranges, func(left, right syntaxRange) int {
		if left.start != right.start {
			return left.start - right.start
		}
		return left.end - right.end
	})

	normalized := make([]syntaxRange, 0, len(ranges))
	cursor := 0
	for _, current := range ranges {
		if !isAllowedSyntaxClass(current.class) {
			continue
		}
		current.start = clampOffset(current.start, textLength)
		current.end = clampOffset(current.end, textLength)
		if current.start < cursor {
			current.start = cursor
		}
		if current.start >= current.end {
			continue
		}
		normalized = append(normalized, current)
		cursor = current.end
	}
	return normalized
}

func renderSyntaxSegment(builder *bytes.Buffer, runes []rune, start, end int, syntax []syntaxRange) {
	cursor := start
	for _, token := range syntax {
		if token.end <= start {
			continue
		}
		if token.start >= end {
			break
		}
		tokenStart := max(token.start, start)
		tokenEnd := min(token.end, end)
		if tokenStart > cursor {
			builder.WriteString(escape(string(runes[cursor:tokenStart])))
		}
		if tokenStart < tokenEnd {
			fmt.Fprintf(builder, "<span class=\"%s\">%s</span>",
				escape(token.class),
				escape(string(runes[tokenStart:tokenEnd])),
			)
			cursor = tokenEnd
		}
	}
	if cursor < end {
		builder.WriteString(escape(string(runes[cursor:end])))
	}
}

func isAllowedSyntaxClass(class string) bool {
	_, ok := allowedSyntaxClasses[class]
	return ok
}

func clampOffset(offset, textLength int) int {
	if offset < 0 {
		return 0
	}
	if offset > textLength {
		return textLength
	}
	return offset
}

func renderResourceHeading(builder *bytes.Buffer, resource diff.Resource) {
	label := resourceLabel(resource)
	fmt.Fprintf(builder, "<h3 class=\"resource-heading\" title=\"%s\" aria-label=\"%s\">",
		escape(label),
		escape(label),
	)
	renderResourcePrimary(builder, resource, label)
	renderResourceMeta(builder, resource)
	builder.WriteString("</h3>\n")
}

func renderResourcePrimary(builder *bytes.Buffer, resource diff.Resource, fallback string) {
	builder.WriteString("<span class=\"resource-primary\">")
	switch {
	case resource.Kind != "" && resource.Name != "":
		fmt.Fprintf(builder, "<span class=\"resource-kind\">%s</span> <span class=\"resource-name\">%s</span>",
			escape(resource.Kind),
			escape(resource.Name),
		)
	case resource.Kind != "":
		fmt.Fprintf(builder, "<span class=\"resource-kind\">%s</span>", escape(resource.Kind))
	case resource.Name != "":
		fmt.Fprintf(builder, "<span class=\"resource-name\">%s</span>", escape(resource.Name))
	default:
		fmt.Fprintf(builder, "<span class=\"resource-name\">%s</span>", escape(fallback))
	}
	builder.WriteString("</span>")
}

func renderResourceMeta(builder *bytes.Buffer, resource diff.Resource) {
	if resource.Namespace == "" && resource.Group == "" {
		return
	}
	builder.WriteString(" <span class=\"resource-meta\">")
	if resource.Namespace != "" {
		fmt.Fprintf(builder, "<span class=\"resource-namespace\">%s</span>", escape(resource.Namespace))
	}
	if resource.Namespace != "" && resource.Group != "" {
		builder.WriteString(" <span class=\"resource-meta-separator\" aria-hidden=\"true\">·</span> ")
	}
	if resource.Group != "" {
		fmt.Fprintf(builder, "<span class=\"resource-api-group\">%s</span>", escape(resource.Group))
	}
	builder.WriteString("</span>")
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
