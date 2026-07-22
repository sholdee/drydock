package report

import (
	"bytes"
	"fmt"
	"html"
	"io"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/sholdee/drydock/internal/app"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/diff"
)

const (
	OutputMarkdown     = "markdown"
	DefaultMaxBytes    = 60000
	MinPositiveMaxByte = 1024
	defaultDiagLimit   = 10
)

type MarkdownOptions struct {
	MaxBytes        int
	DiagnosticLimit int
	Title           string
	Diagnostics     DiagnosticsMode
}

// DiagnosticsMode controls how much of the diagnostics list the markdown
// report embeds. Summary warning/error counts are always kept.
type DiagnosticsMode string

const (
	// DiagnosticsModeAll lists errors openly and collapses warnings inside a
	// <details> block.
	DiagnosticsModeAll DiagnosticsMode = "all"
	// DiagnosticsModeErrors lists errors openly and omits warnings entirely.
	DiagnosticsModeErrors DiagnosticsMode = "errors"
	// DiagnosticsModeNone omits the diagnostics section entirely.
	DiagnosticsModeNone DiagnosticsMode = "none"
)

func ParseDiagnosticsMode(value string) (DiagnosticsMode, error) {
	mode := DiagnosticsMode(strings.ToLower(strings.TrimSpace(value))).Normalize()
	if err := mode.Validate(); err != nil {
		return "", err
	}
	return mode, nil
}

func (mode DiagnosticsMode) Normalize() DiagnosticsMode {
	if mode == "" {
		return DiagnosticsModeAll
	}
	return mode
}

func (mode DiagnosticsMode) Validate() error {
	switch mode.Normalize() {
	case DiagnosticsModeAll, DiagnosticsModeErrors, DiagnosticsModeNone:
		return nil
	default:
		return fmt.Errorf("markdown diagnostics must be all, errors, or none, got %q", string(mode))
	}
}

type MarkdownResult struct {
	Bytes       int
	Truncated   bool
	ShownApps   int
	OmittedApps int
}

type appGroup struct {
	id        string
	resources []diff.Result
	added     int
	removed   int
	diffBytes int
}

type imageDiffRow struct {
	change string
	image  string
}

func WriteDiffMarkdown(w io.Writer, result app.DiffResult, options MarkdownOptions) (MarkdownResult, error) {
	data, meta, err := DiffMarkdown(result, options)
	if err != nil {
		return MarkdownResult{}, err
	}
	_, err = w.Write(data)
	return meta, err
}

func WriteImageDiffMarkdown(w io.Writer, result app.ImageDiffResult, options MarkdownOptions) (MarkdownResult, error) {
	data, meta, err := ImageDiffMarkdown(result, options)
	if err != nil {
		return MarkdownResult{}, err
	}
	_, err = w.Write(data)
	return meta, err
}

func DiffMarkdown(result app.DiffResult, options MarkdownOptions) ([]byte, MarkdownResult, error) {
	if options.MaxBytes < 0 {
		return nil, MarkdownResult{}, fmt.Errorf("markdown max bytes must be greater than or equal to zero")
	}
	if options.MaxBytes > 0 && options.MaxBytes < MinPositiveMaxByte {
		return nil, MarkdownResult{}, fmt.Errorf("markdown max bytes must be zero or at least %d", MinPositiveMaxByte)
	}
	if options.DiagnosticLimit <= 0 {
		options.DiagnosticLimit = defaultDiagLimit
	}
	if strings.TrimSpace(options.Title) == "" {
		options.Title = "drydock diff"
	}
	diagnosticsMode := options.Diagnostics.Normalize()
	if err := diagnosticsMode.Validate(); err != nil {
		return nil, MarkdownResult{}, err
	}

	groups := groupedDiffs(result.Results)
	totalAdded, totalRemoved := totalLineChanges(groups)
	state := markdownState{maxBytes: options.MaxBytes}
	state.appendRequired(fmt.Sprintf("## %s\n\n", escapeMarkdownText(options.Title)))
	state.appendRequired(summaryMarkdown(len(result.Results), len(groups), totalAdded, totalRemoved, result.Diagnostics))
	errorsPart, warningsPart := diagnosticsMarkdown(result.Diagnostics, options.DiagnosticLimit, diagnosticsMode)
	errorsFit := state.appendBounded(errorsPart)
	if len(groups) == 0 {
		state.appendBounded(noDiffMarkdown())
	} else {
		state.appendAppDetails(groups, len(groups) == 1)
	}
	state.appendBounded(omittedMarkdown(groups[state.shownApps:]))
	if errorsFit {
		state.appendBounded(warningsPart)
	}
	state.appendBounded(statsMarkdown(state.truncated, state.shownApps, len(groups)))

	out := state.bytes()
	return out, MarkdownResult{
		Bytes:       len(out),
		Truncated:   state.truncated,
		ShownApps:   state.shownApps,
		OmittedApps: len(groups) - state.shownApps,
	}, nil
}

func ImageDiffMarkdown(result app.ImageDiffResult, options MarkdownOptions) ([]byte, MarkdownResult, error) {
	if options.MaxBytes < 0 {
		return nil, MarkdownResult{}, fmt.Errorf("markdown max bytes must be greater than or equal to zero")
	}
	if options.MaxBytes > 0 && options.MaxBytes < MinPositiveMaxByte {
		return nil, MarkdownResult{}, fmt.Errorf("markdown max bytes must be zero or at least %d", MinPositiveMaxByte)
	}
	if options.DiagnosticLimit <= 0 {
		options.DiagnosticLimit = defaultDiagLimit
	}
	if strings.TrimSpace(options.Title) == "" {
		options.Title = "drydock image diff"
	}
	diagnosticsMode := options.Diagnostics.Normalize()
	if err := diagnosticsMode.Validate(); err != nil {
		return nil, MarkdownResult{}, err
	}

	rows := imageDiffRows(result)
	state := markdownState{maxBytes: options.MaxBytes}
	state.appendRequired(fmt.Sprintf("## %s\n\n", escapeMarkdownText(options.Title)))
	state.appendRequired(imageSummaryMarkdown(len(result.Added), len(result.Removed), result.Diagnostics))
	errorsPart, warningsPart := diagnosticsMarkdown(result.Diagnostics, options.DiagnosticLimit, diagnosticsMode)
	errorsFit := state.appendBounded(errorsPart)
	if len(rows) == 0 {
		state.appendBounded(noImageDiffMarkdown())
	} else {
		state.appendImageRows(rows)
	}
	if errorsFit {
		state.appendBounded(warningsPart)
	}

	out := state.bytes()
	return out, MarkdownResult{
		Bytes:     len(out),
		Truncated: state.truncated,
	}, nil
}

func RawUnifiedDiff(results []diff.Result) string {
	var builder strings.Builder
	for _, item := range results {
		builder.WriteString(item.Diff)
	}
	return builder.String()
}

type markdownState struct {
	buffer    bytes.Buffer
	maxBytes  int
	truncated bool
	shownApps int
}

func (s *markdownState) appendRequired(text string) {
	if s.maxBytes == 0 || s.buffer.Len()+len(text) <= s.maxBytes {
		s.buffer.WriteString(text)
		return
	}
	s.truncated = true
}

func (s *markdownState) appendBounded(text string) bool {
	if text == "" {
		return true
	}
	if s.maxBytes == 0 || s.buffer.Len()+len(text) <= s.maxBytes {
		s.buffer.WriteString(text)
		return true
	}
	s.truncated = true
	return false
}

func (s *markdownState) appendAppDetails(groups []appGroup, openDetails bool) {
	if len(groups) == 0 {
		return
	}
	remaining := s.remaining()
	if remaining <= 0 && s.maxBytes != 0 {
		s.truncated = true
		return
	}
	allocations := allocateAppBudgets(groups, remaining, s.maxBytes == 0)
	for i, group := range groups {
		detail, truncated := appDetailMarkdown(group, allocations[i], openDetails)
		if detail == "" {
			s.truncated = true
			return
		}
		if s.maxBytes != 0 && s.buffer.Len()+len(detail) > s.maxBytes {
			s.truncated = true
			return
		}
		s.buffer.WriteString(detail)
		s.shownApps++
		if truncated {
			s.truncated = true
		}
	}
}

func (s *markdownState) appendImageRows(rows []imageDiffRow) {
	table := "| Change | Image |\n| --- | --- |\n"
	finalNote := imageRowsOmittedMarkdown(0, len(rows), true)
	if s.maxBytes != 0 && s.buffer.Len()+len(table)+len(finalNote) > s.maxBytes {
		s.truncated = true
		s.appendBounded(finalNote)
		return
	}
	s.buffer.WriteString(table)

	shown := 0
	for index, row := range rows {
		line := imageDiffRowMarkdown(row)
		remainingRows := len(rows) - index - 1
		omittedNote := imageRowsOmittedMarkdown(shown+1, len(rows), true)
		requiredAfterRow := 1
		if remainingRows > 0 {
			requiredAfterRow += len(omittedNote)
		}
		if s.maxBytes != 0 && s.buffer.Len()+len(line)+requiredAfterRow > s.maxBytes {
			s.truncated = true
			break
		}
		s.buffer.WriteString(line)
		shown++
	}
	s.buffer.WriteByte('\n')
	if omitted := len(rows) - shown; omitted > 0 {
		s.truncated = true
		s.appendBounded(imageRowsOmittedMarkdown(shown, len(rows), true))
	}
}

func (s *markdownState) remaining() int {
	if s.maxBytes == 0 {
		return 0
	}
	return s.maxBytes - s.buffer.Len()
}

func (s *markdownState) bytes() []byte {
	data := s.buffer.Bytes()
	if s.maxBytes == 0 || len(data) <= s.maxBytes {
		return append([]byte(nil), data...)
	}
	s.truncated = true
	data = data[:s.maxBytes]
	for !utf8.Valid(data) {
		data = data[:len(data)-1]
	}
	return append([]byte(nil), data...)
}

func groupedDiffs(results []diff.Result) []appGroup {
	byID := make(map[string]*appGroup)
	for _, result := range results {
		id := applicationID(result.Parent)
		group := byID[id]
		if group == nil {
			group = &appGroup{id: id}
			byID[id] = group
		}
		group.resources = append(group.resources, result)
		added, removed := lineChanges(result.Diff)
		group.added += added
		group.removed += removed
		group.diffBytes += len(result.Diff)
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

func imageDiffRows(result app.ImageDiffResult) []imageDiffRow {
	rows := make([]imageDiffRow, 0, len(result.Added)+len(result.Removed))
	for _, image := range result.Added {
		rows = append(rows, imageDiffRow{change: "added", image: image})
	}
	for _, image := range result.Removed {
		rows = append(rows, imageDiffRow{change: "removed", image: image})
	}
	return rows
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

func summaryMarkdown(results, apps, added, removed int, diagnostics []diagnostic.Diagnostic) string {
	warnings, errors := diagnosticCounts(diagnostics)
	parts := []string{
		plural(apps, "app", "apps"),
		plural(results, "resource", "resources"),
		fmt.Sprintf("+%d/-%d", added, removed),
	}
	if warnings > 0 {
		parts = append(parts, plural(warnings, "warning", "warnings"))
	}
	if errors > 0 {
		parts = append(parts, plural(errors, "error", "errors"))
	}
	return fmt.Sprintf("**Summary:** %s.\n\n", strings.Join(parts, ", "))
}

func imageSummaryMarkdown(added, removed int, diagnostics []diagnostic.Diagnostic) string {
	warnings, errors := diagnosticCounts(diagnostics)
	parts := []string{
		plural(added, "added", "added"),
		plural(removed, "removed", "removed"),
	}
	if warnings > 0 {
		parts = append(parts, plural(warnings, "warning", "warnings"))
	}
	if errors > 0 {
		parts = append(parts, plural(errors, "error", "errors"))
	}
	return fmt.Sprintf("**Summary:** %s.\n\n", strings.Join(parts, ", "))
}

func plural(count int, singular, multiple string) string {
	if count == 1 {
		return fmt.Sprintf("%d %s", count, singular)
	}
	return fmt.Sprintf("%d %s", count, multiple)
}

func diagnosticCounts(diagnostics []diagnostic.Diagnostic) (int, int) {
	var warnings, errors int
	for _, diag := range diagnostics {
		switch diag.Severity {
		case diagnostic.SeverityError:
			errors++
		case diagnostic.SeverityWarning:
			warnings++
		default:
			warnings++
		}
	}
	return warnings, errors
}

// diagnosticEntry is one distinct rendered diagnostic line; count carries how
// many identical diagnostics it represents.
type diagnosticEntry struct {
	body  string
	count int
}

// diagnosticGroup collects diagnostics sharing one stable code so repeated
// codes render as a single aggregated bullet.
type diagnosticGroup struct {
	code     string
	category string
	severity diagnostic.Severity
	entries  []diagnosticEntry
	total    int
}

// diagnosticsMarkdown renders the diagnostics section as two independently
// appendable parts: errors stay openly listed, warnings collapse inside a
// <details> block so PR comments lead with the diffs.
func diagnosticsMarkdown(diagnostics []diagnostic.Diagnostic, limit int, mode DiagnosticsMode) (string, string) {
	if len(diagnostics) == 0 || mode == DiagnosticsModeNone {
		return "", ""
	}
	var errors, warnings []diagnostic.Diagnostic
	for _, diag := range diagnostics {
		if diag.Severity == diagnostic.SeverityError {
			errors = append(errors, diag)
		} else {
			warnings = append(warnings, diag)
		}
	}

	remaining := limit
	var errorsPart string
	if len(errors) > 0 {
		var builder strings.Builder
		builder.WriteString("Diagnostics:\n")
		remaining = writeDiagnosticList(&builder, groupDiagnostics(errors), remaining)
		builder.WriteByte('\n')
		errorsPart = builder.String()
	}
	if mode == DiagnosticsModeErrors || len(warnings) == 0 {
		return errorsPart, ""
	}

	var builder strings.Builder
	summary := plural(len(warnings), "warning", "warnings")
	if len(errors) == 0 {
		summary = "Diagnostics (" + summary + ")"
	}
	fmt.Fprintf(&builder, "<details>\n<summary>%s</summary>\n\n", summary)
	writeDiagnosticList(&builder, groupDiagnostics(warnings), remaining)
	builder.WriteString("\n</details>\n\n")
	return errorsPart, builder.String()
}

// groupDiagnostics buckets diagnostics by stable code in first-seen order and
// dedupes identical rendered bodies within each bucket.
func groupDiagnostics(diagnostics []diagnostic.Diagnostic) []diagnosticGroup {
	groups := make([]diagnosticGroup, 0, len(diagnostics))
	index := make(map[string]int, len(diagnostics))
	for _, diag := range diagnostics {
		code := diag.Code
		if code == "" {
			code = diagnostic.StableCode(diag)
		}
		at, ok := index[code]
		if !ok {
			at = len(groups)
			index[code] = at
			groups = append(groups, diagnosticGroup{code: code, category: diag.Category, severity: diag.Severity})
		}
		group := &groups[at]
		group.total++
		body := diagnosticBodyMarkdown(diag)
		deduped := false
		for i := range group.entries {
			if group.entries[i].body == body {
				group.entries[i].count++
				deduped = true
				break
			}
		}
		if !deduped {
			group.entries = append(group.entries, diagnosticEntry{body: body, count: 1})
		}
	}
	return groups
}

// writeDiagnosticList renders grouped diagnostics as at most limit markdown
// bullet lines (aggregated group headers included) and returns the unused
// line budget. Diagnostic instances whose bodies never render are counted
// into a trailing omitted note.
func writeDiagnosticList(builder *strings.Builder, groups []diagnosticGroup, limit int) int {
	remaining := limit
	omitted := 0
	for _, group := range groups {
		if remaining <= 0 {
			omitted += group.total
			continue
		}
		if group.total == 1 {
			writeDiagnosticBullet(builder, group)
			remaining--
			continue
		}
		fmt.Fprintf(builder, "- %s %s × %d\n", escapeMarkdownText(string(group.severity)), markdownCodeSpan(group.code), group.total)
		remaining--
		for _, entry := range group.entries {
			if remaining <= 0 {
				omitted += entry.count
				continue
			}
			builder.WriteString("  - ")
			builder.WriteString(entry.body)
			if entry.count > 1 {
				fmt.Fprintf(builder, " × %d", entry.count)
			}
			builder.WriteByte('\n')
			remaining--
		}
	}
	if omitted > 0 {
		fmt.Fprintf(builder, "\n_... and %d more diagnostics omitted._\n", omitted)
	}
	return remaining
}

func writeDiagnosticBullet(builder *strings.Builder, group diagnosticGroup) {
	builder.WriteString("- ")
	builder.WriteString(escapeMarkdownText(string(group.severity)))
	builder.WriteString(" ")
	if group.category != "" {
		builder.WriteString("`")
		builder.WriteString(escapeCodeSpan(group.category))
		builder.WriteString("` ")
	}
	builder.WriteString(group.entries[0].body)
	builder.WriteByte('\n')
}

func diagnosticBodyMarkdown(diag diagnostic.Diagnostic) string {
	var builder strings.Builder
	builder.WriteString(escapeMarkdownText(singleLine(diag.Message)))
	if diag.Provenance.Path != "" {
		builder.WriteString(" (")
		builder.WriteString(escapeMarkdownText(diag.Provenance.Path))
		if diag.Provenance.Pointer != "" {
			builder.WriteString(" ")
			builder.WriteString(escapeMarkdownText(diag.Provenance.Pointer))
		}
		builder.WriteString(")")
	}
	return builder.String()
}

func noDiffMarkdown() string {
	return "No rendered manifest differences detected.\n\n"
}

func noImageDiffMarkdown() string {
	return "No rendered image differences detected.\n\n"
}

func imageDiffRowMarkdown(row imageDiffRow) string {
	return fmt.Sprintf("| %s | %s |\n", row.change, markdownCodeSpan(tableCellText(row.image)))
}

func imageRowsOmittedMarkdown(shown, total int, truncated bool) string {
	if total == 0 {
		if truncated {
			return "_Comment truncated._\n"
		}
		return ""
	}
	if truncated {
		return fmt.Sprintf("_Image rows shown: %d/%d; comment truncated._\n", shown, total)
	}
	return fmt.Sprintf("_Image rows shown: %d/%d._\n", shown, total)
}

func omittedMarkdown(groups []appGroup) string {
	if len(groups) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("_Omitted details: ")
	limit := min(len(groups), 25)
	for index, group := range groups[:limit] {
		if index > 0 {
			builder.WriteString(", ")
		}
		fmt.Fprintf(&builder, "`%s`", escapeCodeSpan(group.id))
	}
	if omitted := len(groups) - limit; omitted > 0 {
		fmt.Fprintf(&builder, ", and %d more", omitted)
	}
	builder.WriteString("._\n\n")
	return builder.String()
}

func statsMarkdown(truncated bool, shown, total int) string {
	if !truncated && shown == total {
		return ""
	}
	if total == 0 {
		if truncated {
			return "_Comment truncated._\n"
		}
		return ""
	}
	if truncated {
		return fmt.Sprintf("_Details shown: %d/%d applications; comment truncated._\n", shown, total)
	}
	return fmt.Sprintf("_Details shown: %d/%d applications._\n", shown, total)
}

func allocateAppBudgets(groups []appGroup, budget int, unlimited bool) []int {
	allocations := make([]int, len(groups))
	if unlimited {
		for i := range allocations {
			allocations[i] = 0
		}
		return allocations
	}
	if len(groups) == 0 || budget <= 0 {
		return allocations
	}
	const minimum = 512
	minTotal := minimum * len(groups)
	if minTotal >= budget {
		shown := budget / minimum
		for i := range min(shown, len(groups)) {
			allocations[i] = minimum
		}
		return allocations
	}
	for i := range allocations {
		allocations[i] = minimum
	}
	remaining := budget - minTotal
	var totalBytes int
	for _, group := range groups {
		totalBytes += max(group.diffBytes, 1)
	}
	for i, group := range groups {
		allocations[i] += remaining * max(group.diffBytes, 1) / totalBytes
	}
	return allocations
}

func appDetailMarkdown(group appGroup, budget int, openDetails bool) (string, bool) {
	diffText := rawGroupDiff(group)
	truncated := false
	if budget > 0 {
		diffText, truncated = excerptDiff(diffText, max(budget/2, 128))
	}
	fence := codeFence(diffText)
	var builder strings.Builder
	detailsTag := "details"
	if openDetails {
		detailsTag = "details open"
	}
	fmt.Fprintf(&builder, "<%s>\n<summary>%s (+%d/-%d, %d resources)</summary>\n\n",
		detailsTag,
		escapeHTML(group.id), group.added, group.removed, len(group.resources))
	builder.WriteString(fence)
	builder.WriteString("diff\n")
	builder.WriteString(diffText)
	if !strings.HasSuffix(diffText, "\n") {
		builder.WriteByte('\n')
	}
	builder.WriteString(fence)
	builder.WriteString("\n")
	if truncated {
		builder.WriteString("\n_Diff truncated for this application._\n")
	}
	builder.WriteString("\n</details>\n\n")
	out := builder.String()
	if budget > 0 && len(out) > budget {
		return "", truncated
	}
	return out, truncated
}

func rawGroupDiff(group appGroup) string {
	var builder strings.Builder
	for _, item := range group.resources {
		builder.WriteString(item.Diff)
	}
	return builder.String()
}

func excerptDiff(text string, maxBytes int) (string, bool) {
	if maxBytes <= 0 || len(text) <= maxBytes {
		return text, false
	}
	marker := "\n@@ drydock skipped remaining diff lines @@\n"
	limit := maxBytes - len(marker)
	if limit <= 0 {
		return strings.TrimLeft(marker, "\n"), true
	}
	cut := validUTF8Prefix(text, limit)
	if index := strings.LastIndex(cut, "\n"); index > 0 {
		cut = cut[:index+1]
	}
	return cut + strings.TrimLeft(marker, "\n"), true
}

func validUTF8Prefix(text string, maxBytes int) string {
	if len(text) <= maxBytes {
		return text
	}
	data := []byte(text[:maxBytes])
	for !utf8.Valid(data) {
		data = data[:len(data)-1]
	}
	return string(data)
}

func codeFence(text string) string {
	longest := 0
	current := 0
	for _, r := range text {
		if r == '`' {
			current++
			longest = max(longest, current)
			continue
		}
		current = 0
	}
	return strings.Repeat("`", max(3, longest+1))
}

func singleLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func escapeMarkdownText(text string) string {
	text = html.EscapeString(text)
	replacer := strings.NewReplacer(
		"\\", "\\\\",
		"`", "\\`",
		"*", "\\*",
		"_", "\\_",
		"{", "\\{",
		"}", "\\}",
		"[", "\\[",
		"]", "\\]",
		"(", "\\(",
		")", "\\)",
		"#", "\\#",
		"+", "\\+",
		"-", "\\-",
		".", "\\.",
		"!", "\\!",
		"|", "\\|",
	)
	return replacer.Replace(text)
}

func escapeCodeSpan(text string) string {
	return strings.ReplaceAll(text, "`", "\\`")
}

func markdownCodeSpan(text string) string {
	longest := 0
	current := 0
	for _, r := range text {
		if r == '`' {
			current++
			longest = max(longest, current)
			continue
		}
		current = 0
	}
	delimiter := strings.Repeat("`", longest+1)
	if strings.HasPrefix(text, "`") || strings.HasSuffix(text, "`") || strings.Contains(text, delimiter) {
		text = " " + text + " "
	}
	return delimiter + text + delimiter
}

func tableCellText(text string) string {
	text = singleLine(text)
	text = strings.ReplaceAll(text, "|", `\|`)
	return text
}

func escapeHTML(text string) string {
	return html.EscapeString(text)
}
