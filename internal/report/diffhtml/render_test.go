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
		`<body data-view="side-by-side" data-default-resource="resource-0">`,
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
		`title="ConfigMap cm-one"`,
		`aria-label="ConfigMap cm-one, plus 1, minus 1"`,
		`<span class="tree-resource-label">ConfigMap · cm-one</span>`,
		`<span class="tree-delta" aria-hidden="true"><span class="tree-delta-added">+1</span><span class="tree-delta-removed">-1</span></span>`,
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

func TestRenderTreeLabelsPreferKindAndName(t *testing.T) {
	out, err := Render(app.DiffResult{
		Results: []diff.Result{
			resourceChange("demo", "apps", "Deployment", "team-a", "api", diff.ChangeModified, diffWithChangedLines("-old", "+new")),
			resourceChange("demo", "apps", "Deployment", "team-b", "api", diff.ChangeModified, diffWithChangedLines("-old", "+new")),
			resourceChange("demo", "", "Service", "team-a", "api-service", diff.ChangeModified, diffWithChangedLines("-old", "+new")),
		},
	}, Options{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	text := string(out)
	for _, want := range []string{
		`<span class="tree-resource-label">Deployment · team-a/api</span>`,
		`<span class="tree-resource-label">Deployment · team-b/api</span>`,
		`<span class="tree-resource-label">Service · api-service</span>`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("HTML missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, `<span class="tree-resource-label">Service · team-a/api-service</span>`) {
		t.Fatalf("HTML rendered namespace for an unambiguous tree label:\n%s", text)
	}
}

func TestRenderPlacesToolbarInResourceHeader(t *testing.T) {
	out, err := Render(app.DiffResult{
		Results: []diff.Result{diffResult("argocd", "demo", "cm-one", "@@ -1,1 +1,1 @@\n-old\n+new\n")},
	}, Options{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	text := string(out)
	articleIndex := strings.Index(text, `<article class="resource"`)
	headerIndex := strings.Index(text, `<header class="resource-header">`)
	titleIndex := strings.Index(text, `<div class="resource-title">`)
	toolbarIndex := strings.Index(text, `<div class="toolbar" role="toolbar" aria-label="Diff view">`)
	tableIndex := strings.Index(text, `<table class="diff-table side-by-side">`)
	for label, index := range map[string]int{
		"article":   articleIndex,
		"header":    headerIndex,
		"title":     titleIndex,
		"toolbar":   toolbarIndex,
		"diffTable": tableIndex,
	} {
		if index == -1 {
			t.Fatalf("HTML missing %s:\n%s", label, text)
		}
	}
	if articleIndex >= headerIndex || headerIndex >= titleIndex || titleIndex >= toolbarIndex || toolbarIndex >= tableIndex {
		t.Fatalf("resource header should place title and toolbar before the diff table:\n%s", text)
	}
}

func TestRenderIncludesTypographyAndLogoContract(t *testing.T) {
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
	}, Options{Title: "Full Rendered Diff View"})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	text := string(out)
	assertStyleRuleContains(t, text, ":root",
		`font-family: Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;`,
	)
	assertStyleRuleContains(t, text, "body", `font-size: 14px;`, `line-height: 1.45;`)
	assertStyleRuleContains(t, text, ".report-header h1", `font-size: 20px;`)
	assertStyleRuleContains(t, text, ".brand-logo", `width: clamp(186px, 18vw, 210px);`)
	assertStyleRuleContains(t, text, ".summary, .resource-meta", `font-size: 14px;`)
	assertStyleRuleContains(t, text, ".tree h2", `font-size: 14px;`)
	assertStyleRuleContains(t, text, ".tree-resource", `font-size: 14px;`)
	assertStyleRuleContains(t, text, ".tree-resource",
		`display: grid;`,
		`grid-template-columns: minmax(0, 1fr) auto;`,
	)
	assertStyleRuleContains(t, text, ".tree-resource-label",
		`overflow: hidden;`,
		`text-overflow: ellipsis;`,
		`white-space: nowrap;`,
	)
	assertStyleRuleContains(t, text, ".tree-delta",
		`font-size: 12px;`,
		`font-variant-numeric: tabular-nums;`,
		`white-space: nowrap;`,
	)
	assertStyleRuleContains(t, text, ".resource-header",
		`display: grid;`,
		`grid-template-columns: minmax(0, 1fr) auto;`,
		`align-items: start;`,
	)
	assertStyleRuleContains(t, text, ".toolbar",
		`justify-content: flex-end;`,
		`white-space: nowrap;`,
	)
	assertStyleRuleContains(t, text, ".toolbar button", `padding: 5px 9px;`, `font-size: 14px;`)
	assertStyleRuleContains(t, text, ".resource h3", `font-size: 18px;`, `line-height: 1.25;`)
	assertStyleRuleContains(t, text, ".diff-table", `margin-top: 0;`, `font-size: 13px;`)

	if want := `font-family="Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif"`; !strings.Contains(text, want) {
		t.Fatalf("HTML missing SVG typography contract %q:\n%s", want, text)
	}
	for _, old := range []string{
		`width: clamp(132px, 16vw, 180px);`,
		`width: 132px;`,
		`font-family="'Inter', 'Helvetica Neue', Arial, sans-serif"`,
	} {
		if strings.Contains(text, old) {
			t.Fatalf("HTML contains stale typography contract %q:\n%s", old, text)
		}
	}
}

func TestRenderDefaultResourceSelectorWins(t *testing.T) {
	out, err := Render(app.DiffResult{
		Results: []diff.Result{
			resourceChange("demo", "apps", "Deployment", "default", "api", diff.ChangeModified, diffWithChangedLines("-replicas: 2", "+replicas: 3")),
			resourceChange("demo", "", "ConfigMap", "default", "runtime", diff.ChangeModified, diffWithChangedLines("-mode: old", "+mode: new")),
		},
	}, Options{
		DefaultResource: DefaultResourceSelector{
			ParentNamespace: "argocd",
			ParentName:      "demo",
			Group:           "",
			Kind:            "ConfigMap",
			Namespace:       "default",
			Name:            "runtime",
		},
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	text := string(out)
	if got := defaultResourceFromBody(t, text); got != "resource-1" {
		t.Fatalf("default resource = %q, want resource-1\n%s", got, text)
	}
}

func TestRenderDefaultResourceSelectorUsesFirstRenderedMatch(t *testing.T) {
	out, err := Render(app.DiffResult{
		Results: []diff.Result{
			resourceChange("zeta", "", "ConfigMap", "default", "later", diff.ChangeModified, diffWithChangedLines("-a", "+b")),
			resourceChange("alpha", "", "ConfigMap", "default", "earlier", diff.ChangeModified, diffWithChangedLines("-a", "+b")),
		},
	}, Options{
		DefaultResource: DefaultResourceSelector{Kind: "ConfigMap"},
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	text := string(out)
	if got := defaultResourceFromBody(t, text); got != "resource-1" {
		t.Fatalf("default resource = %q, want first rendered selector match resource-1\n%s", got, text)
	}
}

func TestRenderDefaultResourceSelectorMissFallsBack(t *testing.T) {
	out, err := Render(app.DiffResult{
		Results: []diff.Result{
			resourceChange("demo", "", "Widget", "default", "misc", diff.ChangeModified, diffWithChangedLines("-a", "+b")),
			resourceChange("demo", "apps", "Deployment", "default", "api", diff.ChangeModified, diffWithChangedLines("-replicas: 2", "+replicas: 3")),
		},
	}, Options{
		DefaultResource: DefaultResourceSelector{Name: "missing"},
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	text := string(out)
	if got := defaultResourceFromBody(t, text); got != "resource-1" {
		t.Fatalf("default resource = %q, want heuristic fallback resource-1\n%s", got, text)
	}
}

func TestRenderDefaultResourceHeuristic(t *testing.T) {
	tests := []struct {
		name    string
		results []diff.Result
		want    string
	}{
		{
			name: "workload controller",
			results: []diff.Result{
				resourceChange("demo", "", "Service", "default", "svc", diff.ChangeModified, diffWithChangedLines("-port: 80", "+port: 81")),
				resourceChange("demo", "apps", "Deployment", "default", "api", diff.ChangeModified, diffWithChangedLines("-replicas: 2", "+replicas: 3")),
			},
			want: "resource-1",
		},
		{
			name: "rollout impact signal breaks category tie",
			results: []diff.Result{
				resourceChange("demo", "", "ConfigMap", "default", "plain", diff.ChangeModified, diffWithChangedLines("-mode: old", "+mode: new", "-feature: old", "+feature: new")),
				resourceChange("demo", "", "Secret", "default", "rollout", diff.ChangeModified, diffWithChangedLines("-image: app:v1", "+image: app:v2")),
			},
			want: "resource-1",
		},
		{
			name: "category outranks rollout impact signal",
			results: []diff.Result{
				resourceChange("demo", "example.io", "Widget", "default", "rollout", diff.ChangeModified, diffWithChangedLines("-image: app:v1", "+image: app:v2")),
				resourceChange("demo", "", "Service", "default", "svc", diff.ChangeModified, diffWithChangedLines("-port: 80", "+port: 81")),
			},
			want: "resource-1",
		},
		{
			name: "traffic exposure",
			results: []diff.Result{
				resourceChange("demo", "autoscaling", "HorizontalPodAutoscaler", "default", "api", diff.ChangeModified, diffWithChangedLines("-maxReplicas: 4", "+maxReplicas: 8")),
				resourceChange("demo", "", "Service", "default", "api", diff.ChangeModified, diffWithChangedLines("-type: ClusterIP", "+type: LoadBalancer")),
			},
			want: "resource-1",
		},
		{
			name: "autoscaling policy",
			results: []diff.Result{
				resourceChange("demo", "", "ConfigMap", "default", "settings", diff.ChangeModified, diffWithChangedLines("-mode: old", "+mode: new")),
				resourceChange("demo", "autoscaling", "HorizontalPodAutoscaler", "default", "api", diff.ChangeModified, diffWithChangedLines("-maxReplicas: 4", "+maxReplicas: 8")),
			},
			want: "resource-1",
		},
		{
			name: "config",
			results: []diff.Result{
				resourceChange("demo", "example.io", "Widget", "default", "misc", diff.ChangeModified, diffWithChangedLines("-value: old", "+value: new")),
				resourceChange("demo", "", "Secret", "default", "settings", diff.ChangeModified, diffWithChangedLines("-token: old", "+token: new")),
			},
			want: "resource-1",
		},
		{
			name: "unknown modified beats added and removed",
			results: []diff.Result{
				resourceChange("demo", "apps", "Deployment", "default", "api", diff.ChangeAdded, diffWithChangedLines("+replicas: 3", "+image: app:v2")),
				resourceChange("demo", "", "Service", "default", "api", diff.ChangeRemoved, diffWithChangedLines("-type: LoadBalancer", "-port: 80")),
				resourceChange("demo", "example.io", "Widget", "default", "misc", diff.ChangeModified, diffWithChangedLines("-value: old", "+value: new")),
			},
			want: "resource-2",
		},
		{
			name: "added beats removed",
			results: []diff.Result{
				resourceChange("demo", "apps", "Deployment", "default", "old", diff.ChangeRemoved, diffWithChangedLines("-a", "-b", "-c")),
				resourceChange("demo", "example.io", "Widget", "default", "new", diff.ChangeAdded, diffWithChangedLines("+a")),
			},
			want: "resource-1",
		},
		{
			name: "changed line count tie breaker",
			results: []diff.Result{
				resourceChange("demo", "", "ConfigMap", "default", "small", diff.ChangeModified, diffWithChangedLines("-a", "+b")),
				resourceChange("demo", "", "Secret", "default", "large", diff.ChangeModified, diffWithChangedLines("-a", "+b", "-c", "+d")),
			},
			want: "resource-1",
		},
		{
			name: "stable rendered order tie breaker",
			results: []diff.Result{
				resourceChange("zeta", "", "ConfigMap", "default", "later", diff.ChangeModified, diffWithChangedLines("-a", "+b")),
				resourceChange("alpha", "", "ConfigMap", "default", "earlier", diff.ChangeModified, diffWithChangedLines("-a", "+b")),
			},
			want: "resource-1",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			out, err := Render(app.DiffResult{Results: test.results}, Options{})
			if err != nil {
				t.Fatalf("Render() error = %v", err)
			}
			text := string(out)
			if got := defaultResourceFromBody(t, text); got != test.want {
				t.Fatalf("default resource = %q, want %s\n%s", got, test.want, text)
			}
		})
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

func TestRenderDefaultResourceScriptContract(t *testing.T) {
	out, err := Render(app.DiffResult{
		Results: []diff.Result{diffResult("argocd", "demo", "cm-one", "@@ -1,1 +1,1 @@\n-old\n+new\n")},
	}, Options{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	text := string(out)
	for _, want := range []string{
		`const resourceIds = new Set(resources.map((resource) => resource.dataset.resourceId));`,
		`const resourceIdFromHash = () => {`,
		`const id = window.location.hash.slice(1);`,
		`return resourceIds.has(id) ? id : '';`,
		`const defaultResourceId = () => {`,
		`resourceIds.has(body.dataset.defaultResource)`,
		`return body.dataset.defaultResource;`,
		`selectResource(resourceIdFromHash() || defaultResourceId());`,
		`activeButton.scrollIntoView({ block: 'nearest' });`,
		`selectResource(button.dataset.targetResource, { updateHash: true })`,
		"history.replaceState(null, '', `#${id}`);",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("HTML missing default resource JS contract %q:\n%s", want, text)
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

func TestRenderOneSidedTablesForAddedAndRemovedResources(t *testing.T) {
	tests := []struct {
		name   string
		change diff.Change
		body   string
		want   string
	}{
		{
			name:   "removed",
			change: diff.ChangeRemoved,
			body:   "--- old\n+++ new\n@@ -1,2 +1 @@\n-alpha\n-beta\n \n",
			want:   `<tr class="diff-row removed"><td class="line-number">1</td><td class="line-code">alpha</td></tr>`,
		},
		{
			name:   "added",
			change: diff.ChangeAdded,
			body:   "--- old\n+++ new\n@@ -0,0 +1,2 @@\n+alpha\n+beta\n",
			want:   `<tr class="diff-row added"><td class="line-number">1</td><td class="line-code">alpha</td></tr>`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			out, err := Render(app.DiffResult{
				Results: []diff.Result{{
					Parent: diff.Parent{Name: "demo"},
					Resource: diff.Resource{
						Kind: "ConfigMap",
						Name: "cm-one",
					},
					Change: test.change,
					Diff:   test.body,
				}},
			}, Options{})
			if err != nil {
				t.Fatalf("Render() error = %v", err)
			}
			text := string(out)
			for _, want := range []string{
				`<table class="diff-table side-by-side one-sided">`,
				`<table class="diff-table unified one-sided">`,
				test.want,
			} {
				if !strings.Contains(text, want) {
					t.Fatalf("HTML missing %q:\n%s", want, text)
				}
			}
			for _, forbidden := range []string{
				`<th colspan="4">`,
				`<tr class="diff-row context">`,
				`<td class="line-number"></td>`,
			} {
				if strings.Contains(text, forbidden) {
					t.Fatalf("HTML contains %q in one-sided %s diff:\n%s", forbidden, test.name, text)
				}
			}
		})
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
	if strings.Contains(text, `data-default-resource=`) {
		t.Fatalf("empty HTML should not include a default resource:\n%s", text)
	}
}

func defaultResourceFromBody(t *testing.T, text string) string {
	t.Helper()
	prefix := `data-default-resource="`
	start := strings.Index(text, prefix)
	if start == -1 {
		t.Fatalf("HTML missing body default resource:\n%s", text)
	}
	remaining := text[start+len(prefix):]
	end := strings.Index(remaining, `"`)
	if end == -1 {
		t.Fatalf("HTML has unterminated body default resource:\n%s", text)
	}
	return remaining[:end]
}

func assertStyleRuleContains(t *testing.T, text, selector string, declarations ...string) {
	t.Helper()
	prefix := selector + " {\n"
	start := strings.Index(text, prefix)
	if start == -1 {
		t.Fatalf("HTML missing style rule %q:\n%s", selector, text)
	}
	bodyStart := start + len(prefix)
	end := strings.Index(text[bodyStart:], "\n}")
	if end == -1 {
		t.Fatalf("HTML has unterminated style rule %q:\n%s", selector, text[start:])
	}
	rule := text[bodyStart : bodyStart+end]
	for _, declaration := range declarations {
		if !strings.Contains(rule, declaration) {
			t.Fatalf("style rule %q missing %q:\n%s", selector, declaration, rule)
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

func resourceChange(parentName, group, kind, namespace, name string, change diff.Change, diffText string) diff.Result {
	return diff.Result{
		Parent: diff.Parent{
			Namespace: "argocd",
			Name:      parentName,
		},
		Resource: diff.Resource{
			Group:     group,
			Kind:      kind,
			Namespace: namespace,
			Name:      name,
		},
		Change: change,
		Diff:   diffText,
	}
}

func diffWithChangedLines(lines ...string) string {
	return "--- old\n+++ new\n@@ -1,1 +1,1 @@\n" + strings.Join(lines, "\n") + "\n"
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
