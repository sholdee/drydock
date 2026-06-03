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

const defaultTitle = "drydock desired state diff"

type Options struct {
	Title string
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

func Render(result app.DiffResult, options Options) ([]byte, error) {
	title := strings.TrimSpace(options.Title)
	if title == "" {
		title = defaultTitle
	}

	groups := groupedResults(result.Results)
	added, removed := totalLineChanges(groups)

	var builder bytes.Buffer
	builder.WriteString("<!doctype html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n")
	fmt.Fprintf(&builder, "<title>%s</title>\n", escape(title))
	fmt.Fprintf(&builder, "<link rel=\"icon\" type=\"image/svg+xml\" href=\"%s\">\n", drydockFaviconHref)
	builder.WriteString("<style>")
	builder.WriteString(reviewStyles)
	builder.WriteString("</style>\n</head>\n<body data-view=\"side-by-side\">\n")
	builder.WriteString("<header class=\"report-header\">\n")
	builder.WriteString("<div>\n")
	fmt.Fprintf(&builder, "<h1>%s</h1>\n", escape(title))
	renderSummary(&builder, len(groups), len(result.Results), added, removed)
	builder.WriteString("</div>\n")
	builder.WriteString(drydockLogo)
	builder.WriteString("\n")
	builder.WriteString("</header>\n")
	builder.WriteString("<div class=\"review-layout\">\n")
	renderTree(&builder, groups)
	builder.WriteString("<main class=\"review-main\">\n")
	if len(groups) == 0 {
		builder.WriteString("<p class=\"no-diff\">No rendered manifest differences detected.</p>\n")
	} else {
		renderToolbar(&builder)
		renderGroups(&builder, groups)
	}
	renderDiagnostics(&builder, result.Diagnostics)
	builder.WriteString("</main>\n</div>\n<script>")
	builder.WriteString(reviewScript)
	builder.WriteString("</script>\n</body>\n</html>\n")
	return builder.Bytes(), nil
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

func renderSummary(builder *bytes.Buffer, apps, resources, added, removed int) {
	fmt.Fprintf(builder, "<p class=\"summary\">%s, %s, +%d/-%d</p>\n",
		escape(plural(apps, "app", "apps")),
		escape(plural(resources, "resource", "resources")),
		added,
		removed,
	)
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
	builder.WriteString("<aside class=\"tree\">\n")
	builder.WriteString("<input class=\"tree-search\" data-tree-search type=\"search\" placeholder=\"Search changes\" aria-label=\"Search changed resources\">\n")
	for _, group := range groups {
		fmt.Fprintf(builder, "<section data-tree-app=\"%s\">\n", escape(group.id))
		fmt.Fprintf(builder, "<h2>%s</h2>\n", escape(group.id))
		for _, entry := range group.entries {
			label := resourceLabel(entry.result.Resource)
			searchText := strings.ToLower(strings.Join([]string{
				group.id,
				label,
				string(entry.result.Change),
			}, " "))
			fmt.Fprintf(builder, "<button class=\"tree-resource\" type=\"button\" data-target-resource=\"resource-%d\" data-search-text=\"%s\">%s</button>\n",
				entry.index,
				escape(searchText),
				escape(label),
			)
		}
		builder.WriteString("</section>\n")
	}
	builder.WriteString("</aside>\n")
}

func renderToolbar(builder *bytes.Buffer) {
	builder.WriteString("<div class=\"toolbar\" role=\"toolbar\" aria-label=\"Diff view\">\n")
	builder.WriteString("<button type=\"button\" data-view=\"side-by-side\" aria-pressed=\"true\">Side-by-side</button>\n")
	builder.WriteString("<button type=\"button\" data-view=\"unified\" aria-pressed=\"false\">Unified</button>\n")
	builder.WriteString("</div>\n")
}

func renderGroups(builder *bytes.Buffer, groups []appGroup) {
	builder.WriteString("<section class=\"applications\">\n")
	for _, group := range groups {
		for _, entry := range group.entries {
			renderResource(builder, group.id, entry)
		}
	}
	builder.WriteString("</section>\n")
}

func renderResource(builder *bytes.Buffer, appID string, entry groupEntry) {
	result := entry.result
	hunks := parseUnifiedDiff(result.Diff)
	fmt.Fprintf(builder, "<article class=\"resource\" data-resource-id=\"resource-%d\" data-result-index=\"%d\" data-change=\"%s\">\n",
		entry.index,
		entry.index,
		escape(string(result.Change)),
	)
	fmt.Fprintf(builder, "<h3>%s</h3>\n", escape(resourceLabel(result.Resource)))
	fmt.Fprintf(builder, "<p class=\"resource-meta\">%s &middot; %s</p>\n", escape(appID), escape(string(result.Change)))
	renderSideBySideTable(builder, hunks)
	renderUnifiedTable(builder, hunks)
	builder.WriteString("</article>\n")
}

func renderSideBySideTable(builder *bytes.Buffer, hunks []diffHunk) {
	builder.WriteString("<table class=\"diff-table side-by-side\">\n<tbody>\n")
	for _, hunk := range hunks {
		leftHighlights, rightHighlights := pairedHighlights(hunk.Rows)
		fmt.Fprintf(builder, "<tr class=\"hunk-header\"><th colspan=\"4\">%s</th></tr>\n", escape(hunk.Header))
		for index, row := range hunk.Rows {
			fmt.Fprintf(builder, "<tr class=\"diff-row %s\">", escape(row.Kind))
			renderLineNumberCell(builder, row.LeftNumber)
			builder.WriteString("<td class=\"line-code\">")
			renderHighlightedText(builder, row.LeftText, leftHighlights[index], "removed")
			builder.WriteString("</td>")
			renderLineNumberCell(builder, row.RightNumber)
			builder.WriteString("<td class=\"line-code\">")
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
		builder.WriteString("<td class=\"line-number\"></td>")
		return
	}
	fmt.Fprintf(builder, "<td class=\"line-number\">%d</td>", number)
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
