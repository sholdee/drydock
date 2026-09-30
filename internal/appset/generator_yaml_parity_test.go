package appset

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The expected parameters in this file come from running Argo CD v3.5.3's own
// generators on the ApplicationSet kube.SplitYAML makes of the same manifest:
// the list generator (applicationset/generators/list.go) and the git files
// generator (git.go) given the same file contents.
// Argo CD reads the manifest, elementsYaml (list.go:79-86) and git files
// (git.go:228-235) with sigs.k8s.io/yaml: YAML 1.1 booleans, timestamps kept
// as text, and numbers decoded by encoding/json as float64, so a goTemplate
// prints 1000000 as 1e+06, and the flat git files parameters are fmt %v of
// those values (git.go:264-267).

func TestGenerateListGeneratorReadsElementsLikeArgoCD(t *testing.T) {
	data := []byte(`apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: list
  namespace: argocd
spec:
  goTemplate: true
  generators:
    - list:
        elements:
          - cluster: elements
            enabled: no
            date: 2024-01-02
            num: 1000000
        elementsYaml: |
          - cluster: elementsyaml
            enabled: no
            date: 2024-01-02
            num: 1000000
  template:
    metadata:
      name: '{{.cluster}}-{{if .enabled}}enabled{{else}}disabled{{end}}'
      annotations:
        example.com/date: '{{.date}}'
        example.com/num: '{{.num}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: apps
        targetRevision: main
      destination:
        server: https://kubernetes.default.svc
        namespace: list
`)

	apps, diags, err := GenerateFromYAML(t.TempDir(), "appset.yaml", data)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	want := map[string]map[string]string{
		"elements-disabled":     {"example.com/date": "2024-01-02", "example.com/num": "1e+06"},
		"elementsyaml-disabled": {"example.com/date": "2024-01-02", "example.com/num": "1e+06"},
	}
	assertGeneratedAnnotations(t, apps, want)
}

func TestGenerateListGeneratorRejectsNonStringFlatElementValues(t *testing.T) {
	// `enabled: no` is a boolean, which Argo CD v3.5.3 rejects in a flat
	// element (list.go:67-70: "error parsing value as string").
	data := []byte(`apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: list
  namespace: argocd
spec:
  generators:
    - list:
        elements:
          - cluster: a
            enabled: no
  template:
    metadata:
      name: '{{cluster}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: apps
        targetRevision: main
      destination:
        server: https://kubernetes.default.svc
        namespace: list
`)

	_, _, err := GenerateFromYAML(t.TempDir(), "appset.yaml", data)
	if err == nil {
		t.Fatal("GenerateFromYAML() error = nil, want non-string element value error")
	}
	if !strings.Contains(err.Error(), `field "enabled" must be a string`) {
		t.Fatalf("GenerateFromYAML() error = %v, want non-string enabled field", err)
	}
}

func TestGenerateGitFilesGeneratorReadsFilesLikeArgoCD(t *testing.T) {
	const object = "cluster: dev\nenabled: no\ndate: 2024-01-02\nnum: 1000000\n"
	for _, tt := range []struct {
		name        string
		content     string
		goTemplate  string
		appName     string
		annotations string
		want        map[string]map[string]string
	}{
		{
			name:        "flat parameters",
			content:     object,
			goTemplate:  "false",
			appName:     "'{{cluster}}-{{enabled}}'",
			annotations: "        example.com/date: '{{date}}'\n        example.com/num: '{{num}}'\n",
			want:        map[string]map[string]string{"dev-false": {"example.com/date": "2024-01-02", "example.com/num": "1e+06"}},
		},
		{
			name:        "goTemplate",
			content:     object,
			goTemplate:  "true",
			appName:     "'{{.cluster}}-{{if .enabled}}enabled{{else}}disabled{{end}}'",
			annotations: "        example.com/date: '{{.date}}'\n        example.com/num: '{{.num}}'\n",
			want:        map[string]map[string]string{"dev-disabled": {"example.com/date": "2024-01-02", "example.com/num": "1e+06"}},
		},
		{
			// A file that is not one object is read as a list of objects
			// (git.go:228-235).
			name:        "list of objects",
			content:     "- cluster: dev\n  enabled: no\n  date: 2024-01-02\n  num: 1000000\n",
			goTemplate:  "true",
			appName:     "'{{.cluster}}-{{if .enabled}}enabled{{else}}disabled{{end}}'",
			annotations: "        example.com/date: '{{.date}}'\n        example.com/num: '{{.num}}'\n",
			want:        map[string]map[string]string{"dev-disabled": {"example.com/date": "2024-01-02", "example.com/num": "1e+06"}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeAppsetTestFile(t, filepath.Join(root, "clusters", "dev", "config.yaml"), tt.content)
			data := []byte(`apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: git
  namespace: argocd
spec:
  goTemplate: ` + tt.goTemplate + `
  generators:
    - git:
        files:
          - path: clusters/*/config.yaml
  template:
    metadata:
      name: ` + tt.appName + `
      annotations:
` + tt.annotations + `    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: apps
        targetRevision: main
      destination:
        server: https://kubernetes.default.svc
        namespace: git
`)

			apps, diags, err := GenerateFromYAML(root, "appset.yaml", data)
			if err != nil {
				t.Fatalf("GenerateFromYAML() error = %v", err)
			}
			if len(diags) != 0 {
				t.Fatalf("diagnostics = %#v", diags)
			}
			assertGeneratedAnnotations(t, apps, tt.want)
		})
	}
}

func assertGeneratedAnnotations(t *testing.T, apps []GeneratedApplication, want map[string]map[string]string) {
	t.Helper()
	got := make(map[string]map[string]string, len(apps))
	for _, app := range apps {
		got[app.Application.Name] = app.Application.Annotations
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("generated applications = %#v, want %#v (Argo CD v3.5.3)", got, want)
	}
}
