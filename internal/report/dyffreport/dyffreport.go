// Package dyffreport renders manifest diff results with the dyff library
// (https://github.com/homeport/dyff). It runs in-process; no dyff binary is
// executed.
package dyffreport

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode"

	"github.com/gonvenience/bunt"
	"github.com/gonvenience/term"
	"github.com/gonvenience/ytbx"
	"github.com/homeport/dyff/pkg/dyff"
	yamlv3 "go.yaml.in/yaml/v3"

	"github.com/sholdee/drydock/internal/diff"
)

// Write renders one dyff report per resource, each preceded by the resource
// header. A removed and an added Kustomize-generated ConfigMap that differ only
// by name hash are reported together as one rename (see Items). It compares
// the result display bodies, so Secret redaction and ignore rules already
// applied by the diff engine are preserved. When dyff cannot compare a resource or finds no structural difference although the
// unified diff is non-empty, the resource falls back to its unified diff so a
// real change is never silently dropped.
//
// dyff styles output through process-wide bunt settings, so Write is not safe
// to call concurrently with itself or other bunt users.
func Write(w io.Writer, results []diff.Result, color bool) error {
	// Measure once: dyff re-reads the terminal for every decision, and on some
	// platforms that probes the whole process table.
	width, height := term.GetTerminalSize()
	defer configure(color, width, height)()

	for _, item := range Items(results) {
		if err := writeItem(w, item, color); err != nil {
			return err
		}
	}
	return nil
}

// Fixed size for GitHubBody so the report never depends on the terminal or
// pipe it runs under. It is wide enough that short order changes use the
// `- a, b` / `+ b, a` form GitHub colors instead of a side-by-side table.
const (
	githubWidth  = 200
	githubHeight = 25
)

// configure sets dyff's process-wide color mode and terminal size and returns
// a function that restores the defaults. True color stays off so output does
// not depend on COLORTERM.
func configure(color bool, width, height int) func() {
	state := bunt.ON
	if !color {
		state = bunt.OFF
	}
	bunt.SetColorSettings(state, bunt.OFF)
	prevWidth, prevHeight := term.FixedTerminalWidth, term.FixedTerminalHeight
	term.FixedTerminalWidth, term.FixedTerminalHeight = width, height
	return func() {
		bunt.SetColorSettings(bunt.AUTO, bunt.AUTO)
		term.FixedTerminalWidth, term.FixedTerminalHeight = prevWidth, prevHeight
	}
}

// GitHubBody renders one result in dyff's GitHub style, the same format as
// `dyff between -o github`, for embedding in a markdown ```diff fence: path
// lines as `@@ path @@`, the resource identity as `# apiVersion/kind/ns/name`,
// and `!` before the change type. The text is plain; GitHub colors the fence.
// Like Write, it falls back to the unified diff when dyff cannot produce a
// structural report. Not safe for concurrent use (see Write).
func GitHubBody(result diff.Result) string {
	return Item{results: []diff.Result{result}}.GitHubBody()
}

// GitHubBody renders the item like the package-level GitHubBody. Items built
// by Items report a renamed generated resource as one change. Not safe for
// concurrent use (see Write).
func (item Item) GitHubBody() string {
	defer configure(false, githubWidth, githubHeight)()

	var out strings.Builder
	for _, e := range item.entries() {
		out.WriteString(e.githubBody())
	}
	return out.String()
}

func (e entry) githubBody() string {
	if e.err != nil {
		return fmt.Sprintf("# dyff: %v; showing unified diff\n%s", e.err, e.fallback)
	}
	report := e.report
	// dyff only prints the per-change resource identity line when the input
	// has several documents, but every report here covers a single resource.
	// Its document count is read from the report copy only, so padding it
	// enables the line without altering the compared documents. Added
	// resources have no before document, so pad to two.
	documents := slices.Clip(report.From.Documents)
	for len(documents) < 2 {
		documents = append(documents, nil)
	}
	report.From.Documents = documents

	github := &dyff.DiffSyntaxReport{
		PathPrefix:            "@@",
		RootDescriptionPrefix: "#",
		ChangeTypePrefix:      "!",
		Report:                report,
		NoTableStyle:          true,
		DoNotInspectCerts:     true,
		OmitHeader:            true,
		PrefixMultiline:       true,
		MinorChangeThreshold:  0.1,
		MultilineContextLines: 4,
		UseIndentLines:        true,
	}
	var out bytes.Buffer
	if err := github.WriteReport(&out); err != nil {
		return fmt.Sprintf("# dyff: %v; showing unified diff\n%s", err, e.fallback)
	}
	return strings.Trim(out.String(), "\n") + "\n"
}

func writeItem(w io.Writer, item Item, color bool) error {
	for _, e := range item.entries() {
		if err := e.write(w, color); err != nil {
			return err
		}
	}
	return nil
}

func (e entry) write(w io.Writer, color bool) error {
	header := e.header
	if color {
		header = "\x1b[1m" + header + "\x1b[0m"
	}
	if _, err := fmt.Fprintln(w, header); err != nil {
		return err
	}
	if e.err != nil {
		_, writeErr := fmt.Fprintf(w, "(dyff: %v; showing unified diff)\n%s", e.err, e.fallback)
		return writeErr
	}
	human := &dyff.HumanReport{
		Report:                e.report,
		Indent:                2,
		UseIndentLines:        true,
		MinorChangeThreshold:  0.1,
		MultilineContextLines: 4,
		OmitHeader:            true,
		DoNotInspectCerts:     true,
	}
	return human.WriteReport(w)
}

// entry is one dyff comparison ready to render. When err is set, fallback (the
// unified diff) is shown instead of the report.
type entry struct {
	header   string
	fallback string
	report   dyff.Report
	err      error
}

func newEntry(header, fallback, before, after string) entry {
	report, err := compare(header, before, after)
	if err == nil && len(report.Diffs) == 0 {
		err = errors.New("no structural differences found")
	}
	return entry{header: header, fallback: fallback, report: report, err: err}
}

// entries compares the item. A pair of results stays one entry only when dyff
// reports it as a rename of one resource; otherwise (dyff found the two
// resources too different, or could not compare them) each result is compared
// on its own, as if it had never been paired.
func (item Item) entries() []entry {
	if len(item.results) == 2 {
		removed, added := item.results[0], item.results[1]
		if removed.Change == diff.ChangeAdded {
			removed, added = added, removed
		}
		header := added.Header() + " (renamed from " + removed.Resource.Name + ")"
		report, err := compare(header, removed.Before, added.After)
		if err == nil && len(report.Diffs) > 0 && isRename(report) {
			return []entry{{header: header, report: report}}
		}
	}
	entries := make([]entry, 0, len(item.results))
	for _, result := range item.results {
		entries = append(entries, newEntry(result.Header(), result.Diff, result.Before, result.After))
	}
	return entries
}

// isRename reports whether the report of a removed and an added document
// describes changes inside one matched document. When dyff does not match the
// two, it reports a document-level removal, addition, or order change, all of
// which have an empty path.
func isRename(report dyff.Report) bool {
	for _, d := range report.Diffs {
		if d.Path == nil || len(d.Path.PathElements) == 0 {
			return false
		}
	}
	return true
}

func compare(header, before, after string) (dyff.Report, error) {
	from, err := inputFile(header, before)
	if err != nil {
		return dyff.Report{}, fmt.Errorf("load before body: %w", err)
	}
	to, err := inputFile(header, after)
	if err != nil {
		return dyff.Report{}, fmt.Errorf("load after body: %w", err)
	}
	if unsafeDocuments(from.Documents) || unsafeDocuments(to.Documents) {
		return dyff.Report{}, errors.New("values contain control characters")
	}
	// FormatStrings stays off: it round-trips number-like strings through
	// float64 and hides real value changes.
	return dyff.CompareInputFiles(from, to,
		dyff.KubernetesEntityDetection(true),
		dyff.DetectRenames(true),
	)
}

func inputFile(location, body string) (ytbx.InputFile, error) {
	documents, err := ytbx.LoadDocuments([]byte(body))
	if err != nil {
		return ytbx.InputFile{}, err
	}
	return ytbx.InputFile{Location: location, Documents: documents}, nil
}

// unsafeDocuments reports whether any decoded string would print control
// characters or extra lines that the YAML-escaped unified diff keeps inert:
// dyff writes decoded values raw, so a manifest could otherwise move the
// cursor, plant terminal links, or forge `@@` lines in a PR comment. Multi-line
// values and tabs are legitimate; keys must be single-line.
func unsafeDocuments(documents []*yamlv3.Node) bool {
	for _, document := range documents {
		if unsafeNode(document, false) {
			return true
		}
	}
	return false
}

func unsafeNode(node *yamlv3.Node, isKey bool) bool {
	if node == nil || node.Kind == yamlv3.AliasNode {
		return false
	}
	if node.Kind == yamlv3.ScalarNode {
		for _, r := range node.Value {
			if r == '\n' && !isKey || r == '\t' {
				continue
			}
			if unicode.IsControl(r) {
				return true
			}
		}
		return false
	}
	for i, child := range node.Content {
		if unsafeNode(child, node.Kind == yamlv3.MappingNode && i%2 == 0) {
			return true
		}
	}
	return false
}
