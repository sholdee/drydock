package diffhtml

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFullRenderedDiffViewExampleMatchesRenderedDemo(t *testing.T) {
	text := readFullRenderedDiffViewExample(t)

	envoyDeploymentID := resourceIDByHeading(t, text, "apps Deployment envoy-gateway-system/envoy-gateway")
	if got := defaultResourceFromBody(t, text); got != envoyDeploymentID {
		t.Fatalf("default resource = %q, want Envoy Deployment %q\n%s", got, envoyDeploymentID, text)
	}

	for _, want := range []string{
		`<title>drydock desired state diff</title>`,
		`<p class="summary">2 apps, 3 resources, +9/-9</p>`,
		`data-tree-app="envoy-gateway-system"`,
		`data-tree-app="renovate"`,
		`<span class="tree-resource-label">Deployment · envoy-gateway</span>`,
		`<span class="tree-resource-label">PDB · envoy-gateway</span>`,
		`<span class="tree-resource-label">RenovateJob · renovate</span>`,
		`<span class="tree-delta-added">+3</span><span class="tree-delta-removed">-3</span>`,
		`<span class="tree-delta-added">+1</span><span class="tree-delta-removed">-1</span>`,
		`<span class="tree-delta-added">+5</span><span class="tree-delta-removed">-5</span>`,
		`<h3>policy PodDisruptionBudget envoy-gateway-system/envoy-gateway</h3>`,
		`<h3>renovate-operator.mogenius.com RenovateJob renovate/renovate</h3>`,
		`<td class="line-code">  replicas: <span class="inline-change removed">3</span></td>`,
		`<td class="line-code">  replicas: <span class="inline-change added">2</span></td>`,
		`<td class="line-code">  minAvailable: <span class="inline-change removed">1</span></td>`,
		`<td class="line-code">  minAvailable: <span class="inline-change added">2</span></td>`,
		`<td class="line-code">  schedule: <span class="inline-change removed">0</span> * * * *</td>`,
		`<td class="line-code">  schedule: <span class="inline-change added">15</span> * * * *</td>`,
		`<td class="line-code">  image: renovate/renovate:43.20<span class="inline-change removed">5.3@sha256:53a36e2d4da0fea960e6d4ebac3da152233532c0be1c14313086011e7c4bb551</span></td>`,
		`<td class="line-code">  image: renovate/renovate:43.20<span class="inline-change added">7.4@sha256:087bab575172b1926bbc57124d988015d899b0a82d45028514377b10a392f69d</span></td>`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("example HTML missing %q:\n%s", want, text)
		}
	}

	for _, stale := range []string{
		"payments-api",
		"payments-worker",
		"argocd/payments-worker",
		"ghcr.io/acme",
		"skipped Secret payments-api",
	} {
		if strings.Contains(text, stale) {
			t.Fatalf("example HTML contains stale synthetic fixture %q:\n%s", stale, text)
		}
	}
}

func readFullRenderedDiffViewExample(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "..", "..", "site", "static", "examples", "full-rendered-diff-view.html")
	text, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(text)
}

func resourceIDByHeading(t *testing.T, text, heading string) string {
	t.Helper()
	article := resourceArticleByHeading(t, text, heading)
	prefix := `<article class="resource" data-resource-id="`
	start := strings.Index(article, prefix)
	if start == -1 {
		t.Fatalf("resource article for %q missing data-resource-id:\n%s", heading, article)
	}
	remaining := article[start+len(prefix):]
	end := strings.Index(remaining, `"`)
	if end == -1 {
		t.Fatalf("resource article for %q has unterminated data-resource-id:\n%s", heading, article)
	}
	return remaining[:end]
}

func resourceArticleByHeading(t *testing.T, text, heading string) string {
	t.Helper()
	headingHTML := "<h3>" + heading + "</h3>"
	headingIndex := strings.Index(text, headingHTML)
	if headingIndex == -1 {
		t.Fatalf("HTML missing resource heading %q:\n%s", heading, text)
	}
	start := strings.LastIndex(text[:headingIndex], `<article class="resource"`)
	if start == -1 {
		t.Fatalf("HTML missing resource article before heading %q:\n%s", heading, text)
	}
	afterHeading := text[headingIndex:]
	endOffset := strings.Index(afterHeading, "</article>")
	if endOffset == -1 {
		t.Fatalf("HTML missing resource article close after heading %q:\n%s", heading, text[start:])
	}
	return text[start : headingIndex+endOffset+len("</article>")]
}
