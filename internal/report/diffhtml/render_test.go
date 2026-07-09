package diffhtml

import (
	"bytes"
	"encoding/json"
	"strconv"
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
		"<title>drydock diff</title>",
		"2 apps",
		"3 resources",
		`<div class="summary" aria-label="2 apps, 3 resources, 3 changed, +3, -3">`,
		`<span class="summary-badge summary-badge-modified summary-badge-detail">3 changed</span>`,
		`<span class="summary-badge summary-badge-added">+3</span>`,
		`<span class="summary-badge summary-badge-removed">-3</span>`,
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
		`<meta name="viewport" content="width=device-width, initial-scale=1">`,
		`<body data-view="side-by-side" data-sidebar="auto" data-default-resource="resource-0">`,
		"<header",
		`<div class="summary" aria-label="1 app, 1 resource, 1 changed, +1, -1"><span class="summary-badge summary-badge-neutral">1 app</span><span class="summary-badge summary-badge-neutral">1 resource</span><span class="summary-badge summary-badge-modified summary-badge-detail">1 changed</span><span class="summary-badge summary-badge-added">+1</span><span class="summary-badge summary-badge-removed">-1</span></div>`,
		`class="nav-toggle"`,
		`data-sidebar-toggle`,
		`aria-controls="diff-tree"`,
		`class="view-toggle"`,
		`data-view-toggle`,
		`class="brand-logo"`,
		`class="tree" id="diff-tree"`,
		`class="sidebar-resizer"`,
		`data-sidebar-resizer`,
		`role="separator"`,
		`aria-orientation="vertical"`,
		`aria-label="Resize changed resources sidebar"`,
		`<span class="sidebar-resizer-hint" aria-hidden="true">Release to close</span>`,
		`class="tree-search"`,
		`data-tree-search`,
		`placeholder="Search resources (/)"`,
		`aria-label="Search resources"`,
		`<details class="tree-app" data-tree-app="argocd/demo" open>`,
		`<summary><span class="tree-app-name">argocd/demo</span><span class="tree-delta" aria-hidden="true"><span class="tree-delta-added">+1</span><span class="tree-delta-removed">-1</span></span></summary>`,
		`data-target-resource="resource-0"`,
		`data-search-text="argocd/demo configmap cm-one modified"`,
		`title="ConfigMap cm-one"`,
		`aria-label="ConfigMap cm-one, modified, plus 1, minus 1"`,
		`<span class="tree-status-dot tree-status-modified" aria-hidden="true"></span>`,
		`<span class="tree-resource-label">ConfigMap · cm-one</span>`,
		`<span class="tree-delta" aria-hidden="true"><span class="tree-delta-added">+1</span><span class="tree-delta-removed">-1</span></span>`,
		`class="sidebar-backdrop"`,
		`data-resource-id="resource-0"`,
		`class="diff-table side-by-side"`,
		`class="diff-table unified"`,
		`class="line-number"`,
		`class="line-code"`,
		`class="line-number line-number-blank"`,
		`class="line-code line-code-blank"`,
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

func TestRenderTreeStatusDotsReflectResourceChange(t *testing.T) {
	out, err := Render(app.DiffResult{
		Results: []diff.Result{
			resourceChange("demo", "", "ConfigMap", "default", "changed", diff.ChangeModified, diffWithChangedLines("-old", "+new")),
			resourceChange("demo", "", "ConfigMap", "default", "created", diff.ChangeAdded, diffWithChangedLines("+new")),
			resourceChange("demo", "", "ConfigMap", "default", "deleted", diff.ChangeRemoved, diffWithChangedLines("-old")),
		},
	}, Options{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	text := string(out)
	for _, want := range []string{
		`tree-status-dot tree-status-modified`,
		`tree-status-dot tree-status-added`,
		`tree-status-dot tree-status-removed`,
		`<span class="summary-badge summary-badge-modified summary-badge-detail">1 changed</span>`,
		`<span class="summary-badge summary-badge-added summary-badge-detail">1 added</span>`,
		`<span class="summary-badge summary-badge-removed summary-badge-detail">1 deleted</span>`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("HTML missing %q:\n%s", want, text)
		}
	}
}

func TestRenderPlacesGlobalControlsInHeaderAndResourceHeaderBeforeDiff(t *testing.T) {
	out, err := Render(app.DiffResult{
		Results: []diff.Result{diffResult("argocd", "demo", "cm-one", "@@ -1,1 +1,1 @@\n-old\n+new\n")},
	}, Options{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	text := string(out)
	reportHeaderIndex := strings.Index(text, `<header class="report-header">`)
	navIndex := strings.Index(text, `data-sidebar-toggle`)
	viewIndex := strings.Index(text, `data-view-toggle`)
	logoIndex := strings.Index(text, `class="brand-logo"`)
	articleIndex := strings.Index(text, `<article class="resource"`)
	headerIndex := strings.Index(text, `<header class="resource-header">`)
	titleIndex := strings.Index(text, `<div class="resource-title">`)
	tableIndex := strings.Index(text, `<table class="diff-table side-by-side">`)
	for label, index := range map[string]int{
		"reportHeader": reportHeaderIndex,
		"navToggle":    navIndex,
		"viewToggle":   viewIndex,
		"logo":         logoIndex,
		"article":      articleIndex,
		"header":       headerIndex,
		"title":        titleIndex,
		"diffTable":    tableIndex,
	} {
		if index == -1 {
			t.Fatalf("HTML missing %s:\n%s", label, text)
		}
	}
	if reportHeaderIndex >= navIndex || navIndex >= logoIndex || logoIndex >= articleIndex || articleIndex >= viewIndex {
		t.Fatalf("view toggle should be outside the report header and inside the active resource header:\n%s", text)
	}
	if articleIndex >= headerIndex || headerIndex >= titleIndex || titleIndex >= viewIndex || viewIndex >= tableIndex {
		t.Fatalf("resource header should place title and view toggle before the diff table:\n%s", text)
	}
}

func TestRenderResourceHeaderUsesReadableIdentityWithAPIGroupBadge(t *testing.T) {
	out, err := Render(app.DiffResult{
		Results: []diff.Result{
			resourceChange("demo", "apps", "Deployment", "envoy-gateway-system", "envoy-gateway", diff.ChangeModified, diffWithChangedLines("-old", "+new")),
			resourceChange("demo", "", "ConfigMap", "default", "app-config", diff.ChangeModified, diffWithChangedLines("-old", "+new")),
		},
	}, Options{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	text := string(out)
	for _, want := range []string{
		`<h3 class="resource-heading" title="apps Deployment envoy-gateway-system/envoy-gateway" aria-label="apps Deployment envoy-gateway-system/envoy-gateway"><span class="resource-primary"><span class="resource-kind">Deployment</span> <span class="resource-name">envoy-gateway</span></span> <span class="resource-meta"><span class="resource-namespace">envoy-gateway-system</span> <span class="resource-meta-separator" aria-hidden="true">·</span> <span class="resource-api-group">apps</span></span></h3>`,
		`<h3 class="resource-heading" title="ConfigMap default/app-config" aria-label="ConfigMap default/app-config"><span class="resource-primary"><span class="resource-kind">ConfigMap</span> <span class="resource-name">app-config</span></span> <span class="resource-meta"><span class="resource-namespace">default</span></span></h3>`,
		`title="apps Deployment envoy-gateway-system/envoy-gateway"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("HTML missing resource header marker %q:\n%s", want, text)
		}
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
		`--topbar-height: 50px;`,
		`--sidebar-width: 320px;`,
		`--sidebar-width-default: 320px;`,
		`--sidebar-width-min: 240px;`,
		`--sidebar-width-max: 480px;`,
		`--sidebar-resizer-hit: 18px;`,
		`--main-pane-padding-x: 18px;`,
		`--header-padding-x: 14px;`,
		`--header-gap: 12px;`,
		`--header-nav-width: 32px;`,
		`--header-title-gap: 18px;`,
	)
	assertStyleRuleContains(t, text, "html", `-webkit-text-size-adjust: 100%;`, `text-size-adjust: 100%;`)
	assertStyleRuleContains(t, text, "body", `min-height: 100vh;`, `min-height: 100dvh;`, `overflow: hidden;`, `font-size: 14px;`, `line-height: 1.45;`)
	assertStyleRuleContains(t, text, "body.is-resizing-sidebar", `cursor: col-resize;`)
	assertStyleRuleContains(t, text, "body.is-resizing-sidebar *", `user-select: none;`)
	assertStyleRuleContains(t, text, ".report-header",
		`grid-template-columns: var(--header-nav-width) minmax(0, 1fr) auto;`,
		`gap: var(--header-gap);`,
		`box-sizing: border-box;`,
		`height: var(--topbar-height);`,
		`padding: 2px var(--header-padding-x);`,
	)
	assertStyleRuleContains(t, text, ".report-header h1", `min-width: 0;`, `font-size: 17px;`, `line-height: 1.2;`)
	assertStyleRuleContains(t, text, ".header-copy",
		`grid-column: 2;`,
		`display: inline-flex;`,
		`align-items: center;`,
		`gap: var(--header-title-gap);`,
	)
	assertStyleRuleContains(t, text, ".report-header h1", `overflow: hidden;`, `text-overflow: ellipsis;`, `white-space: nowrap;`)
	assertStyleRuleContains(t, text, ".nav-toggle",
		`width: var(--header-nav-width);`,
		`height: 32px;`,
	)
	assertStyleRuleContains(t, text, ".view-toggle",
		`min-width: 96px;`,
		`font-size: 13px;`,
	)
	assertStyleRuleContains(t, text, ".header-actions",
		`grid-column: 3;`,
		`align-items: center;`,
	)
	assertStyleRuleContains(t, text, ".brand-logo", `width: 44px;`, `height: 44px;`, `margin: -3px 0;`)
	assertStyleRuleContains(t, text, ".summary", `font-size: 12px;`)
	assertStyleRuleContains(t, text, ".summary", `display: inline-flex;`, `align-items: center;`, `flex: 0 1 auto;`, `gap: 5px;`, `flex-wrap: wrap;`, `line-height: 1;`)
	assertStyleRuleContains(t, text, ".summary-badge",
		`display: inline-flex;`,
		`align-items: center;`,
		`min-height: 18px;`,
		`padding: 2px 6px;`,
		`font-size: 11px;`,
		`vertical-align: middle;`,
	)
	assertStyleRuleContains(t, text, ".summary-badge-modified", `background: rgba(240, 179, 90, 0.14);`, `color: #ffd596;`)
	assertStyleRuleContains(t, text, ".summary-badge-added", `background: rgba(63, 185, 80, 0.13);`, `color: #9ce8a8;`)
	assertStyleRuleContains(t, text, ".summary-badge-removed", `background: rgba(248, 81, 73, 0.13);`, `color: #ffb4ae;`)
	assertStyleRuleContains(t, text, ".review-layout",
		`grid-template-columns: var(--sidebar-width) 0 minmax(0, 1fr);`,
		`height: calc(100vh - var(--topbar-height));`,
		`height: calc(100dvh - var(--topbar-height));`,
		`min-height: 0;`,
	)
	assertStyleRuleContains(t, text, ".tree", `grid-column: 1;`, `min-height: 0;`, `overflow: auto;`)
	assertStyleRuleContains(t, text, ".sidebar-resizer",
		`grid-column: 2;`,
		`width: var(--sidebar-resizer-hit);`,
		`margin-left: calc(var(--sidebar-resizer-hit) / -2);`,
		`cursor: col-resize;`,
		`touch-action: none;`,
	)
	assertStyleRuleContains(t, text, ".sidebar-resizer::before",
		`left: calc(var(--sidebar-resizer-hit) / 2);`,
		`width: 1px;`,
		`transition: background 120ms ease 180ms, box-shadow 120ms ease 180ms;`,
	)
	if want := ".sidebar-resizer:focus-visible::before,\nbody.is-resizing-sidebar .sidebar-resizer::before {\n\ttransition-delay: 0ms;\n}"; !strings.Contains(text, want) {
		t.Fatalf("HTML missing immediate sidebar-resizer activation contract %q:\n%s", want, text)
	}
	assertStyleRuleContains(t, text, "body.sidebar-will-close .sidebar-resizer::before",
		`background: var(--rust-bright);`,
	)
	assertStyleRuleContains(t, text, ".sidebar-resizer-hint",
		`position: absolute;`,
		`pointer-events: none;`,
		`opacity: 0;`,
	)
	assertStyleRuleContains(t, text, "body.sidebar-will-close .sidebar-resizer-hint",
		`opacity: 1;`,
	)
	assertStyleRuleContains(t, text, ".review-main", `grid-column: 3;`, `min-height: 0;`, `overflow: auto;`)
	assertStyleRuleContains(t, text, ".tree-app summary",
		`display: grid;`,
		`grid-template-columns: auto minmax(0, 1fr) auto;`,
		`font-size: 13px;`,
	)
	assertStyleRuleContains(t, text, ".tree-resource", `font-size: 13px;`)
	assertStyleRuleContains(t, text, ".tree-resource",
		`display: grid;`,
		`grid-template-columns: auto minmax(0, 1fr) auto;`,
	)
	assertStyleRuleContains(t, text, ".tree-status-modified", `background: #f0b35a;`)
	assertStyleRuleContains(t, text, ".tree-status-added", `background: #3fb950;`)
	assertStyleRuleContains(t, text, ".tree-status-removed", `background: #f85149;`)
	assertStyleRuleContains(t, text, ".tree-resource-label",
		`overflow: hidden;`,
		`text-overflow: ellipsis;`,
		`white-space: nowrap;`,
	)
	assertStyleRuleContains(t, text, ".tree-delta",
		`font-size: 11px;`,
		`font-variant-numeric: tabular-nums;`,
		`white-space: nowrap;`,
	)
	assertStyleRuleContains(t, text, ".tree-delta-added",
		`border-radius: 999px;`,
		`background: rgba(63, 185, 80, 0.13);`,
	)
	assertStyleRuleContains(t, text, ".tree-delta-removed",
		`border-radius: 999px;`,
		`background: rgba(248, 81, 73, 0.13);`,
	)
	assertStyleRuleContains(t, text, ".resource-header",
		`display: grid;`,
		`grid-template-columns: minmax(0, 1fr) auto;`,
		`align-items: start;`,
		`margin: 0 0 7px;`,
		`padding: 0 0 5px;`,
		`background: #0a1421;`,
	)
	assertStyleRuleContains(t, text, ".resource-header::before",
		`content: "";`,
		`position: absolute;`,
		`z-index: -1;`,
		`inset: -14px 0 0;`,
		`background: #0a1421;`,
	)
	assertStyleRuleContains(t, text, ".resource h3", `margin: 0;`, `font-size: 16px;`, `line-height: 1.25;`)
	assertStyleRuleContains(t, text, ".resource-heading",
		`display: flex;`,
		`align-items: center;`,
		`gap: 8px;`,
		`flex-wrap: wrap;`,
	)
	assertStyleRuleContains(t, text, ".resource-meta",
		`display: inline-flex;`,
		`font-size: 13px;`,
		`color: var(--quiet);`,
	)
	assertStyleRuleContains(t, text, ".resource-api-group",
		`border-radius: 999px;`,
		`background: rgba(129, 144, 163, 0.13);`,
		`color: var(--muted);`,
	)
	assertStyleRuleContains(t, text, ".diff-table", `margin-top: 0;`, `font-size: 13.5px;`, `line-height: 20px;`)
	blankGutterSelector := strings.Join([]string{
		".line-number-blank,",
		".diff-row.added .line-number-blank,",
		".diff-row.removed .line-number-blank",
	}, "\n")
	assertStyleRuleContains(t, text, blankGutterSelector,
		`background-color: rgba(16, 27, 41, 0.78);`,
		`background-image: none;`,
	)
	blankStripeSelector := strings.Join([]string{
		".line-code-blank,",
		".diff-row.added .line-code-blank,",
		".diff-row.removed .line-code-blank",
	}, "\n")
	assertStyleRuleContains(t, text, blankStripeSelector,
		`background-size: 6.67px 6.67px;`,
		`background-repeat: repeat;`,
		`rgba(129, 144, 163, 0.34) 46%,`,
		`rgba(129, 144, 163, 0.34) 54%,`,
		`linear-gradient(`,
	)
	if strings.Contains(text, `background-attachment: fixed;`) {
		t.Fatalf("blank-cell stripe rule uses fixed attachment, which creates Chrome raster artifacts:\n%s", text)
	}
	if strings.Contains(text, `--blank-stripe-y`) {
		t.Fatalf("blank-cell stripe rule uses row-local offsets, which disconnect at row seams:\n%s", text)
	}
	if strings.Contains(text, `linear-gradient(rgba(16, 27, 41, 0.78), rgba(16, 27, 41, 0.78))`) {
		t.Fatalf("blank-cell stripe rule contains opaque overlay that hides diagonal lines:\n%s", text)
	}
	assertStyleRuleContains(t, text, ".yaml-key", `color: #8ec8ff;`)
	assertStyleRuleContains(t, text, ".yaml-string", `color: #ce9178;`)
	assertStyleRuleContains(t, text, ".yaml-number", `color: #b5cea8;`)
	assertStyleRuleContains(t, text, ".yaml-bool,\n.yaml-null", `color: #7fb4ff;`)
	assertStyleRuleContains(t, text, ".yaml-comment", `color: #7aa36f;`)
	assertStyleRuleContains(t, text, ".yaml-doc", `color: #d7a2ff;`)
	assertStyleRuleContains(t, text, ".yaml-anchor,\n.yaml-alias", `color: #dcdcaa;`)
	assertStyleRuleContains(t, text, ".yaml-tag", `color: #c586c0;`)
	assertStyleRuleContains(t, text, ".yaml-punctuation", `color: #b9c7d8;`)
	for _, want := range []string{
		"body[data-sidebar=\"closed\"] .tree {\n\t\tdisplay: none;",
		"body[data-sidebar=\"closed\"] .sidebar-resizer {\n\t\tdisplay: none;",
		"body[data-sidebar=\"closed\"] .review-main {\n\t\tgrid-column: 1;",
		"body.is-resizing-sidebar .sidebar-resizer::before {\n\tbackground: var(--teal);",
		"body[data-view=\"side-by-side\"] .diff-table.unified,\nbody[data-view=\"unified\"] .diff-table.side-by-side {\n\tdisplay: none;",
		"body[data-view=\"side-by-side\"] .diff-table.unified {\n\t\tdisplay: table;",
		"@media (max-width: 800px) {\n\t:root {\n\t\t--topbar-height: 50px;",
		".report-header {\n\t\tgrid-template-columns: auto minmax(0, 1fr) auto;",
		".summary {\n\t\tdisplay: none;",
		".sidebar-resizer {\n\t\tdisplay: none;",
		".diff-table {\n\t\tfont-size: 14px;\n\t\tline-height: 21px;",
		"@media (max-width: 520px) {\n\t:root {\n\t\t--topbar-height: 46px;",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("HTML missing responsive style contract %q:\n%s", want, text)
		}
	}

	if want := `viewBox="0 0 128 128"`; !strings.Contains(text, want) {
		t.Fatalf("HTML missing icon-only logo contract %q:\n%s", want, text)
	}
	for _, old := range []string{
		`width: clamp(132px, 16vw, 180px);`,
		`width: clamp(186px, 18vw, 210px);`,
		`width: clamp(168px, 16vw, 188px);`,
		`width: 132px;`,
		`viewBox="0 0 480 128"`,
		`font-family="'Inter', 'Helvetica Neue', Arial, sans-serif"`,
		`<text x=`,
		`class="toolbar"`,
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

func TestRenderDefaultResourceHeuristicCRDLosesToModifiedFallback(t *testing.T) {
	out, err := Render(app.DiffResult{
		Results: []diff.Result{
			resourceChange("demo", "apiextensions.k8s.io", "CustomResourceDefinition", "", "widgets.example.io", diff.ChangeModified, diffWithChangedLines("-spec: old", "+spec: new", "-schema: old", "+schema: new")),
			resourceChange("demo", "example.io", "Widget", "default", "runtime", diff.ChangeModified, diffWithChangedLines("-value: old", "+value: new")),
		},
	}, Options{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	text := string(out)
	if got := defaultResourceFromBody(t, text); got != "resource-1" {
		t.Fatalf("default resource = %q, want non-CRD fallback resource-1\n%s", got, text)
	}
}

func TestRenderCRDRemainsRenderedAndSelectableWhenHeuristicDefaultSkipsIt(t *testing.T) {
	out, err := Render(app.DiffResult{
		Results: []diff.Result{
			resourceChange("demo", "apiextensions.k8s.io", "CustomResourceDefinition", "", "widgets.example.io", diff.ChangeModified, diffWithChangedLines("-spec: old", "+spec: new", "-schema: old", "+schema: new")),
			resourceChange("demo", "example.io", "Widget", "default", "runtime", diff.ChangeModified, diffWithChangedLines("-value: old", "+value: new")),
		},
	}, Options{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	text := string(out)
	if got := defaultResourceFromBody(t, text); got != "resource-1" {
		t.Fatalf("default resource = %q, want non-CRD fallback resource-1\n%s", got, text)
	}
	for _, want := range []string{
		`data-target-resource="resource-0"`,
		`<article class="resource" data-resource-id="resource-0"`,
		`<h3 class="resource-heading" title="apiextensions.k8s.io CustomResourceDefinition widgets.example.io" aria-label="apiextensions.k8s.io CustomResourceDefinition widgets.example.io"><span class="resource-primary"><span class="resource-kind">CustomResourceDefinition</span> <span class="resource-name">widgets.example.io</span></span> <span class="resource-meta"><span class="resource-api-group">apiextensions.k8s.io</span></span></h3>`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("HTML missing rendered/selectable CRD marker %q:\n%s", want, text)
		}
	}
}

func TestRenderDefaultResourceHeuristicOversizedModifiedLosesToReadableSameRank(t *testing.T) {
	out, err := Render(app.DiffResult{
		Results: []diff.Result{
			resourceChange("demo", "", "ConfigMap", "default", "oversized", diff.ChangeModified, diffWithRawByteCount(t, defaultResourceRawBytesLimit+1)),
			resourceChange("demo", "", "Secret", "default", "readable", diff.ChangeModified, diffWithChangedLines("-mode: old", "+mode: new")),
		},
	}, Options{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	text := string(out)
	if got := defaultResourceFromBody(t, text); got != "resource-1" {
		t.Fatalf("default resource = %q, want readable same-rank resource-1\n%s", got, text)
	}
}

func TestRenderDefaultResourceSelectorCanPickOversizedCRD(t *testing.T) {
	out, err := Render(app.DiffResult{
		Results: []diff.Result{
			resourceChange("demo", "apps", "Deployment", "default", "api", diff.ChangeModified, diffWithChangedLines("-replicas: 2", "+replicas: 3")),
			resourceChange("demo", "apiextensions.k8s.io", "CustomResourceDefinition", "", "widgets.example.io", diff.ChangeModified, diffWithRawByteCount(t, defaultResourceRawBytesLimit+1)),
		},
	}, Options{
		DefaultResource: DefaultResourceSelector{
			Kind: "CustomResourceDefinition",
			Name: "widgets.example.io",
		},
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	text := string(out)
	if got := defaultResourceFromBody(t, text); got != "resource-1" {
		t.Fatalf("default resource = %q, want explicit oversized CRD resource-1\n%s", got, text)
	}
}

func TestRenderDefaultResourceHeuristicThresholdBoundaries(t *testing.T) {
	tests := []struct {
		name    string
		results []diff.Result
	}{
		{
			name: "raw bytes at 20 KiB are readable and 20 KiB plus one is oversized",
			results: []diff.Result{
				resourceChange("demo", "", "ConfigMap", "default", "over-raw", diff.ChangeModified, diffWithRawByteCount(t, defaultResourceRawBytesLimit+1)),
				resourceChange("demo", "", "Secret", "default", "at-raw", diff.ChangeModified, diffWithRawByteCount(t, defaultResourceRawBytesLimit)),
			},
		},
		{
			name: "400 changed lines are readable and 401 changed lines are oversized",
			results: []diff.Result{
				resourceChange("demo", "", "ConfigMap", "default", "over-lines", diff.ChangeModified, diffWithChangedLineCount(defaultResourceChangedLinesLimit+1)),
				resourceChange("demo", "", "Secret", "default", "at-lines", diff.ChangeModified, diffWithChangedLineCount(defaultResourceChangedLinesLimit)),
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			out, err := Render(app.DiffResult{Results: test.results}, Options{})
			if err != nil {
				t.Fatalf("Render() error = %v", err)
			}
			text := string(out)
			if got := defaultResourceFromBody(t, text); got != "resource-1" {
				t.Fatalf("default resource = %q, want threshold-readable resource-1\n%s", got, text)
			}
		})
	}
}

func TestRenderDefaultResourceHeuristicFallbackPrefersSmallerDiffWhenAllCandidatesNoisyOrOversized(t *testing.T) {
	tests := []struct {
		name    string
		results []diff.Result
	}{
		{
			name: "all oversized",
			results: []diff.Result{
				resourceChange("demo", "", "ConfigMap", "default", "larger", diff.ChangeModified, diffWithRawByteCount(t, defaultResourceRawBytesLimit+2048)),
				resourceChange("demo", "", "Secret", "default", "smaller", diff.ChangeModified, diffWithRawByteCount(t, defaultResourceRawBytesLimit+1)),
			},
		},
		{
			name: "all CRDs",
			results: []diff.Result{
				resourceChange("demo", "apiextensions.k8s.io", "CustomResourceDefinition", "", "large.example.io", diff.ChangeModified, diffWithRawByteCount(t, 10*1024)),
				resourceChange("demo", "apiextensions.k8s.io", "CustomResourceDefinition", "", "small.example.io", diff.ChangeModified, diffWithRawByteCount(t, 2*1024)),
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			out, err := Render(app.DiffResult{Results: test.results}, Options{})
			if err != nil {
				t.Fatalf("Render() error = %v", err)
			}
			text := string(out)
			if got := defaultResourceFromBody(t, text); got != "resource-1" {
				t.Fatalf("default resource = %q, want smaller nonpreferred resource-1\n%s", got, text)
			}
		})
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

func TestMeasureDiffCountsRawBytesChangedLinesAndParsedRows(t *testing.T) {
	result := diff.Result{
		Diff: "--- old\n+++ new\n@@ -10,3 +10,3 @@\n context\n-old\n+new\n",
	}
	got := measureDiff(result)
	want := resourceDiffMetrics{
		rawBytes:     len(result.Diff),
		addedLines:   1,
		removedLines: 1,
		changedLines: 2,
		parsedRows:   3,
	}
	if got != want {
		t.Fatalf("measureDiff() = %+v, want %+v", got, want)
	}
}

func TestLazyResourceThresholdBoundaries(t *testing.T) {
	tests := []struct {
		name     string
		metrics  resourceDiffMetrics
		wantLazy bool
		wantHard bool
	}{
		{
			name:     "raw bytes just below lazy threshold",
			metrics:  resourceDiffMetrics{rawBytes: lazyResourceRawBytesThreshold - 1},
			wantLazy: false,
			wantHard: false,
		},
		{
			name:     "raw bytes at lazy threshold",
			metrics:  resourceDiffMetrics{rawBytes: lazyResourceRawBytesThreshold},
			wantLazy: true,
			wantHard: false,
		},
		{
			name:     "raw bytes at hard guard limit",
			metrics:  resourceDiffMetrics{rawBytes: hardResourceRawBytesLimit},
			wantLazy: true,
			wantHard: false,
		},
		{
			name:     "raw bytes above hard guard limit",
			metrics:  resourceDiffMetrics{rawBytes: hardResourceRawBytesLimit + 1},
			wantLazy: true,
			wantHard: true,
		},
		{
			name:     "parsed rows at lazy threshold",
			metrics:  resourceDiffMetrics{parsedRows: lazyResourceParsedRowsThreshold},
			wantLazy: true,
			wantHard: false,
		},
		{
			name:     "parsed rows at hard guard limit",
			metrics:  resourceDiffMetrics{parsedRows: hardResourceParsedRowsLimit},
			wantLazy: true,
			wantHard: false,
		},
		{
			name:     "parsed rows above hard guard limit",
			metrics:  resourceDiffMetrics{parsedRows: hardResourceParsedRowsLimit + 1},
			wantLazy: true,
			wantHard: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isLazyResource(test.metrics); got != test.wantLazy {
				t.Fatalf("isLazyResource(%+v) = %t, want %t", test.metrics, got, test.wantLazy)
			}
			if got := isHardGuardedResource(test.metrics); got != test.wantHard {
				t.Fatalf("isHardGuardedResource(%+v) = %t, want %t", test.metrics, got, test.wantHard)
			}
		})
	}
}

func TestRenderLargeRenderableResourceEmitsLazyPlaceholderAndPayload(t *testing.T) {
	out, err := Render(app.DiffResult{
		Results: []diff.Result{
			resourceChange("demo", "", "ConfigMap", "", "cm-large", diff.ChangeModified, diffWithLineCounts(1000, 1000)),
		},
	}, Options{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	text := string(out)
	article := resourceArticleByCanonicalTitle(t, text, "ConfigMap cm-large")
	for _, want := range []string{
		`data-lazy-diff="true"`,
		`data-lazy-state="pending"`,
		`<div class="lazy-diff-placeholder" data-lazy-placeholder aria-live="polite">`,
		`Large diff: 2,000 rows, +1,000/-1,000,`,
		`<button class="lazy-render-button" type="button" data-lazy-render aria-label="Render diff for ConfigMap cm-large. Large diff: 2,000 rows, +1,000/-1,000,`,
		`<script type="application/json" data-diff-payload="resource-0">`,
		`"parsedRows":2000`,
	} {
		if !strings.Contains(article, want) {
			t.Fatalf("large resource article missing %q:\n%s", want, article)
		}
	}
	for _, forbidden := range []string{
		`<table class="diff-table`,
		`<tr class="diff-row`,
	} {
		if strings.Contains(article, forbidden) {
			t.Fatalf("large resource article contains pre-rendered diff marker %q:\n%s", forbidden, article)
		}
	}
	for _, want := range []string{
		`data-search-text="argocd/demo configmap cm-large modified large"`,
		`aria-label="ConfigMap cm-large, modified, large, plus 1000, minus 1000"`,
		`<span class="tree-resource-meta"><span class="tree-large-badge">large</span><span class="tree-delta" aria-hidden="true"><span class="tree-delta-added">+1000</span><span class="tree-delta-removed">-1000</span></span></span>`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("large resource sidebar missing %q:\n%s", want, text)
		}
	}
}

func TestRenderLazyPayloadRowsIncludeYAMLSyntaxMetadata(t *testing.T) {
	var diffBuilder strings.Builder
	diffBuilder.WriteString("--- old\n+++ new\n@@ -1,2000 +1,2000 @@\n")
	diffBuilder.WriteString(" replicas: 3\n")
	diffBuilder.WriteString("-enabled: false\n")
	diffBuilder.WriteString("+enabled: true\n")
	for range 1998 {
		diffBuilder.WriteString(" filler: plain\n")
	}
	out, err := Render(app.DiffResult{
		Results: []diff.Result{
			resourceChange("demo", "", "ConfigMap", "", "cm-large-yaml", diff.ChangeModified, diffBuilder.String()),
		},
	}, Options{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}

	payload := payloadScriptData(t, string(out), "resource-0")
	var decoded lazyDiffPayload
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatalf("lazy payload is not valid JSON: %v\n%s", err, payload)
	}
	if len(decoded.Hunks) != 1 || len(decoded.Hunks[0].Rows) < 3 {
		t.Fatalf("decoded lazy payload missing expected rows: %+v", decoded)
	}

	contextRow := decoded.Hunks[0].Rows[0]
	if contextRow.LeftText != "replicas: 3" || contextRow.RightText != "replicas: 3" {
		t.Fatalf("decoded context row text = %#v", contextRow)
	}
	assertLazySyntaxRanges(t, contextRow.LeftSyntax,
		lazyDiffPayloadSyntaxRange{Start: 0, End: 8, Class: yamlKeyClass},
		lazyDiffPayloadSyntaxRange{Start: 8, End: 9, Class: yamlPunctuationClass},
		lazyDiffPayloadSyntaxRange{Start: 10, End: 11, Class: yamlNumberClass},
	)
	assertLazySyntaxRanges(t, contextRow.RightSyntax,
		lazyDiffPayloadSyntaxRange{Start: 0, End: 8, Class: yamlKeyClass},
		lazyDiffPayloadSyntaxRange{Start: 8, End: 9, Class: yamlPunctuationClass},
		lazyDiffPayloadSyntaxRange{Start: 10, End: 11, Class: yamlNumberClass},
	)

	removedRow := decoded.Hunks[0].Rows[1]
	addedRow := decoded.Hunks[0].Rows[2]
	assertLazySyntaxRanges(t, removedRow.LeftSyntax,
		lazyDiffPayloadSyntaxRange{Start: 0, End: 7, Class: yamlKeyClass},
		lazyDiffPayloadSyntaxRange{Start: 7, End: 8, Class: yamlPunctuationClass},
		lazyDiffPayloadSyntaxRange{Start: 9, End: 14, Class: yamlBoolClass},
	)
	if len(removedRow.RightSyntax) != 0 {
		t.Fatalf("removed-only row right syntax = %+v, want none", removedRow.RightSyntax)
	}
	assertLazySyntaxRanges(t, addedRow.RightSyntax,
		lazyDiffPayloadSyntaxRange{Start: 0, End: 7, Class: yamlKeyClass},
		lazyDiffPayloadSyntaxRange{Start: 7, End: 8, Class: yamlPunctuationClass},
		lazyDiffPayloadSyntaxRange{Start: 9, End: 13, Class: yamlBoolClass},
	)
	if len(addedRow.LeftSyntax) != 0 {
		t.Fatalf("added-only row left syntax = %+v, want none", addedRow.LeftSyntax)
	}

	syntaxJSON, err := json.Marshal(contextRow.LeftSyntax)
	if err != nil {
		t.Fatalf("Marshal(left syntax) error = %v", err)
	}
	for _, forbidden := range []string{"replicas", "enabled", "plain"} {
		if strings.Contains(string(syntaxJSON), forbidden) {
			t.Fatalf("syntax metadata duplicated row text marker %q: %s", forbidden, syntaxJSON)
		}
	}
}

func TestRenderTooLargeResourceEmitsBlockedPlaceholder(t *testing.T) {
	out, err := Render(app.DiffResult{
		Results: []diff.Result{
			resourceChange("demo", "", "ConfigMap", "", "cm-too-large", diff.ChangeModified, diffWithLineCounts(10001, 10000)),
		},
	}, Options{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	text := string(out)
	article := resourceArticleByCanonicalTitle(t, text, "ConfigMap cm-too-large")
	for _, want := range []string{
		`data-lazy-diff="blocked"`,
		`data-lazy-state="blocked"`,
		`Diff too large for in-page rendering: 20,001 rows, +10,000/-10,001,`,
		`<button class="lazy-render-button" type="button" disabled aria-disabled="true" aria-label="Cannot render diff for ConfigMap cm-too-large.`,
		`Render diff unavailable`,
	} {
		if !strings.Contains(article, want) {
			t.Fatalf("too-large resource article missing %q:\n%s", want, article)
		}
	}
	for _, forbidden := range []string{
		`data-lazy-render`,
		`data-diff-payload`,
		`<table class="diff-table`,
		`<tr class="diff-row`,
	} {
		if strings.Contains(article, forbidden) {
			t.Fatalf("too-large resource article contains forbidden marker %q:\n%s", forbidden, article)
		}
	}
}

func TestRenderLazyPayloadScriptDataEscapesHostileContent(t *testing.T) {
	hostile := "message: </script><script>alert(\"x\")</script><img src=x onerror=alert(1)> \"quote\" 'single' `backtick` & " + "\u2028" + "\u2029"
	out, err := Render(app.DiffResult{
		Results: []diff.Result{
			resourceChange("demo", "", "ConfigMap", "", "cm-hostile", diff.ChangeModified, largeDiffWithHostileContent(hostile)),
		},
	}, Options{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	text := string(out)
	payload := payloadScriptData(t, text, "resource-0")
	for _, forbidden := range []string{
		"</script",
		"<script",
		"<",
		">",
		"&",
		"alert(\"x\")",
		"src=x",
		"onerror=alert",
		"'single'",
		"`backtick`",
		"\u2028",
		"\u2029",
	} {
		if strings.Contains(payload, forbidden) {
			t.Fatalf("lazy payload contains raw hostile marker %q:\n%s", forbidden, payload)
		}
	}
	for _, want := range []string{
		`\u003c/script`,
		`\u003cscript`,
		`\u003d`,
		`\u0026`,
		`\u0027single\u0027`,
		`\u0060backtick\u0060`,
		`\u2028`,
		`\u2029`,
	} {
		if !strings.Contains(payload, want) {
			t.Fatalf("lazy payload missing escaped marker %q:\n%s", want, payload)
		}
	}
	var decoded lazyDiffPayload
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatalf("lazy payload is not valid JSON: %v\n%s", err, payload)
	}
	if len(decoded.Hunks) == 0 || len(decoded.Hunks[0].Rows) == 0 {
		t.Fatalf("decoded lazy payload missing rows: %+v", decoded)
	}
	if decoded.Hunks[0].Rows[0].LeftText != hostile {
		t.Fatalf("decoded hostile text = %q, want %q", decoded.Hunks[0].Rows[0].LeftText, hostile)
	}
	assertLazySyntaxRangePrefix(t, decoded.Hunks[0].Rows[0].LeftSyntax,
		lazyDiffPayloadSyntaxRange{Start: 0, End: 7, Class: yamlKeyClass},
		lazyDiffPayloadSyntaxRange{Start: 7, End: 8, Class: yamlPunctuationClass},
	)
}

func TestRenderLazyScriptContract(t *testing.T) {
	out, err := Render(app.DiffResult{
		Results: []diff.Result{
			resourceChange("demo", "", "ConfigMap", "", "cm-large", diff.ChangeModified, diffWithLineCounts(1000, 1000)),
		},
	}, Options{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	text := string(out)
	for _, want := range []string{
		`const lazyRenderButtons = Array.from(document.querySelectorAll('[data-lazy-render]'));`,
		`const currentView = () => normalizedView(body.dataset.view);`,
		`const lazyPayloadScript = (resource) => resource?.querySelector('script[type="application/json"][data-diff-payload]') || null;`,
		`resource.dataset[renderedDataKey(view)] === 'true'`,
		`JSON.parse(script.textContent || '{}')`,
		`const table = renderLazyTable(payload, view);`,
		`renderLazyResource(button.closest('[data-resource-id]'), currentView());`,
		`if (resource?.dataset.lazyState === 'partial')`,
		`renderLazyResource(resource, currentView());`,
		`const syntaxClassWhitelist = new Set([`,
		`'yaml-key'`,
		`'yaml-punctuation'`,
		`const normalizeSyntaxRanges = (syntax, length) => {`,
		`syntaxClassWhitelist.has(className)`,
		`const appendSyntaxSegment = (parent, runes, start, end, syntax) => {`,
		`span.classList.add(range.className);`,
		`appendHighlightedText(cell, text, options.highlights || [], options.highlightClass || 'added', options.syntax || []);`,
		`syntax: row?.leftSyntax`,
		`syntax: row?.rightSyntax`,
		`document.createElement('table')`,
		`document.createTextNode`,
		`error.setAttribute('role', 'alert');`,
		`setLazyBusy(resource, true);`,
		`lazyPayloadScript(resource)?.remove();`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("HTML missing lazy JS contract %q:\n%s", want, text)
		}
	}
	for _, forbidden := range []string{
		"eval(",
		"innerHTML",
		"new Function",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("HTML contains forbidden JS contract marker %q:\n%s", forbidden, text)
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

func TestRenderTreeKeyboardNavigationContract(t *testing.T) {
	out, err := Render(app.DiffResult{
		Results: []diff.Result{
			diffResult("argocd", "demo", "cm-one", "@@ -1,1 +1,1 @@\n-old\n+new\n"),
			diffResult("argocd", "demo", "cm-two", "@@ -1,1 +1,1 @@\n-old\n+new\n"),
			diffResult("argocd", "other", "cm-three", "@@ -1,1 +1,1 @@\n-old\n+new\n"),
		},
	}, Options{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	text := string(out)
	for _, want := range []string{
		`let focusedTreeButton = null;`,
		`const treeButtonIsVisible = (button) => {`,
		`return !(app instanceof HTMLDetailsElement) || app.open;`,
		`const visibleTreeButtons = () => treeButtons.filter(treeButtonIsVisible);`,
		`const setTreeButtonTabStop = (button) => {`,
		`candidate.tabIndex = candidate === button ? 0 : -1;`,
		`const moveTreeFocus = (delta) => {`,
		`const focusTreeEdge = (edge) => {`,
		`const syncTreeFocusAfterFilter = () => {`,
		`setTreeButtonTabStop(activeButton);`,
		`button.addEventListener('keydown', (event) => {`,
		`event.key === 'ArrowDown'`,
		`event.key === 'ArrowUp'`,
		`event.key === 'Home'`,
		`event.key === 'End'`,
		`event.key === 'Enter' || event.key === ' '`,
		`button.click();`,
		`syncTreeFocusAfterFilter();`,
		`app.addEventListener('toggle', syncTreeFocusAfterFilter);`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("HTML missing tree keyboard contract %q:\n%s", want, text)
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

func TestRenderSidebarResizeScriptContract(t *testing.T) {
	out, err := Render(app.DiffResult{
		Results: []diff.Result{diffResult("argocd", "demo", "cm-one", "@@ -1,1 +1,1 @@\n-old\n+new\n")},
	}, Options{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	text := string(out)
	for _, want := range []string{
		`const sidebarResizer = document.querySelector('[data-sidebar-resizer]');`,
		`localStorage.removeItem(key);`,
		`const sidebarBounds = () => {`,
		`--sidebar-width-min`,
		`--sidebar-width-max`,
		`const desired = dragStartWidth + event.clientX - dragStartX;`,
		// Width persists on release, not on every move (move call omits persist).
		`setSidebarWidth(desired);`,
		`store.set('drydock-sidebar-width', String(next));`,
		`store.remove('drydock-sidebar-width');`,
		`sidebarResizer.addEventListener('dblclick'`,
		`body.classList.add('is-resizing-sidebar');`,
		`body.classList.remove('is-resizing-sidebar', 'sidebar-will-close');`,
		`restoreSidebarWidth();`,
		// Drag past the close threshold arms a snap-close.
		`const closeThreshold = () => sidebarBounds().min * (2 / 3);`,
		`willClose = desired < closeThreshold();`,
		`body.classList.toggle('sidebar-will-close', willClose);`,
		`setSidebar('closed');`,
		// Release is caught on window so a dropped pointer capture cannot leave the
		// handle stuck to the cursor.
		`window.addEventListener('pointermove', onSidebarPointerMove);`,
		`window.addEventListener('pointerup', stopSidebarResize);`,
		`window.addEventListener('pointercancel', stopSidebarResize);`,
		`window.removeEventListener('pointermove', onSidebarPointerMove);`,
		`window.removeEventListener('pointerup', stopSidebarResize);`,
		`window.removeEventListener('pointercancel', stopSidebarResize);`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("HTML missing sidebar resize JS contract %q:\n%s", want, text)
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
		`viewBox="0 0 128 128"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("HTML missing %q:\n%s", want, text)
		}
	}
	for _, stale := range []string{
		`viewBox="0 0 480 128"`,
		`>drydock</text>`,
	} {
		if strings.Contains(text, stale) {
			t.Fatalf("HTML contains stale wordmark marker %q:\n%s", stale, text)
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
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("HTML missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, `class="resource-meta"`) || strings.Contains(text, `&middot; modified`) {
		t.Fatalf("main diff viewport should not render secondary resource metadata:\n%s", text)
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
		`<span class="yaml-key">image</span><span class="yaml-punctuation">:</span> app:v<span class="inline-change removed">1</span>`,
		`<span class="yaml-key">image</span><span class="yaml-punctuation">:</span> app:v<span class="inline-change added">2</span>`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("HTML missing inline highlight %q:\n%s", want, text)
		}
	}
}

func TestRenderHighlightsYAMLSyntaxInEagerRows(t *testing.T) {
	out, err := Render(app.DiffResult{
		Results: []diff.Result{{
			Parent:   diff.Parent{Name: "demo"},
			Resource: diff.Resource{Kind: "ConfigMap", Name: "cm-one"},
			Change:   diff.ChangeModified,
			Diff: strings.Join([]string{
				"--- old",
				"+++ new",
				"@@ -1,5 +1,5 @@",
				" apiVersion: v1",
				" kind: ConfigMap",
				" metadata:",
				"   labels:",
				"     app.kubernetes.io/name: api",
				"",
			}, "\n"),
		}},
	}, Options{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	text := string(out)
	for _, want := range []string{
		`<span class="yaml-key">apiVersion</span><span class="yaml-punctuation">:</span> v1`,
		`<span class="yaml-key">app.kubernetes.io/name</span><span class="yaml-punctuation">:</span> api`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("HTML missing YAML syntax %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, `<span class="yaml-string">v1</span>`) || strings.Contains(text, `<span class="yaml-string">api</span>`) {
		t.Fatalf("plain scalar values should remain unstyled:\n%s", text)
	}
}

func TestRenderNestsYAMLSyntaxInsideInlineDiffSpans(t *testing.T) {
	out, err := Render(app.DiffResult{
		Results: []diff.Result{{
			Parent:   diff.Parent{Name: "demo"},
			Resource: diff.Resource{Kind: "ConfigMap", Name: "cm-one"},
			Change:   diff.ChangeModified,
			Diff:     "--- old\n+++ new\n@@ -1,1 +1,1 @@\n-value: \"alpha\"\n+value: \"omega\"\n",
		}},
	}, Options{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	text := string(out)
	for _, want := range []string{
		`<span class="yaml-key">value</span><span class="yaml-punctuation">:</span> <span class="yaml-string">&#34;</span><span class="inline-change removed"><span class="yaml-string">alph</span></span><span class="yaml-string">a&#34;</span>`,
		`<span class="yaml-key">value</span><span class="yaml-punctuation">:</span> <span class="yaml-string">&#34;</span><span class="inline-change added"><span class="yaml-string">omeg</span></span><span class="yaml-string">a&#34;</span>`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("HTML missing nested inline/YAML highlight %q:\n%s", want, text)
		}
	}
}

func TestRenderDoesNotSyntaxHighlightHunkOrFileHeaders(t *testing.T) {
	out, err := Render(app.DiffResult{
		Results: []diff.Result{{
			Parent:   diff.Parent{Name: "demo"},
			Resource: diff.Resource{Kind: "ConfigMap", Name: "cm-one"},
			Change:   diff.ChangeModified,
			Diff:     "--- file: old\n+++ file: new\n@@ -1,1 +1,1 @@ header: value\n-name: old\n+name: new\n",
		}},
	}, Options{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	text := string(out)
	for _, want := range []string{
		`<tr class="hunk-header"><th colspan="4">@@ -1,1 +1,1 @@ header: value</th></tr>`,
		`<tr class="hunk-header"><th colspan="2">@@ -1,1 +1,1 @@ header: value</th></tr>`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("HTML missing unhighlighted hunk header %q:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{
		`--- file: old`,
		`+++ file: new`,
		`<th colspan="4"><span class="yaml`,
		`<th colspan="2"><span class="yaml`,
	} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("HTML should not syntax-highlight or render file/hunk header marker %q:\n%s", unwanted, text)
		}
	}
}

func TestRenderComposedHighlightedTextNormalizesRangesAndIgnoresUnknownSyntax(t *testing.T) {
	var builder bytes.Buffer
	renderComposedHighlightedText(&builder, "ab: cd",
		[]highlightRange{
			{start: 4, end: 20},
			{start: 2, end: 2},
			{start: -4, end: 2},
		},
		"added",
		[]syntaxRange{
			{start: 2, end: 4, class: "yaml-not-real"},
			{start: 0, end: 2, class: yamlKeyClass},
			{start: 4, end: 6, class: yamlStringClass},
		},
	)
	want := `<span class="inline-change added"><span class="yaml-key">ab</span></span>: <span class="inline-change added"><span class="yaml-string">cd</span></span>`
	if got := builder.String(); got != want {
		t.Fatalf("composed highlighted text = %q, want %q", got, want)
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
		`<td class="line-number">11</td><td class="line-code"><span class="inline-change removed">remov</span>ed</td><td class="line-number line-number-blank"></td><td class="line-code line-code-blank"></td>`,
		`<td class="line-number line-number-blank"></td><td class="line-code line-code-blank"></td><td class="line-number">11</td><td class="line-code"><span class="inline-change added">add</span>ed</td>`,
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
		"No rendered manifest differences detected.",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("HTML missing %q:\n%s", want, text)
		}
	}
	for _, forbidden := range []string{
		`class="summary"`,
		`+0`,
		`-0`,
		`0 apps`,
		`0 resources`,
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("empty HTML contains zero summary marker %q:\n%s", forbidden, text)
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
	start := -1
	for _, leading := range []string{"", "\t", "\t\t"} {
		if index := strings.Index(text, "\n"+leading+prefix); index != -1 {
			start = index + 1 + len(leading)
			break
		}
	}
	if start == -1 {
		if !strings.HasPrefix(text, prefix) {
			t.Fatalf("HTML missing style rule %q:\n%s", selector, text)
		}
		start = 0
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

func assertLazySyntaxRanges(t *testing.T, got []lazyDiffPayloadSyntaxRange, want ...lazyDiffPayloadSyntaxRange) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("syntax ranges = %+v, want %+v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("syntax range[%d] = %+v, want %+v\nall ranges: %+v", index, got[index], want[index], got)
		}
	}
}

func assertLazySyntaxRangePrefix(t *testing.T, got []lazyDiffPayloadSyntaxRange, want ...lazyDiffPayloadSyntaxRange) {
	t.Helper()
	if len(got) < len(want) {
		t.Fatalf("syntax ranges = %+v, want prefix %+v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("syntax range prefix[%d] = %+v, want %+v\nall ranges: %+v", index, got[index], want[index], got)
		}
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

func diffWithChangedLineCount(changedLines int) string {
	removed := changedLines / 2
	added := changedLines - removed
	return diffWithLineCounts(removed, added)
}

func diffWithLineCounts(removed, added int) string {
	var builder strings.Builder
	builder.WriteString("--- old\n+++ new\n@@ -1,")
	builder.WriteString(strconv.Itoa(removed))
	builder.WriteString(" +1,")
	builder.WriteString(strconv.Itoa(added))
	builder.WriteString(" @@\n")
	for range removed {
		builder.WriteString("-old\n")
	}
	for range added {
		builder.WriteString("+new\n")
	}
	return builder.String()
}

func diffWithRawByteCount(t *testing.T, rawBytes int) string {
	t.Helper()
	prefix := "--- old\n+++ new\n@@ -1,1 +1,1 @@\n-a\n+"
	suffix := "\n"
	if rawBytes < len(prefix)+len(suffix) {
		t.Fatalf("raw byte diff length %d is smaller than minimum %d", rawBytes, len(prefix)+len(suffix))
	}
	return prefix + strings.Repeat("b", rawBytes-len(prefix)-len(suffix)) + suffix
}

func largeDiffWithHostileContent(hostile string) string {
	var builder strings.Builder
	builder.WriteString("--- old\n+++ new\n@@ -1,1000 +1,1000 @@\n")
	builder.WriteString("-")
	builder.WriteString(hostile)
	builder.WriteString("\n+")
	builder.WriteString(hostile)
	builder.WriteString("\n")
	for range 999 {
		builder.WriteString("-old\n+new\n")
	}
	return builder.String()
}

func payloadScriptData(t *testing.T, text, resourceID string) string {
	t.Helper()
	prefix := `<script type="application/json" data-diff-payload="` + resourceID + `">`
	start := strings.Index(text, prefix)
	if start == -1 {
		t.Fatalf("HTML missing lazy payload script for %s:\n%s", resourceID, text)
	}
	start += len(prefix)
	end := strings.Index(text[start:], "</script>")
	if end == -1 {
		t.Fatalf("HTML has unterminated lazy payload script for %s:\n%s", resourceID, text[start:])
	}
	return text[start : start+end]
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
