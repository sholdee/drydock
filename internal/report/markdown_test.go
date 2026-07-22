package report

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/sholdee/drydock/internal/app"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/diff"
)

func TestDiffMarkdownGroupsByApplicationIdentity(t *testing.T) {
	result := app.DiffResult{
		Results: []diff.Result{
			diffResult("argocd", "demo", "cm-one", "-old\n+new\n"),
			diffResult("other", "demo", "cm-two", "-old\n+new\n-old2\n+new2\n"),
		},
	}

	out, meta, err := DiffMarkdown(result, MarkdownOptions{MaxBytes: DefaultMaxBytes})
	if err != nil {
		t.Fatalf("DiffMarkdown() error = %v", err)
	}
	text := string(out)
	for _, want := range []string{"## drydock diff", "**Summary:** 2 apps, 2 resources, +3/-3.", "<summary>other/demo", "<summary>argocd/demo"} {
		if !strings.Contains(text, want) {
			t.Fatalf("markdown missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Changed Applications") || strings.Contains(text, "_Stats_") {
		t.Fatalf("markdown contains redundant app inventory or stats:\n%s", text)
	}
	if strings.Index(text, "<summary>other/demo") > strings.Index(text, "<summary>argocd/demo") {
		t.Fatalf("markdown did not sort larger diff first:\n%s", text)
	}
	if meta.ShownApps != 2 || meta.OmittedApps != 0 {
		t.Fatalf("meta = %#v, want 2 shown and 0 omitted", meta)
	}
}

func TestDiffMarkdownCapsOutputAndPreservesUTF8(t *testing.T) {
	result := app.DiffResult{
		Results: []diff.Result{
			diffResult("argocd", "emoji", "cm", strings.Repeat("- old snowman ☃\n+ new snowman ☃\n", 200)),
		},
	}

	out, meta, err := DiffMarkdown(result, MarkdownOptions{MaxBytes: MinPositiveMaxByte})
	if err != nil {
		t.Fatalf("DiffMarkdown() error = %v", err)
	}
	if len(out) > MinPositiveMaxByte {
		t.Fatalf("markdown length = %d, want <= %d", len(out), MinPositiveMaxByte)
	}
	if !utf8.Valid(out) {
		t.Fatalf("markdown is not valid UTF-8:\n%s", string(out))
	}
	if !meta.Truncated {
		t.Fatalf("meta.Truncated = false, want true")
	}
}

func TestDiffMarkdownRejectsInvalidLimits(t *testing.T) {
	for _, maxBytes := range []int{-1, MinPositiveMaxByte - 1} {
		_, _, err := DiffMarkdown(app.DiffResult{}, MarkdownOptions{MaxBytes: maxBytes})
		if err == nil {
			t.Fatalf("DiffMarkdown(MaxBytes=%d) error = nil, want error", maxBytes)
		}
	}
}

func TestDiffMarkdownUnlimitedIncludesFullDiff(t *testing.T) {
	diffText := strings.Repeat("- old\n+ new\n", 300)
	out, meta, err := DiffMarkdown(app.DiffResult{
		Results: []diff.Result{diffResult("argocd", "demo", "cm", diffText)},
	}, MarkdownOptions{MaxBytes: 0})
	if err != nil {
		t.Fatalf("DiffMarkdown() error = %v", err)
	}
	if !strings.Contains(string(out), diffText) {
		t.Fatalf("unlimited markdown omitted full diff")
	}
	if meta.Truncated {
		t.Fatalf("meta.Truncated = true, want false")
	}
	text := string(out)
	if !strings.Contains(text, "<details open>") {
		t.Fatalf("single-app markdown did not expand details by default:\n%s", text)
	}
}

func TestDiffMarkdownClosesDetailsForMultipleApplications(t *testing.T) {
	out, _, err := DiffMarkdown(app.DiffResult{
		Results: []diff.Result{
			diffResult("argocd", "one", "cm-one", "-old\n+new\n"),
			diffResult("argocd", "two", "cm-two", "-old\n+new\n"),
		},
	}, MarkdownOptions{MaxBytes: DefaultMaxBytes})
	if err != nil {
		t.Fatalf("DiffMarkdown() error = %v", err)
	}
	text := string(out)
	if strings.Contains(text, "<details open>") {
		t.Fatalf("multi-app markdown expanded details by default:\n%s", text)
	}
	if got := strings.Count(text, "<details>"); got != 2 {
		t.Fatalf("closed details count = %d, want 2:\n%s", got, text)
	}
}

func TestDiffMarkdownUsesDynamicFenceForBackticks(t *testing.T) {
	out, _, err := DiffMarkdown(app.DiffResult{
		Results: []diff.Result{diffResult("argocd", "demo", "cm", "- value: ```\n+ value: ````\n")},
	}, MarkdownOptions{MaxBytes: DefaultMaxBytes})
	if err != nil {
		t.Fatalf("DiffMarkdown() error = %v", err)
	}
	if !strings.Contains(string(out), "`````diff\n") {
		t.Fatalf("markdown did not use dynamic fence:\n%s", string(out))
	}
}

func TestDiffMarkdownEscapesDiagnostics(t *testing.T) {
	out, _, err := DiffMarkdown(app.DiffResult{
		Diagnostics: []diagnostic.Diagnostic{{
			Severity: diagnostic.SeverityWarning,
			Category: "render",
			Message:  "message with <tag> and [link](bad)",
		}},
	}, MarkdownOptions{MaxBytes: DefaultMaxBytes})
	if err != nil {
		t.Fatalf("DiffMarkdown() error = %v", err)
	}
	text := string(out)
	if strings.Contains(text, "<tag>") || strings.Contains(text, "[link](bad)") {
		t.Fatalf("diagnostic was not escaped:\n%s", text)
	}
	if !strings.Contains(text, "&lt;tag&gt;") {
		t.Fatalf("diagnostic HTML escape missing:\n%s", text)
	}
	if !strings.Contains(text, "**Summary:** 0 apps, 0 resources, +0/-0, 1 warning.") {
		t.Fatalf("summary did not include compact diagnostic count:\n%s", text)
	}
	if !strings.Contains(text, "No rendered manifest differences detected.") {
		t.Fatalf("no-diff message missing:\n%s", text)
	}
}

func TestDiffMarkdownShowsOmittedDetailsOnlyWhenTruncated(t *testing.T) {
	results := make([]diff.Result, 0, 8)
	for i := range 8 {
		results = append(results, diffResult("argocd", fmt.Sprintf("app-%d", i), "cm", strings.Repeat("- old\n+ new\n", 80)))
	}
	out, meta, err := DiffMarkdown(app.DiffResult{Results: results}, MarkdownOptions{MaxBytes: MinPositiveMaxByte})
	if err != nil {
		t.Fatalf("DiffMarkdown() error = %v", err)
	}
	text := string(out)
	if !meta.Truncated {
		t.Fatalf("meta.Truncated = false, want true")
	}
	if meta.ShownApps >= len(results) {
		t.Fatalf("meta.ShownApps = %d, want fewer than %d", meta.ShownApps, len(results))
	}
	for _, want := range []string{"_Omitted details:", "_Details shown:"} {
		if !strings.Contains(text, want) {
			t.Fatalf("markdown missing %q:\n%s", want, text)
		}
	}
}

func TestRawUnifiedDiffPreservesResultOrder(t *testing.T) {
	results := []diff.Result{
		{Diff: "first\n"},
		{Diff: "second\n"},
	}
	if got := RawUnifiedDiff(results); got != "first\nsecond\n" {
		t.Fatalf("RawUnifiedDiff() = %q", got)
	}
}

func TestImageDiffMarkdownRendersAddedRemovedAndOmitsUnchanged(t *testing.T) {
	out, meta, err := ImageDiffMarkdown(app.ImageDiffResult{
		Added:     []string{"registry.example.com/app:new", "registry.example.com/app:`tick`", "registry.example.com/app:pipe|line\nbreak"},
		Removed:   []string{"registry.example.com/app:old"},
		Unchanged: []string{"registry.example.com/app:same"},
	}, MarkdownOptions{MaxBytes: DefaultMaxBytes})
	if err != nil {
		t.Fatalf("ImageDiffMarkdown() error = %v", err)
	}
	text := string(out)
	for _, want := range []string{
		"## drydock image diff",
		"**Summary:** 3 added, 1 removed.",
		"| Change | Image |",
		"| added | `registry.example.com/app:new` |",
		"| added | `` registry.example.com/app:`tick` `` |",
		"| added | `registry.example.com/app:pipe\\|line break` |",
		"| removed | `registry.example.com/app:old` |",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("markdown missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "registry.example.com/app:same") {
		t.Fatalf("unchanged image rendered:\n%s", text)
	}
	if meta.Truncated || meta.ShownApps != 0 || meta.OmittedApps != 0 {
		t.Fatalf("meta = %#v, want untruncated with app counts unset", meta)
	}
}

func TestImageDiffMarkdownNoChangeOutputCompact(t *testing.T) {
	out, meta, err := ImageDiffMarkdown(app.ImageDiffResult{
		Unchanged: []string{"registry.example.com/app:same"},
	}, MarkdownOptions{MaxBytes: DefaultMaxBytes})
	if err != nil {
		t.Fatalf("ImageDiffMarkdown() error = %v", err)
	}
	text := string(out)
	for _, want := range []string{
		"**Summary:** 0 added, 0 removed.",
		"No rendered image differences detected.",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("markdown missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "| Change | Image |") || strings.Contains(text, "registry.example.com/app:same") {
		t.Fatalf("no-change markdown was not compact:\n%s", text)
	}
	if meta.Truncated {
		t.Fatalf("meta.Truncated = true, want false")
	}
}

func TestImageDiffMarkdownEscapesDiagnosticsAndCountsSummary(t *testing.T) {
	out, _, err := ImageDiffMarkdown(app.ImageDiffResult{
		Diagnostics: []diagnostic.Diagnostic{
			{
				Severity: diagnostic.SeverityWarning,
				Category: "render",
				Message:  "message with <tag> and [link](bad)",
			},
			{
				Severity: diagnostic.SeverityError,
				Category: "image",
				Message:  "error with <secret>",
			},
		},
	}, MarkdownOptions{MaxBytes: DefaultMaxBytes})
	if err != nil {
		t.Fatalf("ImageDiffMarkdown() error = %v", err)
	}
	text := string(out)
	if strings.Contains(text, "<tag>") || strings.Contains(text, "[link](bad)") || strings.Contains(text, "<secret>") {
		t.Fatalf("diagnostic was not escaped:\n%s", text)
	}
	if !strings.Contains(text, "&lt;tag&gt;") || !strings.Contains(text, "\\[link\\]\\(bad\\)") {
		t.Fatalf("diagnostic escape missing:\n%s", text)
	}
	if !strings.Contains(text, "**Summary:** 0 added, 0 removed, 1 warning, 1 error.") {
		t.Fatalf("summary did not include diagnostic counts:\n%s", text)
	}
}

func TestImageDiffMarkdownRejectsInvalidLimits(t *testing.T) {
	for _, maxBytes := range []int{-1, MinPositiveMaxByte - 1} {
		_, _, err := ImageDiffMarkdown(app.ImageDiffResult{}, MarkdownOptions{MaxBytes: maxBytes})
		if err == nil {
			t.Fatalf("ImageDiffMarkdown(MaxBytes=%d) error = nil, want error", maxBytes)
		}
	}
}

func TestImageDiffMarkdownCapsOutputAndPreservesUTF8(t *testing.T) {
	images := make([]string, 0, 80)
	for i := range 80 {
		images = append(images, fmt.Sprintf("registry.example.com/team/image-%02d:%s", i, strings.Repeat("snowman-☃-", 8)))
	}

	out, meta, err := ImageDiffMarkdown(app.ImageDiffResult{Added: images}, MarkdownOptions{MaxBytes: MinPositiveMaxByte})
	if err != nil {
		t.Fatalf("ImageDiffMarkdown() error = %v", err)
	}
	if len(out) > MinPositiveMaxByte {
		t.Fatalf("markdown length = %d, want <= %d", len(out), MinPositiveMaxByte)
	}
	if !utf8.Valid(out) {
		t.Fatalf("markdown is not valid UTF-8:\n%s", string(out))
	}
	if !meta.Truncated {
		t.Fatalf("meta.Truncated = false, want true")
	}
	if !strings.Contains(string(out), "Image rows shown:") || !strings.Contains(string(out), "comment truncated") {
		t.Fatalf("truncation note missing:\n%s", string(out))
	}
}

func TestImageDiffMarkdownUnlimitedIncludesAllRows(t *testing.T) {
	images := make([]string, 0, 300)
	for i := range 300 {
		images = append(images, fmt.Sprintf("registry.example.com/team/image-%03d:tag", i))
	}

	out, meta, err := ImageDiffMarkdown(app.ImageDiffResult{Added: images}, MarkdownOptions{MaxBytes: 0})
	if err != nil {
		t.Fatalf("ImageDiffMarkdown() error = %v", err)
	}
	text := string(out)
	for _, want := range []string{
		"`registry.example.com/team/image-000:tag`",
		"`registry.example.com/team/image-299:tag`",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("unlimited markdown missing %q", want)
		}
	}
	if meta.Truncated {
		t.Fatalf("meta.Truncated = true, want false")
	}
	if strings.Contains(text, "Image rows shown:") {
		t.Fatalf("unlimited markdown included omitted note:\n%s", text)
	}
}

func diffResult(namespace, appName, resourceName, body string) diff.Result {
	return diff.Result{
		Parent: diff.Parent{
			Namespace: namespace,
			Name:      appName,
		},
		Resource: diff.Resource{
			Kind: "ConfigMap",
			Name: resourceName,
		},
		Change: diff.ChangeModified,
		Diff:   "@@ Application modified: " + appName + " @@\n" + body,
	}
}

func TestDiffMarkdownCollapsesWarningsInDetails(t *testing.T) {
	out, _, err := DiffMarkdown(app.DiffResult{
		Diagnostics: []diagnostic.Diagnostic{
			{
				Code:     "kustomize.ksops-compat-substituted",
				Severity: diagnostic.SeverityWarning,
				Category: "kustomize",
				Message:  "first warning",
			},
			{
				Code:     "source.self-repo-near-miss",
				Severity: diagnostic.SeverityWarning,
				Category: "source",
				Message:  "second warning",
			},
		},
	}, MarkdownOptions{MaxBytes: DefaultMaxBytes})
	if err != nil {
		t.Fatalf("DiffMarkdown() error = %v", err)
	}
	text := string(out)
	for _, want := range []string{
		"<details>\n<summary>Diagnostics (2 warnings)</summary>\n\n",
		"- warning `kustomize` first warning\n",
		"- warning `source` second warning\n",
		"\n</details>\n",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("markdown missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Diagnostics:") {
		t.Fatalf("warnings-only report should not render the open Diagnostics list:\n%s", text)
	}
}

func TestDiffMarkdownKeepsErrorsVisibleAndCollapsesWarnings(t *testing.T) {
	out, _, err := DiffMarkdown(app.DiffResult{
		Diagnostics: []diagnostic.Diagnostic{
			{
				Code:     "kustomize.ksops-compat-substituted",
				Severity: diagnostic.SeverityWarning,
				Category: "kustomize",
				Message:  "placeholder warning",
			},
			{
				Code:     "render.failed",
				Severity: diagnostic.SeverityError,
				Category: "render",
				Message:  "render exploded",
			},
		},
	}, MarkdownOptions{MaxBytes: DefaultMaxBytes})
	if err != nil {
		t.Fatalf("DiffMarkdown() error = %v", err)
	}
	text := string(out)
	for _, want := range []string{
		"Diagnostics:\n- error `render` render exploded\n",
		"<details>\n<summary>1 warning</summary>\n\n",
		"- warning `kustomize` placeholder warning\n",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("markdown missing %q:\n%s", want, text)
		}
	}
	errorAt := strings.Index(text, "render exploded")
	detailsAt := strings.Index(text, "<details>")
	if errorAt == -1 || detailsAt == -1 || errorAt > detailsAt {
		t.Fatalf("error bullet should render before the collapsed warnings block:\n%s", text)
	}
}

func TestDiffMarkdownErrorsOnlyRendersOpenList(t *testing.T) {
	out, _, err := DiffMarkdown(app.DiffResult{
		Diagnostics: []diagnostic.Diagnostic{{
			Code:     "render.failed",
			Severity: diagnostic.SeverityError,
			Category: "render",
			Message:  "render exploded",
		}},
	}, MarkdownOptions{MaxBytes: DefaultMaxBytes})
	if err != nil {
		t.Fatalf("DiffMarkdown() error = %v", err)
	}
	text := string(out)
	if !strings.Contains(text, "Diagnostics:\n- error `render` render exploded\n") {
		t.Fatalf("markdown missing open error list:\n%s", text)
	}
	if strings.Contains(text, "<details>\n<summary>") && strings.Contains(text, "warning") {
		t.Fatalf("errors-only report should not render a warnings block:\n%s", text)
	}
}

func TestDiffMarkdownAggregatesRepeatedDiagnosticCodes(t *testing.T) {
	out, _, err := DiffMarkdown(app.DiffResult{
		Diagnostics: []diagnostic.Diagnostic{
			{
				Code:     "kustomize.ksops-compat-substituted",
				Severity: diagnostic.SeverityWarning,
				Category: "kustomize",
				Message:  "substituted 2 sops files",
			},
			{
				Code:     "kustomize.ksops-compat-substituted",
				Severity: diagnostic.SeverityWarning,
				Category: "kustomize",
				Message:  "substituted 2 sops files",
			},
			{
				Code:     "kustomize.ksops-compat-substituted",
				Severity: diagnostic.SeverityWarning,
				Category: "kustomize",
				Message:  "substituted 5 sops files",
			},
		},
	}, MarkdownOptions{MaxBytes: DefaultMaxBytes})
	if err != nil {
		t.Fatalf("DiffMarkdown() error = %v", err)
	}
	text := string(out)
	for _, want := range []string{
		"- warning `kustomize.ksops-compat-substituted` × 3\n",
		"  - substituted 2 sops files × 2\n",
		"  - substituted 5 sops files\n",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("markdown missing %q:\n%s", want, text)
		}
	}
}

func TestDiffMarkdownAggregatesEmptyCodeViaStableCode(t *testing.T) {
	out, _, err := DiffMarkdown(app.DiffResult{
		Diagnostics: []diagnostic.Diagnostic{
			{
				Severity:   diagnostic.SeverityWarning,
				Category:   "appset",
				Message:    "generator one skipped",
				Provenance: diagnostic.Provenance{Path: "apps/one.yaml", Pointer: "spec.generators"},
			},
			{
				Severity:   diagnostic.SeverityWarning,
				Category:   "appset",
				Message:    "generator two skipped",
				Provenance: diagnostic.Provenance{Path: "apps/two.yaml", Pointer: "spec.generators"},
			},
		},
	}, MarkdownOptions{MaxBytes: DefaultMaxBytes})
	if err != nil {
		t.Fatalf("DiffMarkdown() error = %v", err)
	}
	text := string(out)
	if !strings.Contains(text, "× 2") {
		t.Fatalf("empty-code diagnostics with a shared stable code should aggregate:\n%s", text)
	}
	for _, want := range []string{"generator one skipped", "generator two skipped"} {
		if !strings.Contains(text, want) {
			t.Fatalf("markdown missing aggregated instance %q:\n%s", want, text)
		}
	}
}

func TestDiffMarkdownDiagnosticsModeErrors(t *testing.T) {
	out, _, err := DiffMarkdown(app.DiffResult{
		Diagnostics: []diagnostic.Diagnostic{
			{
				Code:     "kustomize.ksops-compat-substituted",
				Severity: diagnostic.SeverityWarning,
				Category: "kustomize",
				Message:  "placeholder warning",
			},
			{
				Code:     "render.failed",
				Severity: diagnostic.SeverityError,
				Category: "render",
				Message:  "render exploded",
			},
		},
	}, MarkdownOptions{MaxBytes: DefaultMaxBytes, Diagnostics: DiagnosticsModeErrors})
	if err != nil {
		t.Fatalf("DiffMarkdown() error = %v", err)
	}
	text := string(out)
	if !strings.Contains(text, "- error `render` render exploded\n") {
		t.Fatalf("errors mode should keep the error bullet:\n%s", text)
	}
	if strings.Contains(text, "placeholder warning") || strings.Contains(text, "<details>\n<summary>1 warning") {
		t.Fatalf("errors mode should omit warnings entirely:\n%s", text)
	}
	if !strings.Contains(text, "**Summary:** 0 apps, 0 resources, +0/-0, 1 warning, 1 error.") {
		t.Fatalf("summary counts should be unaffected by the diagnostics mode:\n%s", text)
	}
}

func TestDiffMarkdownDiagnosticsModeNone(t *testing.T) {
	out, _, err := DiffMarkdown(app.DiffResult{
		Diagnostics: []diagnostic.Diagnostic{
			{
				Code:     "kustomize.ksops-compat-substituted",
				Severity: diagnostic.SeverityWarning,
				Category: "kustomize",
				Message:  "placeholder warning",
			},
			{
				Code:     "render.failed",
				Severity: diagnostic.SeverityError,
				Category: "render",
				Message:  "render exploded",
			},
		},
	}, MarkdownOptions{MaxBytes: DefaultMaxBytes, Diagnostics: DiagnosticsModeNone})
	if err != nil {
		t.Fatalf("DiffMarkdown() error = %v", err)
	}
	text := string(out)
	for _, unwanted := range []string{"Diagnostics:", "placeholder warning", "render exploded", "<summary>"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("none mode should omit the diagnostics section, found %q:\n%s", unwanted, text)
		}
	}
	if !strings.Contains(text, "**Summary:** 0 apps, 0 resources, +0/-0, 1 warning, 1 error.") {
		t.Fatalf("summary counts should be unaffected by the diagnostics mode:\n%s", text)
	}
}

func TestDiffMarkdownRejectsInvalidDiagnosticsMode(t *testing.T) {
	_, _, err := DiffMarkdown(app.DiffResult{}, MarkdownOptions{MaxBytes: DefaultMaxBytes, Diagnostics: DiagnosticsMode("verbose")})
	if err == nil {
		t.Fatal("DiffMarkdown(Diagnostics=verbose) error = nil, want error")
	}
	_, _, err = ImageDiffMarkdown(app.ImageDiffResult{}, MarkdownOptions{MaxBytes: DefaultMaxBytes, Diagnostics: DiagnosticsMode("verbose")})
	if err == nil {
		t.Fatal("ImageDiffMarkdown(Diagnostics=verbose) error = nil, want error")
	}
}

func TestDiffMarkdownDiagnosticLimitCountsOmittedInstances(t *testing.T) {
	diags := make([]diagnostic.Diagnostic, 0, 6)
	for i := range 6 {
		diags = append(diags, diagnostic.Diagnostic{
			Code:     fmt.Sprintf("discovery.duplicate-%d", i),
			Severity: diagnostic.SeverityWarning,
			Category: "discovery",
			Message:  fmt.Sprintf("duplicate resource %d", i),
		})
	}
	out, _, err := DiffMarkdown(app.DiffResult{Diagnostics: diags},
		MarkdownOptions{MaxBytes: DefaultMaxBytes, DiagnosticLimit: 4})
	if err != nil {
		t.Fatalf("DiffMarkdown() error = %v", err)
	}
	text := string(out)
	if !strings.Contains(text, "_... and 2 more diagnostics omitted._") {
		t.Fatalf("omitted trailer missing or wrong:\n%s", text)
	}
	if !strings.Contains(text, "duplicate resource 3") || strings.Contains(text, "duplicate resource 4") {
		t.Fatalf("limit should render the first 4 warnings only:\n%s", text)
	}
}

func TestDiffMarkdownDiagnosticLimitSpansErrorsThenWarnings(t *testing.T) {
	out, _, err := DiffMarkdown(app.DiffResult{
		Diagnostics: []diagnostic.Diagnostic{
			{Code: "render.failed-a", Severity: diagnostic.SeverityError, Category: "render", Message: "error one"},
			{Code: "render.failed-b", Severity: diagnostic.SeverityError, Category: "render", Message: "error two"},
			{Code: "discovery.duplicate-a", Severity: diagnostic.SeverityWarning, Category: "discovery", Message: "warning one"},
			{Code: "discovery.duplicate-b", Severity: diagnostic.SeverityWarning, Category: "discovery", Message: "warning two"},
		},
	}, MarkdownOptions{MaxBytes: DefaultMaxBytes, DiagnosticLimit: 3})
	if err != nil {
		t.Fatalf("DiffMarkdown() error = %v", err)
	}
	text := string(out)
	for _, want := range []string{"error one", "error two", "warning one"} {
		if !strings.Contains(text, want) {
			t.Fatalf("markdown missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "warning two") {
		t.Fatalf("limit of 3 should omit the fourth diagnostic:\n%s", text)
	}
	if !strings.Contains(text, "_... and 1 more diagnostics omitted._") {
		t.Fatalf("omitted trailer missing:\n%s", text)
	}
}

func TestImageDiffMarkdownCollapsesWarningsInDetails(t *testing.T) {
	out, _, err := ImageDiffMarkdown(app.ImageDiffResult{
		Added: []string{"registry.example.com/app:v2"},
		Diagnostics: []diagnostic.Diagnostic{{
			Code:     "kustomize.ksops-compat-substituted",
			Severity: diagnostic.SeverityWarning,
			Category: "kustomize",
			Message:  "placeholder warning",
		}},
	}, MarkdownOptions{MaxBytes: DefaultMaxBytes})
	if err != nil {
		t.Fatalf("ImageDiffMarkdown() error = %v", err)
	}
	text := string(out)
	for _, want := range []string{
		"<details>\n<summary>Diagnostics (1 warning)</summary>\n\n",
		"- warning `kustomize` placeholder warning\n",
		"\n</details>\n",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("markdown missing %q:\n%s", want, text)
		}
	}
}

func TestImageDiffMarkdownDiagnosticsModeNone(t *testing.T) {
	out, _, err := ImageDiffMarkdown(app.ImageDiffResult{
		Diagnostics: []diagnostic.Diagnostic{{
			Severity: diagnostic.SeverityWarning,
			Category: "render",
			Message:  "placeholder warning",
		}},
	}, MarkdownOptions{MaxBytes: DefaultMaxBytes, Diagnostics: DiagnosticsModeNone})
	if err != nil {
		t.Fatalf("ImageDiffMarkdown() error = %v", err)
	}
	if strings.Contains(string(out), "placeholder warning") {
		t.Fatalf("none mode should omit diagnostics:\n%s", string(out))
	}
}

func TestParseDiagnosticsMode(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  DiagnosticsMode
	}{
		{value: "", want: DiagnosticsModeAll},
		{value: "all", want: DiagnosticsModeAll},
		{value: " Errors", want: DiagnosticsModeErrors},
		{value: "NONE", want: DiagnosticsModeNone},
	} {
		got, err := ParseDiagnosticsMode(tc.value)
		if err != nil {
			t.Fatalf("ParseDiagnosticsMode(%q) error = %v", tc.value, err)
		}
		if got != tc.want {
			t.Fatalf("ParseDiagnosticsMode(%q) = %q, want %q", tc.value, got, tc.want)
		}
	}
	if _, err := ParseDiagnosticsMode("verbose"); err == nil {
		t.Fatal("ParseDiagnosticsMode(verbose) error = nil, want error")
	}
}

func TestDiffMarkdownAggregatedParentLinesCountAgainstLimit(t *testing.T) {
	diags := make([]diagnostic.Diagnostic, 0, 3)
	for _, message := range []string{"first body", "second body", "third body"} {
		diags = append(diags, diagnostic.Diagnostic{
			Code:     "discovery.duplicate",
			Severity: diagnostic.SeverityWarning,
			Category: "discovery",
			Message:  message,
		})
	}
	out, _, err := DiffMarkdown(app.DiffResult{Diagnostics: diags},
		MarkdownOptions{MaxBytes: DefaultMaxBytes, DiagnosticLimit: 2})
	if err != nil {
		t.Fatalf("DiffMarkdown() error = %v", err)
	}
	text := string(out)
	for _, want := range []string{
		"- warning `discovery.duplicate` × 3\n",
		"  - first body\n",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("markdown missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "second body") || strings.Contains(text, "third body") {
		t.Fatalf("parent line should consume one unit of the limit, leaving room for one entry only:\n%s", text)
	}
	if !strings.Contains(text, "_... and 2 more diagnostics omitted._") {
		t.Fatalf("omitted trailer missing or wrong:\n%s", text)
	}
}

func TestDiffMarkdownOmittedTrailerIsAStandaloneParagraph(t *testing.T) {
	diags := make([]diagnostic.Diagnostic, 0, 3)
	for i := range 3 {
		diags = append(diags, diagnostic.Diagnostic{
			Code:     fmt.Sprintf("discovery.duplicate-%d", i),
			Severity: diagnostic.SeverityWarning,
			Category: "discovery",
			Message:  fmt.Sprintf("duplicate %d", i),
		})
	}
	out, _, err := DiffMarkdown(app.DiffResult{Diagnostics: diags},
		MarkdownOptions{MaxBytes: DefaultMaxBytes, DiagnosticLimit: 2})
	if err != nil {
		t.Fatalf("DiffMarkdown() error = %v", err)
	}
	if !strings.Contains(string(out), "\n\n_... and 1 more diagnostics omitted._\n") {
		t.Fatalf("omitted trailer should be separated from the list by a blank line:\n%s", string(out))
	}
}

func TestDiffMarkdownDropsWarningsWhenErrorsDoNotFit(t *testing.T) {
	long := strings.Repeat("x", 200)
	diags := make([]diagnostic.Diagnostic, 0, 9)
	for i := range 8 {
		diags = append(diags, diagnostic.Diagnostic{
			Code:     fmt.Sprintf("render.failed-%d", i),
			Severity: diagnostic.SeverityError,
			Category: "render",
			Message:  fmt.Sprintf("error %d %s", i, long),
		})
	}
	diags = append(diags, diagnostic.Diagnostic{
		Code:     "kustomize.ksops-compat-substituted",
		Severity: diagnostic.SeverityWarning,
		Category: "kustomize",
		Message:  "short warning",
	})
	out, meta, err := DiffMarkdown(app.DiffResult{Diagnostics: diags},
		MarkdownOptions{MaxBytes: MinPositiveMaxByte})
	if err != nil {
		t.Fatalf("DiffMarkdown() error = %v", err)
	}
	text := string(out)
	if strings.Contains(text, "short warning") || strings.Contains(text, "<summary>1 warning</summary>") {
		t.Fatalf("collapsed warnings must not render when the open errors list was dropped:\n%s", text)
	}
	if !meta.Truncated {
		t.Fatal("meta.Truncated = false, want true when the errors block is dropped")
	}
}
