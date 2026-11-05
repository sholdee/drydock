package app

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/argoproj/argo-cd/v3/common"
	argodiff "github.com/argoproj/argo-cd/v3/util/argo/diff"
	"github.com/sholdee/drydock/internal/diff"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// writeAnnotatedDeploymentApp writes the demo Application and a Deployment
// carrying the given argocd.argoproj.io/ignore-differences annotation value
// (omitted when empty).
func writeAnnotatedDeploymentApp(t *testing.T, root string, replicas int, image, ignoreAnnotation string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", "demo.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    path: manifests/demo
    targetRevision: main
  destination:
    name: in-cluster
    namespace: demo
`)
	annotations := ""
	if ignoreAnnotation != "" {
		annotations = "  annotations:\n    " + common.AnnotationKeyIgnoreDifferences + ": |\n" + indentLines(ignoreAnnotation, "      ")
	}
	writeTestFile(t, filepath.Join(root, "manifests", "demo", "deployment.yaml"), `apiVersion: apps/v1
kind: Deployment
metadata:
  name: demo
`+annotations+`spec:
  replicas: `+fmt.Sprint(replicas)+`
  selector:
    matchLabels:
      app: demo
  template:
    metadata:
      labels:
        app: demo
    spec:
      containers:
        - name: app
          image: `+image+`
`)
	// A second Deployment with the same kind but another name must not pick up
	// the annotated object's rule.
	writeTestFile(t, filepath.Join(root, "manifests", "demo", "other.yaml"), `apiVersion: apps/v1
kind: Deployment
metadata:
  name: other
spec:
  replicas: `+fmt.Sprint(replicas)+`
  selector:
    matchLabels:
      app: other
  template:
    metadata:
      labels:
        app: other
    spec:
      containers:
        - name: app
          image: example/other:v1
`)
}

func indentLines(text, prefix string) string {
	var out strings.Builder
	for line := range strings.SplitSeq(strings.TrimRight(text, "\n"), "\n") {
		out.WriteString(prefix + line + "\n")
	}
	return out.String()
}

func runAnnotationDiff(t *testing.T, left, right string, showIgnored bool) []diff.Result {
	t.Helper()
	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		LeftPath:          left,
		RightPath:         right,
		Unified:           3,
		ShowIgnoredFields: showIgnored,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v", err)
	}
	return result.Results
}

func resultsByResourceName(results []diff.Result) map[string]diff.Result {
	out := make(map[string]diff.Result, len(results))
	for _, result := range results {
		out[result.Resource.Kind+"/"+result.Resource.Name] = result
	}
	return out
}

func TestOrchestratorDiffAppsHonorsIgnoreDifferencesAnnotation(t *testing.T) {
	for _, showIgnored := range []bool{false, true} {
		t.Run(fmt.Sprintf("showIgnoredFields=%t", showIgnored), func(t *testing.T) {
			root := t.TempDir()
			left := filepath.Join(root, "left")
			right := filepath.Join(root, "right")
			annotation := "jsonPointers:\n- /spec/replicas\n"
			writeAnnotatedDeploymentApp(t, left, 1, "example/app:v1", annotation)
			writeAnnotatedDeploymentApp(t, right, 2, "example/app:v1", annotation)

			byName := resultsByResourceName(runAnnotationDiff(t, left, right, showIgnored))
			if _, ok := byName["Deployment/demo"]; ok {
				t.Fatalf("annotated Deployment diff = %q, want replicas ignored", byName["Deployment/demo"].Diff)
			}
			other, ok := byName["Deployment/other"]
			if !ok || !strings.Contains(other.Diff, "+  replicas: 2") {
				t.Fatalf("results = %#v, want unannotated Deployment replicas diff kept", byName)
			}
		})
	}
}

func TestOrchestratorDiffAppsIgnoreDifferencesAnnotationOnOneSideOnly(t *testing.T) {
	annotation := "jsonPointers:\n- /spec/replicas\n"

	t.Run("current side only", func(t *testing.T) {
		root := t.TempDir()
		left := filepath.Join(root, "left")
		right := filepath.Join(root, "right")
		writeAnnotatedDeploymentApp(t, left, 1, "example/app:v1", "")
		writeAnnotatedDeploymentApp(t, right, 2, "example/app:v1", annotation)

		byName := resultsByResourceName(runAnnotationDiff(t, left, right, false))
		demo, ok := byName["Deployment/demo"]
		if !ok {
			t.Fatalf("results = %#v, want annotation addition reported", byName)
		}
		if !strings.Contains(demo.Diff, "+    "+common.AnnotationKeyIgnoreDifferences) {
			t.Fatalf("Diff = %q, want added annotation shown", demo.Diff)
		}
		if strings.Contains(demo.Diff, "replicas: ") {
			t.Fatalf("Diff = %q, want current-side annotation to ignore replicas on both sides", demo.Diff)
		}
	})

	t.Run("baseline side only", func(t *testing.T) {
		root := t.TempDir()
		left := filepath.Join(root, "left")
		right := filepath.Join(root, "right")
		writeAnnotatedDeploymentApp(t, left, 1, "example/app:v1", annotation)
		writeAnnotatedDeploymentApp(t, right, 2, "example/app:v1", "")

		byName := resultsByResourceName(runAnnotationDiff(t, left, right, false))
		demo, ok := byName["Deployment/demo"]
		if !ok {
			t.Fatalf("results = %#v, want Deployment diff", byName)
		}
		for _, want := range []string{"-  replicas: 1", "+  replicas: 2"} {
			if !strings.Contains(demo.Diff, want) {
				t.Fatalf("Diff = %q, want %q: baseline-only annotation must not apply while the current side has the resource", demo.Diff, want)
			}
		}
	})
}

func TestOrchestratorDiffAppsIgnoreDifferencesAnnotationInvalidValuesAreSkipped(t *testing.T) {
	tests := []struct {
		name       string
		annotation string
	}{
		{name: "invalid YAML", annotation: "jsonPointers: [/spec/replicas\n"},
		{name: "empty jsonPointers", annotation: "jsonPointers: []\n"},
		{name: "jq expressions are unsupported", annotation: "jqPathExpressions:\n- .spec.replicas\n"},
		{name: "scalar jsonPointers", annotation: "jsonPointers: /spec/replicas\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			left := filepath.Join(root, "left")
			right := filepath.Join(root, "right")
			writeAnnotatedDeploymentApp(t, left, 1, "example/app:v1", tt.annotation)
			writeAnnotatedDeploymentApp(t, right, 2, "example/app:v1", tt.annotation)

			byName := resultsByResourceName(runAnnotationDiff(t, left, right, false))
			demo, ok := byName["Deployment/demo"]
			if !ok || !strings.Contains(demo.Diff, "+  replicas: 2") {
				t.Fatalf("results = %#v, want replicas diff kept for skipped annotation", byName)
			}
		})
	}
}

func TestOrchestratorDiffAppsIgnoreDifferencesAnnotationInvalidAndRootPointersAreDropped(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	annotation := `jsonPointers:
- ""
- spec/template
- /spec/template/spec/containers/app/image
- /spec/replicas
`
	writeAnnotatedDeploymentApp(t, left, 1, "example/app:v1", annotation)
	writeAnnotatedDeploymentApp(t, right, 2, "example/app:v2", annotation)

	byName := resultsByResourceName(runAnnotationDiff(t, left, right, false))
	demo, ok := byName["Deployment/demo"]
	if !ok {
		t.Fatalf("results = %#v, want Deployment kept: root pointer must not remove the resource", byName)
	}
	if !strings.Contains(demo.Diff, "+        - image: example/app:v2") {
		t.Fatalf("Diff = %q, want image diff kept", demo.Diff)
	}
	if strings.Contains(demo.Diff, "replicas: ") {
		t.Fatalf("Diff = %q, want valid /spec/replicas pointer still applied", demo.Diff)
	}
}

func TestOrchestratorDiffAppsIgnoreDifferencesAnnotationOnChildApplication(t *testing.T) {
	for _, annotated := range []bool{false, true} {
		t.Run(fmt.Sprintf("annotated=%t", annotated), func(t *testing.T) {
			root := t.TempDir()
			left := filepath.Join(root, "left")
			right := filepath.Join(root, "right")
			writeAnnotatedChildApplicationApps(t, left, "false", annotated)
			writeAnnotatedChildApplicationApps(t, right, "true", annotated)

			result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
				LeftPath:  left,
				RightPath: right,
				Unified:   3,
			})
			if err != nil {
				t.Fatalf("DiffApps() error = %v", err)
			}
			var childDiff *diff.Result
			for i, item := range result.Results {
				if item.Parent.Name == "root" && item.Resource.Kind == "Application" && item.Resource.Name == "child" {
					childDiff = &result.Results[i]
				}
			}
			switch {
			case annotated && childDiff != nil:
				t.Fatalf("child Application diff = %q, want annotated /spec/syncPolicy ignored", childDiff.Diff)
			case !annotated && (childDiff == nil || !strings.Contains(childDiff.Diff, "+      selfHeal: true")):
				t.Fatalf("results = %#v, want unannotated child Application selfHeal diff", result.Results)
			}
		})
	}
}

func writeAnnotatedChildApplicationApps(t *testing.T, root, selfHeal string, annotated bool) {
	t.Helper()
	annotation := ""
	if annotated {
		annotation = "  annotations:\n    " + common.AnnotationKeyIgnoreDifferences + ": |\n      jsonPointers:\n      - /spec/syncPolicy\n"
	}
	writeTestFile(t, filepath.Join(root, "apps", "root.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: root
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    path: children
    targetRevision: main
  destination:
    name: in-cluster
    namespace: argocd
`)
	writeTestFile(t, filepath.Join(root, "children", "child.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: child
  namespace: argocd
`+annotation+`spec:
  source:
    repoURL: https://github.com/example/repo
    path: manifests/child
    targetRevision: main
  destination:
    name: in-cluster
    namespace: child
  syncPolicy:
    automated:
      selfHeal: `+selfHeal+`
`)
	writeTestFile(t, filepath.Join(root, "manifests", "child", "configmap.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: child
data:
  value: v1
`)
}

// TestAnnotationIgnoreJSONPointersParityWithArgoCD pins the annotation parser
// to Argo CD v3.6.1's ExtractIgnoreDifferencesFromAnnotations.
func TestAnnotationIgnoreJSONPointersParityWithArgoCD(t *testing.T) {
	values := []string{
		"",
		"jsonPointers:\n- /spec/replicas\n",
		"jsonPointers:\n- /spec/replicas\n- /metadata/labels/version\n",
		`{"jsonPointers": ["/spec/replicas"]}`,
		"JSONPointers:\n- /spec/replicas\n",
		"jsonpointers:\n- /spec/replicas\n",
		"jsonPointers: []\n",
		"jsonPointers:\n",
		"jsonPointers: /spec/replicas\n",
		"jsonPointers: [/spec/replicas\n",
		"jsonPointers:\n- 1\n",
		"jsonPointers:\n- null\n",
		"jsonPointers:\n- \"\"\n- spec/replicas\n",
		"jqPathExpressions:\n- .spec.replicas\n",
		"jsonPointers:\n- /spec/replicas\nunknown: field\n",
		"- /spec/replicas\n",
		"not yaml: [",
	}
	for _, value := range values {
		t.Run(fmt.Sprintf("%q", value), func(t *testing.T) {
			obj := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "apps/v1",
				"kind":       "Deployment",
				"metadata": map[string]any{
					"name":        "web",
					"namespace":   "default",
					"annotations": map[string]any{common.AnnotationKeyIgnoreDifferences: value},
				},
			}}
			got := annotationIgnoreJSONPointers(obj)

			var want []string
			rules := argodiff.ExtractIgnoreDifferencesFromAnnotations([]*unstructured.Unstructured{obj})
			if len(rules) > 1 {
				t.Fatalf("upstream rules = %#v, want at most one", rules)
			}
			if len(rules) == 1 {
				rule := rules[0]
				if rule.Group != "apps" || rule.Kind != "Deployment" || rule.Name != "web" || rule.Namespace != "default" {
					t.Fatalf("upstream rule = %#v, want rule scoped to the annotated object", rule)
				}
				if len(rule.JQPathExpressions) != 0 || len(rule.ManagedFieldsManagers) != 0 {
					t.Fatalf("upstream rule = %#v, want jsonPointers only", rule)
				}
				want = rule.JSONPointers
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("annotationIgnoreJSONPointers() = %#v, upstream = %#v", got, want)
			}
		})
	}
}

func TestAnnotationIgnoreJSONPointersWithoutAnnotation(t *testing.T) {
	if got := annotationIgnoreJSONPointers(nil); got != nil {
		t.Fatalf("annotationIgnoreJSONPointers(nil) = %#v, want nil", got)
	}
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ConfigMap"}}
	if got := annotationIgnoreJSONPointers(obj); got != nil {
		t.Fatalf("annotationIgnoreJSONPointers() = %#v, want nil", got)
	}
}
