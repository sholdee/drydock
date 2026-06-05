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
