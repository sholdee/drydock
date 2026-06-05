package diffhtml

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/sholdee/drydock/internal/app"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/diff"
)

const (
	defaultTitle                     = "drydock diff"
	defaultResourceRawBytesLimit     = 20 * 1024
	defaultResourceChangedLinesLimit = 400
	lazyResourceRawBytesThreshold    = 250 * 1024
	lazyResourceParsedRowsThreshold  = 2000
	hardResourceRawBytesLimit        = 500 * 1024
	hardResourceParsedRowsLimit      = 20000
)

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

type lazyDiffPayload struct {
	Change  string                 `json:"change"`
	Metrics lazyDiffPayloadMetrics `json:"metrics"`
	Hunks   []lazyDiffPayloadHunk  `json:"hunks"`
}

type lazyDiffPayloadMetrics struct {
	RawBytes     int `json:"rawBytes"`
	RawKiB       int `json:"rawKiB"`
	AddedLines   int `json:"addedLines"`
	RemovedLines int `json:"removedLines"`
	ParsedRows   int `json:"parsedRows"`
}

type lazyDiffPayloadHunk struct {
	Header string               `json:"header"`
	Rows   []lazyDiffPayloadRow `json:"rows"`
}

type lazyDiffPayloadRow struct {
	Kind        string                       `json:"kind"`
	LeftNumber  int                          `json:"leftNumber"`
	RightNumber int                          `json:"rightNumber"`
	LeftText    string                       `json:"leftText"`
	RightText   string                       `json:"rightText"`
	LeftSyntax  []lazyDiffPayloadSyntaxRange `json:"leftSyntax,omitempty"`
	RightSyntax []lazyDiffPayloadSyntaxRange `json:"rightSyntax,omitempty"`
}

type lazyDiffPayloadSyntaxRange struct {
	Start int    `json:"start"`
	End   int    `json:"end"`
	Class string `json:"class"`
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
		if err := renderGroups(&builder, groups); err != nil {
			return nil, err
		}
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

func diffRawKiB(rawBytes int) int {
	if rawBytes == 0 {
		return 0
	}
	return (rawBytes + 1023) / 1024
}

func lazyDiffMetricText(metrics resourceDiffMetrics) string {
	return fmt.Sprintf("%s rows, +%s/-%s, %s KiB",
		formatCount(metrics.parsedRows),
		formatCount(metrics.addedLines),
		formatCount(metrics.removedLines),
		formatCount(diffRawKiB(metrics.rawBytes)),
	)
}

func formatCount(count int) string {
	text := strconv.Itoa(count)
	if len(text) <= 3 {
		return text
	}
	var builder strings.Builder
	firstGroup := len(text) % 3
	if firstGroup == 0 {
		firstGroup = 3
	}
	builder.WriteString(text[:firstGroup])
	for offset := firstGroup; offset < len(text); offset += 3 {
		builder.WriteByte(',')
		builder.WriteString(text[offset : offset+3])
	}
	return builder.String()
}

func lazyDiffPayloadJSON(result diff.Result, metrics resourceDiffMetrics, hunks []diffHunk) (string, error) {
	payload := lazyDiffPayload{
		Change: string(result.Change),
		Metrics: lazyDiffPayloadMetrics{
			RawBytes:     metrics.rawBytes,
			RawKiB:       diffRawKiB(metrics.rawBytes),
			AddedLines:   metrics.addedLines,
			RemovedLines: metrics.removedLines,
			ParsedRows:   metrics.parsedRows,
		},
		Hunks: lazyDiffPayloadHunks(hunks),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return scriptDataSafeJSON(data), nil
}

func lazyDiffPayloadHunks(hunks []diffHunk) []lazyDiffPayloadHunk {
	payloadHunks := make([]lazyDiffPayloadHunk, 0, len(hunks))
	for _, hunk := range hunks {
		payloadRows := make([]lazyDiffPayloadRow, 0, len(hunk.Rows))
		for _, row := range hunk.Rows {
			payloadRows = append(payloadRows, lazyDiffPayloadRowFromDiffRow(row))
		}
		payloadHunks = append(payloadHunks, lazyDiffPayloadHunk{
			Header: hunk.Header,
			Rows:   payloadRows,
		})
	}
	return payloadHunks
}

func lazyDiffPayloadRowFromDiffRow(row diffRow) lazyDiffPayloadRow {
	return lazyDiffPayloadRow{
		Kind:        row.Kind,
		LeftNumber:  row.LeftNumber,
		RightNumber: row.RightNumber,
		LeftText:    row.LeftText,
		RightText:   row.RightText,
		LeftSyntax:  lazyDiffPayloadSyntaxRanges(row.LeftText),
		RightSyntax: lazyDiffPayloadSyntaxRanges(row.RightText),
	}
}

func lazyDiffPayloadSyntaxRanges(text string) []lazyDiffPayloadSyntaxRange {
	runes := []rune(text)
	syntax := normalizedSyntaxRanges(lexYAMLLine(text), len(runes))
	if len(syntax) == 0 {
		return nil
	}
	payload := make([]lazyDiffPayloadSyntaxRange, 0, len(syntax))
	for _, token := range syntax {
		payload = append(payload, lazyDiffPayloadSyntaxRange{
			Start: token.start,
			End:   token.end,
			Class: token.class,
		})
	}
	return payload
}

func scriptDataSafeJSON(data []byte) string {
	replacer := strings.NewReplacer(
		"<", `\u003c`,
		">", `\u003e`,
		"&", `\u0026`,
		"'", `\u0027`,
		"`", `\u0060`,
		"=", `\u003d`,
		"\u2028", `\u2028`,
		"\u2029", `\u2029`,
	)
	return replacer.Replace(string(data))
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
