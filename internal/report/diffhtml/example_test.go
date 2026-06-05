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
		`<div class="summary" aria-label="2 apps, 4 resources, 2 changed, 1 added, 1 deleted, +30, -26">`,
		`<span class="summary-badge summary-badge-neutral">2 apps</span>`,
		`<span class="summary-badge summary-badge-neutral">4 resources</span>`,
		`<span class="summary-badge summary-badge-modified summary-badge-detail">2 changed</span>`,
		`<span class="summary-badge summary-badge-added summary-badge-detail">1 added</span>`,
		`<span class="summary-badge summary-badge-removed summary-badge-detail">1 deleted</span>`,
		`<span class="summary-badge summary-badge-added">+30</span>`,
		`<span class="summary-badge summary-badge-removed">-26</span>`,
		`data-tree-app="argocd/envoy-gateway-system"`,
		`data-tree-app="argocd/renovate"`,
		`<span class="tree-resource-label">Deployment · envoy-gateway</span>`,
		`<span class="tree-resource-label">PDB · envoy-gateway</span>`,
		`<span class="tree-resource-label">ServiceMonitor · renovate</span>`,
		`<span class="tree-resource-label">RenovateJob · renovate</span>`,
		`tree-status-dot tree-status-added`,
		`tree-status-dot tree-status-removed`,
		`<span class="tree-delta-added">+9</span><span class="tree-delta-removed">-9</span>`,
		`<span class="tree-delta-removed">-12</span>`,
		`<span class="tree-delta-added">+16</span>`,
		`<span class="tree-delta-added">+5</span><span class="tree-delta-removed">-5</span>`,
		`data-change="removed"`,
		`data-change="added"`,
		`<h3>policy PodDisruptionBudget envoy-gateway-system/envoy-gateway</h3>`,
		`<h3>monitoring.coreos.com ServiceMonitor renovate/renovate</h3>`,
		`<h3>renovate-operator.mogenius.com RenovateJob renovate/renovate</h3>`,
		`<td class="line-code">  <span class="yaml-key">replicas</span><span class="yaml-punctuation">:</span> <span class="inline-change removed"><span class="yaml-number">3</span></span></td>`,
		`<td class="line-code">  <span class="yaml-key">replicas</span><span class="yaml-punctuation">:</span> <span class="inline-change added"><span class="yaml-number">2</span></span></td>`,
		`<td class="line-code">            <span class="yaml-punctuation">-</span> --gateway-class-name=<span class="inline-change removed">envoy</span>-gateway</td>`,
		`<td class="line-code">            <span class="yaml-punctuation">-</span> --gateway-class-name=<span class="inline-change added">platform</span>-gateway</td>`,
		`<td class="line-code">            <span class="yaml-punctuation">-</span> --metrics-bind-address=0.0.0.0:1900<span class="inline-change removed">1</span></td>`,
		`<td class="line-code">            <span class="yaml-punctuation">-</span> --metrics-bind-address=0.0.0.0:1900<span class="inline-change added">2</span></td>`,
		`<td class="line-code">            <span class="yaml-punctuation">-</span> --enable-wasm-extension=<span class="inline-change removed">fals</span>e</td>`,
		`<td class="line-code">            <span class="yaml-punctuation">-</span> --enable-wasm-extension=<span class="inline-change added">tru</span>e</td>`,
		`<td class="line-code">          <span class="yaml-key">image</span><span class="yaml-punctuation">:</span> docker.io/envoyproxy/gateway:v1.<span class="inline-change removed">3.2</span></td>`,
		`<td class="line-code">          <span class="yaml-key">image</span><span class="yaml-punctuation">:</span> docker.io/envoyproxy/gateway:v1.<span class="inline-change added">4.0</span></td>`,
		`<td class="line-code"><span class="yaml-key">apiVersion</span><span class="yaml-punctuation">:</span> policy/v1</td>`,
		`<td class="line-code"><span class="yaml-key">apiVersion</span><span class="yaml-punctuation">:</span> monitoring.coreos.com/v1</td>`,
		`<td class="line-code">  <span class="yaml-key">schedule</span><span class="yaml-punctuation">:</span> <span class="inline-change removed"><span class="yaml-number">0</span></span> * * * *</td>`,
		`<td class="line-code">  <span class="yaml-key">schedule</span><span class="yaml-punctuation">:</span> <span class="inline-change added"><span class="yaml-number">15</span></span> * * * *</td>`,
		`<td class="line-code">  <span class="yaml-key">image</span><span class="yaml-punctuation">:</span> renovate/renovate:43.20<span class="inline-change removed">5.3@sha256:53a36e2d4da0fea960e6d4ebac3da152233532c0be1c14313086011e7c4bb551</span></td>`,
		`<td class="line-code">  <span class="yaml-key">image</span><span class="yaml-punctuation">:</span> renovate/renovate:43.20<span class="inline-change added">7.4@sha256:087bab575172b1926bbc57124d988015d899b0a82d45028514377b10a392f69d</span></td>`,
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
				Parent: diff.Parent{Namespace: "argocd", Name: "envoy-gateway-system"},
				Resource: diff.Resource{
					Group:     "apps",
					Kind:      "Deployment",
					Namespace: "envoy-gateway-system",
					Name:      "envoy-gateway",
				},
				Change: diff.ChangeModified,
				Diff: `--- Application: argocd/envoy-gateway-system Source: 0 platform/envoy-gateway/kustomization.yaml apps/Deployment: envoy-gateway-system/envoy-gateway
+++ Application: argocd/envoy-gateway-system Source: 0 platform/envoy-gateway/kustomization.yaml apps/Deployment: envoy-gateway-system/envoy-gateway
@@ -8,7 +8,7 @@
   name: envoy-gateway
   namespace: envoy-gateway-system
 spec:
-  replicas: 3
+  replicas: 2
   selector:
     matchLabels:
       app.kubernetes.io/instance: envoy-gateway
@@ -19,17 +19,17 @@
     spec:
       containers:
         - args:
-            - --gateway-class-name=envoy-gateway
-            - --metrics-bind-address=0.0.0.0:19001
-            - --enable-wasm-extension=false
-          image: docker.io/envoyproxy/gateway:v1.3.2
+            - --gateway-class-name=platform-gateway
+            - --metrics-bind-address=0.0.0.0:19002
+            - --enable-wasm-extension=true
+          image: docker.io/envoyproxy/gateway:v1.4.0
           name: envoy-gateway
           resources:
             limits:
-              cpu: 100m
-              memory: 150Mi
+              cpu: 150m
+              memory: 192Mi
             requests:
-              cpu: 10m
-              memory: 150Mi
+              cpu: 25m
+              memory: 192Mi
           securityContext:
             allowPrivilegeEscalation: false
`,
			},
			{
				Parent: diff.Parent{Namespace: "argocd", Name: "envoy-gateway-system"},
				Resource: diff.Resource{
					Group:     "policy",
					Kind:      "PodDisruptionBudget",
					Namespace: "envoy-gateway-system",
					Name:      "envoy-gateway",
				},
				Change: diff.ChangeRemoved,
				Diff: `--- Application: argocd/envoy-gateway-system Source: 0 platform/envoy-gateway/kustomization.yaml policy/PodDisruptionBudget: envoy-gateway-system/envoy-gateway
+++ Application: argocd/envoy-gateway-system Source: 0 platform/envoy-gateway/kustomization.yaml policy/PodDisruptionBudget: envoy-gateway-system/envoy-gateway
@@ -1,12 +0,0 @@
-apiVersion: policy/v1
-kind: PodDisruptionBudget
-metadata:
-  annotations:
-    argocd.argoproj.io/tracking-id: envoy-gateway-system:policy/PodDisruptionBudget:envoy-gateway-system/envoy-gateway
-  name: envoy-gateway
-  namespace: envoy-gateway-system
-spec:
-  minAvailable: 1
-  selector:
-    matchLabels:
-      app.kubernetes.io/instance: envoy-gateway
`,
			},
			{
				Parent: diff.Parent{Namespace: "argocd", Name: "renovate"},
				Resource: diff.Resource{
					Group:     "monitoring.coreos.com",
					Kind:      "ServiceMonitor",
					Namespace: "renovate",
					Name:      "renovate",
				},
				Change: diff.ChangeAdded,
				Diff: `--- Application: argocd/renovate Source: 0 apps/renovate/templates/servicemonitor.yaml monitoring.coreos.com/ServiceMonitor: renovate/renovate
+++ Application: argocd/renovate Source: 0 apps/renovate/templates/servicemonitor.yaml monitoring.coreos.com/ServiceMonitor: renovate/renovate
@@ -0,0 +1,16 @@
+apiVersion: monitoring.coreos.com/v1
+kind: ServiceMonitor
+metadata:
+  annotations:
+    argocd.argoproj.io/tracking-id: renovate:monitoring.coreos.com/ServiceMonitor:renovate/renovate
+  name: renovate
+  namespace: renovate
+spec:
+  endpoints:
+    - interval: 30s
+      path: /metrics
+      port: metrics
+      scrapeTimeout: 10s
+  selector:
+    matchLabels:
+      app.kubernetes.io/name: renovate
`,
			},
			{
				Parent: diff.Parent{Namespace: "argocd", Name: "renovate"},
				Resource: diff.Resource{
					Group:     "renovate-operator.mogenius.com",
					Kind:      "RenovateJob",
					Namespace: "renovate",
					Name:      "renovate",
				},
				Change: diff.ChangeModified,
				Diff: `--- Application: argocd/renovate Source: 0 apps/renovate/templates/renovatejob.yaml renovate-operator.mogenius.com/RenovateJob: renovate/renovate
+++ Application: argocd/renovate Source: 0 apps/renovate/templates/renovatejob.yaml renovate-operator.mogenius.com/RenovateJob: renovate/renovate
@@ -11,18 +11,18 @@
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
	}, Options{})
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
