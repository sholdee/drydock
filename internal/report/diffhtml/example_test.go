package diffhtml

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/app"
	"github.com/sholdee/drydock/internal/diff"
)

func TestFullRenderedDiffViewExampleIsCurrent(t *testing.T) {
	path := fullRenderedDiffViewExamplePath()
	rendered := renderFullRenderedDiffViewDemo(t)
	if os.Getenv("UPDATE_DIFFHTML_EXAMPLE") == "1" {
		if err := os.WriteFile(path, rendered, 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, err)
		}
		return
	}

	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", path, err)
	}
	if string(current) != string(rendered) {
		t.Fatalf("example HTML is stale; run UPDATE_DIFFHTML_EXAMPLE=1 go test ./internal/report/diffhtml -run TestFullRenderedDiffViewExampleIsCurrent")
	}
}

func TestFullRenderedDiffViewExampleMatchesRenderedDemo(t *testing.T) {
	text := readFullRenderedDiffViewExample(t)

	envoyDeploymentID := resourceIDByHeading(t, text, "apps Deployment envoy-gateway-system/envoy-gateway")
	if got := defaultResourceFromBody(t, text); got != envoyDeploymentID {
		t.Fatalf("default resource = %q, want Envoy Deployment %q\n%s", got, envoyDeploymentID, text)
	}

	for _, want := range []string{
		`<title>drydock diff</title>`,
		`<div class="summary" aria-label="2 apps, 3 resources, 3 changed, +9, -9">`,
		`<span class="summary-badge summary-badge-neutral">2 apps</span>`,
		`<span class="summary-badge summary-badge-neutral">3 resources</span>`,
		`<span class="summary-badge summary-badge-modified summary-badge-detail">3 changed</span>`,
		`<span class="summary-badge summary-badge-added">+9</span>`,
		`<span class="summary-badge summary-badge-removed">-9</span>`,
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
	path := fullRenderedDiffViewExamplePath()
	text, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(text)
}

func fullRenderedDiffViewExamplePath() string {
	return filepath.Join("..", "..", "..", "site", "static", "examples", "full-rendered-diff-view.html")
}

func renderFullRenderedDiffViewDemo(t *testing.T) []byte {
	t.Helper()
	rendered, err := Render(app.DiffResult{
		Results: []diff.Result{
			{
				Parent: diff.Parent{Name: "envoy-gateway-system"},
				Resource: diff.Resource{
					Group:     "apps",
					Kind:      "Deployment",
					Namespace: "envoy-gateway-system",
					Name:      "envoy-gateway",
				},
				Change: diff.ChangeModified,
				Diff: `--- Application: envoy-gateway-system Source: 0 apps/envoy-gateway-system/kustomization.yaml apps/Deployment: envoy-gateway-system/envoy-gateway
+++ Application: envoy-gateway-system Source: 0 apps/envoy-gateway-system/kustomization.yaml apps/Deployment: envoy-gateway-system/envoy-gateway
@@ -11,7 +11,7 @@
   name: envoy-gateway
   namespace: envoy-gateway-system
 spec:
-  replicas: 3
+  replicas: 2
   selector:
     matchLabels:
       app.kubernetes.io/instance: envoy-gateway
@@ -67,10 +67,10 @@
             periodSeconds: 10
           resources:
             limits:
-              cpu: 100m
+              cpu: 150m
               memory: 150Mi
             requests:
-              cpu: 10m
+              cpu: 25m
               memory: 150Mi
           securityContext:
             allowPrivilegeEscalation: false
`,
			},
			{
				Parent: diff.Parent{Name: "envoy-gateway-system"},
				Resource: diff.Resource{
					Group:     "policy",
					Kind:      "PodDisruptionBudget",
					Namespace: "envoy-gateway-system",
					Name:      "envoy-gateway",
				},
				Change: diff.ChangeModified,
				Diff: `--- Application: envoy-gateway-system Source: 0 apps/envoy-gateway-system/kustomization.yaml policy/PodDisruptionBudget: envoy-gateway-system/envoy-gateway
+++ Application: envoy-gateway-system Source: 0 apps/envoy-gateway-system/kustomization.yaml policy/PodDisruptionBudget: envoy-gateway-system/envoy-gateway
@@ -6,7 +6,7 @@
   name: envoy-gateway
   namespace: envoy-gateway-system
 spec:
-  minAvailable: 1
+  minAvailable: 2
   selector:
     matchLabels:
       app.kubernetes.io/instance: envoy-gateway
`,
			},
			{
				Parent: diff.Parent{Name: "renovate"},
				Resource: diff.Resource{
					Group:     "renovate-operator.mogenius.com",
					Kind:      "RenovateJob",
					Namespace: "renovate",
					Name:      "renovate",
				},
				Change: diff.ChangeModified,
				Diff: `--- Application: renovate Source: 0 apps/renovate/kustomization.yaml renovate-operator.mogenius.com/RenovateJob: renovate/renovate
+++ Application: renovate Source: 0 apps/renovate/kustomization.yaml renovate-operator.mogenius.com/RenovateJob: renovate/renovate
@@ -34,18 +34,18 @@
       value: http://10.2.0.110:3900
     - name: S3_FORCE_PATH_STYLE
       value: "true"
-  image: renovate/renovate:43.205.3@sha256:53a36e2d4da0fea960e6d4ebac3da152233532c0be1c14313086011e7c4bb551
-  parallelism: 3
+  image: renovate/renovate:43.207.4@sha256:087bab575172b1926bbc57124d988015d899b0a82d45028514377b10a392f69d
+  parallelism: 2
   provider:
     name: github
   resources:
     limits:
-      cpu: 500m
+      cpu: 750m
       memory: 2048Mi
     requests:
-      cpu: 500m
+      cpu: 750m
       memory: 2048Mi
-  schedule: 0 * * * *
+  schedule: 15 * * * *
   secretRef: renovate-secret
   webhook:
     authentication:
`,
			},
		},
	}, Options{
		DefaultResource: DefaultResourceSelector{
			ParentName: "envoy-gateway-system",
			Group:      "apps",
			Kind:       "Deployment",
			Namespace:  "envoy-gateway-system",
			Name:       "envoy-gateway",
		},
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	return rendered
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
