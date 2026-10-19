package report

import (
	"fmt"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/app"
	"github.com/sholdee/drydock/internal/diff"
)

func configMapDiff(t *testing.T, appName, name string, oldData, newData map[string]string) diff.Result {
	t.Helper()
	body := func(data map[string]string) diff.Document {
		var b strings.Builder
		fmt.Fprintf(&b, "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: %s\n  namespace: default\ndata:\n", name)
		for k, v := range data {
			fmt.Fprintf(&b, "  %s: %s\n", k, v)
		}
		return diff.Document{
			Parent:   diff.Parent{Namespace: "argocd", Name: appName},
			Resource: diff.Resource{Kind: "ConfigMap", Namespace: "default", Name: name},
			Body:     b.String(),
		}
	}
	var left, right []diff.Document
	if oldData != nil {
		left = append(left, body(oldData))
	}
	if newData != nil {
		right = append(right, body(newData))
	}
	results, err := diff.Run(left, right, diff.Options{Unified: 3})
	if err != nil {
		t.Fatalf("diff.Run() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("diff.Run() returned %d results, want 1", len(results))
	}
	return results[0]
}

func TestDiffMarkdownDyffStyleEmbedsGitHubReport(t *testing.T) {
	result := app.DiffResult{Results: []diff.Result{
		configMapDiff(t, "demo", "cm", map[string]string{"image": "registry.example/app:v1"}, map[string]string{"image": "registry.example/app:v2"}),
	}}

	out, _, err := DiffMarkdown(result, MarkdownOptions{MaxBytes: DefaultMaxBytes, DiffStyle: DiffStyleDyff})
	if err != nil {
		t.Fatalf("DiffMarkdown() error = %v", err)
	}

	want := "```diff\n" +
		"@@ data.image @@\n" +
		"# v1/ConfigMap/default/cm\n" +
		"! \u00b1 value change\n" +
		"- registry.example/app:v1\n" +
		"+ registry.example/app:v2\n" +
		"```\n"
	text := string(out)
	if !strings.Contains(text, want) {
		t.Fatalf("markdown missing dyff fence %q:\n%s", want, text)
	}
	// Summary counts still come from the unified diff.
	if !strings.Contains(text, "**Summary:** 1 app, 1 resource, +1/-1.") {
		t.Fatalf("markdown summary changed with dyff style:\n%s", text)
	}
	if strings.Contains(text, "\x1b[") {
		t.Fatalf("markdown contains ANSI escapes:\n%q", text)
	}
}

func TestDiffMarkdownDefaultStyleStaysUnified(t *testing.T) {
	result := app.DiffResult{Results: []diff.Result{
		configMapDiff(t, "demo", "cm", map[string]string{"k": "old"}, map[string]string{"k": "new"}),
	}}

	for _, style := range []DiffStyle{"", DiffStyleUnified} {
		out, _, err := DiffMarkdown(result, MarkdownOptions{MaxBytes: DefaultMaxBytes, DiffStyle: style})
		if err != nil {
			t.Fatalf("DiffMarkdown(%q) error = %v", style, err)
		}
		if text := string(out); !strings.Contains(text, "+  k: new") || strings.Contains(text, "! \u00b1") {
			t.Fatalf("DiffMarkdown(%q) is not the unified diff:\n%s", style, text)
		}
	}
}

func TestDiffMarkdownDyffStyleWidensFenceForBackticks(t *testing.T) {
	result := app.DiffResult{Results: []diff.Result{
		configMapDiff(t, "demo", "cm", map[string]string{"k": "\"``` old\""}, map[string]string{"k": "\"``` new\""}),
	}}

	out, _, err := DiffMarkdown(result, MarkdownOptions{MaxBytes: DefaultMaxBytes, DiffStyle: DiffStyleDyff})
	if err != nil {
		t.Fatalf("DiffMarkdown() error = %v", err)
	}

	if !strings.Contains(string(out), "````diff\n") {
		t.Fatalf("markdown fence not widened for backticks:\n%s", out)
	}
}

func TestDiffMarkdownDyffStyleTruncatesWithCommentMarker(t *testing.T) {
	oldData := map[string]string{}
	newData := map[string]string{}
	for i := range 200 {
		key := fmt.Sprintf("key%03d", i)
		oldData[key] = "old-" + strings.Repeat("x", 40)
		newData[key] = "new-" + strings.Repeat("y", 40)
	}
	result := app.DiffResult{Results: []diff.Result{configMapDiff(t, "demo", "cm", oldData, newData)}}

	out, meta, err := DiffMarkdown(result, MarkdownOptions{MaxBytes: MinPositiveMaxByte, DiffStyle: DiffStyleDyff})
	if err != nil {
		t.Fatalf("DiffMarkdown() error = %v", err)
	}

	text := string(out)
	if !meta.Truncated || len(out) > MinPositiveMaxByte {
		t.Fatalf("meta = %#v, bytes = %d, want truncated output within budget", meta, len(out))
	}
	if strings.Contains(text, "@@ drydock skipped") {
		t.Fatalf("dyff style used the unified truncation marker:\n%s", text)
	}
	if meta.ShownApps != 1 || !strings.Contains(text, "# drydock skipped remaining diff lines") {
		t.Fatalf("meta = %#v, want the app shown with a comment truncation marker:\n%s", meta, text)
	}
}

func TestDiffMarkdownRejectsUnknownDiffStyle(t *testing.T) {
	_, _, err := DiffMarkdown(app.DiffResult{}, MarkdownOptions{MaxBytes: DefaultMaxBytes, DiffStyle: "fancy"})
	if err == nil || !strings.Contains(err.Error(), "markdown diff style must be unified or dyff") {
		t.Fatalf("DiffMarkdown() error = %v, want unknown style error", err)
	}
}

func TestParseDiffStyle(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  DiffStyle
	}{
		{"", DiffStyleUnified},
		{"unified", DiffStyleUnified},
		{" DYFF ", DiffStyleDyff},
	} {
		got, err := ParseDiffStyle(tc.value)
		if err != nil || got != tc.want {
			t.Errorf("ParseDiffStyle(%q) = %q, %v; want %q", tc.value, got, err, tc.want)
		}
	}
	if _, err := ParseDiffStyle("github"); err == nil {
		t.Error("ParseDiffStyle(github) error = nil, want error")
	}
}

func TestDiffMarkdownDyffStyleShowsGeneratedRenameAsOneChange(t *testing.T) {
	data := func(tail string) map[string]string {
		return map[string]string{"A": "a", "B": "b", "C": "c", "D": "d", "TAIL": tail}
	}
	oldCM := configMapDiff(t, "demo", "common-env-745mg7f9mk", data("old"), nil)
	newCM := configMapDiff(t, "demo", "common-env-8f59fckt76", nil, data("new"))
	result := app.DiffResult{Results: []diff.Result{oldCM, newCM}}

	out, _, err := DiffMarkdown(result, MarkdownOptions{MaxBytes: DefaultMaxBytes, DiffStyle: DiffStyleDyff})
	if err != nil {
		t.Fatalf("DiffMarkdown() error = %v", err)
	}

	text := string(out)
	for _, want := range []string{"@@ data.TAIL @@", "- old", "+ new", "@@ metadata.name @@"} {
		if !strings.Contains(text, want) {
			t.Errorf("markdown missing %q:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{"document removed", "document added", "A: a"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("markdown contains %q:\n%s", unwanted, text)
		}
	}
}

func TestDiffMarkdownDyffStylePairsRenamesPerApplicationInOrder(t *testing.T) {
	data := func(tail string) map[string]string {
		return map[string]string{"A": "a", "B": "b", "C": "c", "D": "d", "TAIL": tail}
	}
	result := app.DiffResult{Results: []diff.Result{
		configMapDiff(t, "demo", "common-env-745mg7f9mk", data("old"), nil),
		configMapDiff(t, "demo", "common-env-8f59fckt76", nil, data("new")),
		configMapDiff(t, "demo", "zz-plain", map[string]string{"k": "1"}, map[string]string{"k": "2"}),
		// Same generated names in another Application must not pair with demo.
		configMapDiff(t, "other", "common-env-745mg7f9mk", data("old"), nil),
	}}

	out, _, err := DiffMarkdown(result, MarkdownOptions{MaxBytes: DefaultMaxBytes, DiffStyle: DiffStyleDyff})
	if err != nil {
		t.Fatalf("DiffMarkdown() error = %v", err)
	}

	text := string(out)
	rename := strings.Index(text, "@@ data.TAIL @@")
	plain := strings.Index(text, "# v1/ConfigMap/default/zz-plain")
	if rename < 0 || plain < 0 || rename > plain {
		t.Errorf("paired rename should come before zz-plain (rename=%d, plain=%d):\n%s", rename, plain, text)
	}
	if strings.Count(text, "one document removed") != 1 {
		t.Errorf("only the unpaired Application should report a removed document:\n%s", text)
	}
}
