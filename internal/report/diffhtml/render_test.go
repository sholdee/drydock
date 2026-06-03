package diffhtml

import (
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/app"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/diff"
)

func TestRenderEscapesDynamicContent(t *testing.T) {
	out, err := Render(app.DiffResult{
		Results: []diff.Result{{
			Parent: diff.Parent{
				Namespace: "argocd<script>",
				Name:      "demo&app",
			},
			Resource: diff.Resource{
				Kind: "ConfigMap",
				Name: "cm<one>",
			},
			Change: diff.ChangeModified,
			Diff:   "--- <old>\n+++ <new>\n@@ -1,1 +1,1 @@\n-<old>\n+<new>\n",
		}},
		Diagnostics: []diagnostic.Diagnostic{{
			Severity: diagnostic.SeverityWarning,
			Category: "render<script>",
			Message:  "message <tag>",
		}},
	}, Options{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	text := string(out)
	for _, raw := range []string{
		"argocd<script>/demo&app",
		"render<script>",
		"cm<one>",
		"<old>",
		"<new>",
		"<tag>",
	} {
		if strings.Contains(text, raw) {
			t.Fatalf("HTML contains unescaped dynamic value %q:\n%s", raw, text)
		}
	}
	for _, escaped := range []string{
		"argocd&lt;script&gt;/demo&amp;app",
		"render&lt;script&gt;",
		"cm&lt;one&gt;",
		`&lt;<span class="inline-change removed">old</span>&gt;`,
		`&lt;<span class="inline-change added">new</span>&gt;`,
		"&lt;tag&gt;",
	} {
		if !strings.Contains(text, escaped) {
			t.Fatalf("HTML missing escaped dynamic value %q:\n%s", escaped, text)
		}
	}
}

func TestRenderGroupsAndSummarizesChanges(t *testing.T) {
	out, err := Render(app.DiffResult{
		Results: []diff.Result{
			diffResult("argocd", "small", "cm-one", "-old\n+new\n"),
			diffResult("argocd", "large", "cm-two", "-old\n+new\n-old2\n+new2\n"),
			diffResult("argocd", "small", "cm-three", " context\n"),
		},
	}, Options{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	text := string(out)
	for _, want := range []string{
		"<title>drydock desired state diff</title>",
		"2 apps",
		"3 resources",
		"+3/-3",
		"argocd/large",
		"argocd/small",
		"cm-one",
		"cm-two",
		"cm-three",
		`data-change="modified"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("HTML missing %q:\n%s", want, text)
		}
	}
	if strings.Index(text, "argocd/large") > strings.Index(text, "argocd/small") {
		t.Fatalf("HTML did not sort app groups by changed lines descending:\n%s", text)
	}
}

func TestRenderIncludesStaticReviewUI(t *testing.T) {
	out, err := Render(app.DiffResult{
		Results: []diff.Result{{
			Parent: diff.Parent{
				Namespace: "argocd",
				Name:      "demo",
			},
			Resource: diff.Resource{
				Kind: "ConfigMap",
				Name: "cm-one",
			},
			Change: diff.ChangeModified,
			Diff:   "--- old\n+++ new\n@@ -1,1 +1,1 @@\n-old\n+new\n",
		}},
	}, Options{Title: "Review"})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	text := string(out)
	for _, want := range []string{
		"<style>",
		`<body data-view="side-by-side">`,
		"<header",
		`class="brand-logo"`,
		`class="tree"`,
		`class="tree-search"`,
		`data-tree-search`,
		`placeholder="Search changes"`,
		`aria-label="Search changed resources"`,
		`data-tree-app`,
		`data-target-resource="resource-0"`,
		`data-search-text="argocd/demo configmap cm-one modified"`,
		`class="toolbar"`,
		`data-view="side-by-side"`,
		`data-view="unified"`,
		`data-resource-id="resource-0"`,
		`class="diff-table side-by-side"`,
		`class="diff-table unified"`,
		`class="line-number"`,
		`class="line-code"`,
		`class="inline-change added"`,
		`[data-resource-id]`,
		`[data-tree-search]`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("HTML missing %q:\n%s", want, text)
		}
	}
}

func TestRenderSearchKeyboardContract(t *testing.T) {
	out, err := Render(app.DiffResult{
		Results: []diff.Result{diffResult("argocd", "demo", "cm-one", "@@ -1,1 +1,1 @@\n-old\n+new\n")},
	}, Options{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	text := string(out)
	for _, want := range []string{
		`const isEditable = (target) => {`,
		`const clearOrBlurSearch = () => {`,
		`document.addEventListener('keydown', (event) => {`,
		`event.key === '/' && !isEditable(event.target)`,
		`searchInput.focus();`,
		`searchInput.select();`,
		`if (searchInput.value) {`,
		`searchInput.blur();`,
		`event.stopPropagation();`,
		`event.key === 'Escape' && (document.activeElement === searchInput || searchInput.value)`,
		`clearOrBlurSearch();`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("HTML missing search keyboard contract %q:\n%s", want, text)
		}
	}
}

func TestRenderUsesDarkDocsThemeAndInlineLogo(t *testing.T) {
	out, err := Render(app.DiffResult{}, Options{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	text := string(out)
	for _, want := range []string{
		`color-scheme: dark;`,
		`--paper: #07111d;`,
		`--surface: #101b29;`,
		`--teal: #69d7c2;`,
		`--rust-bright: #f08a51;`,
		`.resource h3 {`,
		`aria-label="drydock"`,
		`viewBox="0 0 480 128"`,
		`>drydock</text>`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("HTML missing %q:\n%s", want, text)
		}
	}
}

func TestRenderIncludesInlineFavicon(t *testing.T) {
	out, err := Render(app.DiffResult{}, Options{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	text := string(out)
	for _, want := range []string{
		`<link rel="icon" type="image/svg+xml" href="data:image/svg+xml;base64,`,
		`PHN2ZyB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciIHZpZXdCb3g9IjAgMCAxMjggMTI4IiB3aWR0aD0iMTI4IiBoZWlnaHQ9IjEyOCI+`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("HTML missing favicon marker %q:\n%s", want, text)
		}
	}
}

func TestRenderDiagnosticsAtBottomCollapsedByDefault(t *testing.T) {
	out, err := Render(app.DiffResult{
		Results: []diff.Result{diffResult("argocd", "demo", "cm-one", "@@ -1,1 +1,1 @@\n-old\n+new\n")},
		Diagnostics: []diagnostic.Diagnostic{{
			Severity: diagnostic.SeverityWarning,
			Category: "changed-only",
			Message:  "rendering all Applications",
		}},
	}, Options{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	text := string(out)
	diffIndex := strings.Index(text, `class="applications"`)
	diagnosticsIndex := strings.Index(text, `<details class="diagnostics">`)
	if diffIndex == -1 || diagnosticsIndex == -1 {
		t.Fatalf("HTML missing applications or diagnostics section:\n%s", text)
	}
	if diagnosticsIndex < diffIndex {
		t.Fatalf("diagnostics should render after diff content:\n%s", text)
	}
	if strings.Contains(text, `<details class="diagnostics" open>`) {
		t.Fatalf("diagnostics should be collapsed by default:\n%s", text)
	}
	for _, want := range []string{
		`<summary>Diagnostics: 1 warning</summary>`,
		`<span class="severity">warning</span>`,
		`<span class="category">changed-only</span>`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("HTML missing %q:\n%s", want, text)
		}
	}
}

func TestRenderDiffViewportOnlyContainsResourceArticles(t *testing.T) {
	out, err := Render(app.DiffResult{
		Results: []diff.Result{
			diffResult("argocd", "one", "cm-one", "@@ -1,1 +1,1 @@\n-old\n+new\n"),
			diffResult("argocd", "two", "cm-two", "@@ -1,1 +1,1 @@\n-left\n+right\n"),
		},
	}, Options{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	text := string(out)
	if strings.Contains(text, `class="application"`) || strings.Contains(text, `class="app-summary"`) {
		t.Fatalf("main diff viewport should not render app wrapper headers:\n%s", text)
	}
	for _, want := range []string{
		`<article class="resource" data-resource-id="resource-0"`,
		`<article class="resource" data-resource-id="resource-1"`,
		`<p class="resource-meta">argocd/one &middot; modified</p>`,
		`<p class="resource-meta">argocd/two &middot; modified</p>`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("HTML missing %q:\n%s", want, text)
		}
	}
}

func TestRenderHighlightsChangedCharactersInsidePairedRows(t *testing.T) {
	out, err := Render(app.DiffResult{
		Results: []diff.Result{{
			Parent:   diff.Parent{Name: "demo"},
			Resource: diff.Resource{Kind: "ConfigMap", Name: "cm-one"},
			Change:   diff.ChangeModified,
			Diff:     "--- old\n+++ new\n@@ -1,1 +1,1 @@\n-image: app:v1\n+image: app:v2\n",
		}},
	}, Options{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	text := string(out)
	for _, want := range []string{
		`image: app:v<span class="inline-change removed">1</span>`,
		`image: app:v<span class="inline-change added">2</span>`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("HTML missing inline highlight %q:\n%s", want, text)
		}
	}
}

func TestRenderTreeTargetsOriginalResourceIndexesAfterGrouping(t *testing.T) {
	out, err := Render(app.DiffResult{
		Results: []diff.Result{
			diffResult("argocd", "small", "cm-small", "@@ -1,1 +1,1 @@\n-old\n+new\n"),
			diffResult("argocd", "large", "cm-large", "@@ -1,2 +1,2 @@\n-old\n+new\n-old2\n+new2\n"),
		},
	}, Options{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	text := string(out)
	largeIndex := strings.Index(text, `data-tree-app="argocd/large"`)
	smallIndex := strings.Index(text, `data-tree-app="argocd/small"`)
	if largeIndex == -1 || smallIndex == -1 {
		t.Fatalf("HTML missing expected app groups:\n%s", text)
	}
	if largeIndex > smallIndex {
		t.Fatalf("HTML did not render larger app first:\n%s", text)
	}
	assertContainsAfter(t, text, largeIndex, `data-target-resource="resource-1"`)
	assertContainsAfter(t, text, smallIndex, `data-target-resource="resource-0"`)
	assertCount(t, text, `data-resource-id="resource-0"`, 1)
	assertCount(t, text, `data-resource-id="resource-1"`, 1)
}

func TestRenderSideBySideRowsFromHunks(t *testing.T) {
	out, err := Render(app.DiffResult{
		Results: []diff.Result{{
			Parent: diff.Parent{Name: "demo"},
			Resource: diff.Resource{
				Kind: "ConfigMap",
				Name: "cm-one",
			},
			Change: diff.ChangeModified,
			Diff:   "--- old\n+++ new\n@@ -10,2 +10,2 @@\n context\n-removed\n+added\n",
		}},
	}, Options{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	text := string(out)
	for _, want := range []string{
		`<tr class="hunk-header"><th colspan="4">@@ -10,2 +10,2 @@</th></tr>`,
		`<tr class="hunk-header"><th colspan="2">@@ -10,2 +10,2 @@</th></tr>`,
		`<td class="line-number">10</td><td class="line-code">context</td><td class="line-number">10</td><td class="line-code">context</td>`,
		`<td class="line-number">11</td><td class="line-code"><span class="inline-change removed">remov</span>ed</td><td class="line-number"></td><td class="line-code"></td>`,
		`<td class="line-number"></td><td class="line-code"></td><td class="line-number">11</td><td class="line-code"><span class="inline-change added">add</span>ed</td>`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("HTML missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "--- old") || strings.Contains(text, "+++ new") {
		t.Fatalf("HTML rendered complete unified file headers instead of hunk rows:\n%s", text)
	}
}

func TestRenderEmptyDiff(t *testing.T) {
	out, err := Render(app.DiffResult{}, Options{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	text := string(out)
	for _, want := range []string{
		"0 apps",
		"0 resources",
		"+0/-0",
		"No rendered manifest differences detected.",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("HTML missing %q:\n%s", want, text)
		}
	}
}

func assertContainsAfter(t *testing.T, text string, offset int, want string) {
	t.Helper()
	if !strings.Contains(text[offset:], want) {
		t.Fatalf("HTML missing %q after offset %d:\n%s", want, offset, text)
	}
}

func assertCount(t *testing.T, text, needle string, want int) {
	t.Helper()
	if got := strings.Count(text, needle); got != want {
		t.Fatalf("strings.Count(%q) = %d, want %d\n%s", needle, got, want, text)
	}
}

func diffResult(namespace, appName, resourceName, diffText string) diff.Result {
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
		Diff:   diffText,
	}
}
