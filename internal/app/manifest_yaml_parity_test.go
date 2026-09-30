package app

import (
	"context"
	"path/filepath"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// These tests write Application manifests as files and let discovery read
// them, as Argo CD v3.5.3 receives them through kubectl or an app-of-apps
// source: apimachinery's YAMLOrJSONDecoder with sigs.k8s.io/yaml, which parses
// YAML 1.1 (cli-runtime pkg/resource/visitor.go, gitops-engine
// pkg/utils/kube/kube.go SplitYAML).

// TestBuildApplicationYAMLValuesObjectMatchesHelmCLI pins what a chart sees
// from a valuesObject written in YAML. The expected strings are helm v4.2.1
// output (argo-cd hack/tool-versions.sh) for the --values file Argo CD writes
// for the same Application: ApplicationSourceHelm.ValuesYAML(), that is
// sigs.k8s.io/yaml.JSONToYAML of the stored valuesObject
// (pkg/apis/application/v1alpha1/values.go:91-101).
func TestBuildApplicationYAMLValuesObjectMatchesHelmCLI(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "values.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: values
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    path: chart
    helm:
      valuesObject:
        enabled: no
        date: 2024-01-02
        coords:
          x: 1
          y: 2
  destination:
    server: https://kubernetes.default.svc
    namespace: values
`)
	writeTestFile(t, filepath.Join(root, "chart", "Chart.yaml"), "apiVersion: v2\nname: values\nversion: 0.1.0\n")
	writeTestFile(t, filepath.Join(root, "chart", "templates", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: values
data:
  enabled.kind: {{ kindOf .Values.enabled | quote }}
  enabled.branch: {{ if .Values.enabled }}"true-branch"{{ else }}"false-branch"{{ end }}
  date: {{ .Values.date | quote }}
  coordKeys: {{ keys .Values.coords | sortAlpha | join "," | quote }}
  coordY: {{ .Values.coords.y | default "missing" | quote }}
`)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	configMap, ok := findManifestByKindAndName(result.Manifests, "ConfigMap", "values")
	if !ok {
		t.Fatalf("ConfigMap/values not rendered: %#v", result.Manifests)
	}
	got, _, err := unstructured.NestedStringMap(configMap.Object.Object, "data")
	if err != nil {
		t.Fatalf("data: %v", err)
	}
	// helm v4.2.1: the --values file reads `enabled: false`, `date:
	// "2024-01-02"`, and coords `"true": 2` beside `x: 1`.
	want := map[string]string{
		"enabled.kind":   "bool",
		"enabled.branch": "false-branch",
		"date":           "2024-01-02",
		"coordKeys":      "true,x",
		"coordY":         "missing",
	}
	for key, wantValue := range want {
		if got[key] != wantValue {
			t.Errorf("data[%q] = %q, want %q (helm v4.2.1)", key, got[key], wantValue)
		}
	}
	if len(got) != len(want) {
		t.Errorf("rendered %d data keys, want %d: %v", len(got), len(want), got)
	}
}

// TestBuildApplicationYAMLDirectoryRecurseYes pins a typed boolean written as
// a YAML 1.1 word: kubectl sends `recurse: true`, which the CRD's boolean
// schema accepts (manifests/crds/application-crd.yaml:237-240), so Argo CD
// renders the directory recursively.
func TestBuildApplicationYAMLDirectoryRecurseYes(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "recurse.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: recurse
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    path: manifests
    directory:
      recurse: yes
  destination:
    server: https://kubernetes.default.svc
    namespace: recurse
`)
	writeTestFile(t, filepath.Join(root, "manifests", "top.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: top\n")
	writeTestFile(t, filepath.Join(root, "manifests", "nested", "deep.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: deep\n")

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	for _, name := range []string{"top", "deep"} {
		if _, ok := findManifestByKindAndName(result.Manifests, "ConfigMap", name); !ok {
			t.Errorf("ConfigMap/%s not rendered: %#v", name, result.Manifests)
		}
	}
}
