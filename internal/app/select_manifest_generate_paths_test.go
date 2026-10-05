package app

import (
	"path/filepath"
	"reflect"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/discovery"
)

func TestManifestGeneratePaths(t *testing.T) {
	readerSource := &argoappv1.ApplicationSource{RepoURL: "https://github.com/example/repo", Path: "apps/reader"}
	for _, tc := range []struct {
		name       string
		annotation string // absent when empty
		source     *argoappv1.ApplicationSource
		sources    argoappv1.ApplicationSources
		want       []string // nil: owns nothing
	}{
		{name: "no annotation", source: readerSource},
		{name: "blank annotation", annotation: " ; ", source: readerSource},
		{name: "relative to the source path", annotation: "config", source: readerSource, want: []string{"apps/reader/config"}},
		{name: "dot is the source path", annotation: ".", source: readerSource, want: []string{"apps/reader"}},
		{name: "absolute from the repository root", annotation: "/shared/config.yaml", source: readerSource, want: []string{"shared/config.yaml"}},
		{
			name:       "semicolon list with spaces and empty entries",
			annotation: " . ; /shared;; ../common ;",
			source:     readerSource,
			want:       []string{"apps/reader", "shared", "apps/common"},
		},
		{name: "escapes the source path but not the repository", annotation: "../../shared", source: readerSource, want: []string{"shared"}},
		{name: "escapes the repository root", annotation: "../../../outside; /../outside; /shared/../../outside", source: readerSource},
		{name: "repository root is never owned", annotation: "/; //; /.; ../..", source: readerSource},
		{name: "source at the repository root", annotation: "config; ./charts/web", source: &argoappv1.ApplicationSource{RepoURL: "https://github.com/example/repo", Path: "."}, want: []string{"config", "charts/web"}},
		{name: "dot at the repository root is never owned", annotation: ".; ./", source: &argoappv1.ApplicationSource{RepoURL: "https://github.com/example/repo", Path: "."}},
		{name: "pathless git source is the repository root", annotation: "config", source: &argoappv1.ApplicationSource{RepoURL: "https://github.com/example/repo"}, want: []string{"config"}},
		{name: "backslashes", annotation: `sub\dir; \shared\config.yaml`, source: readerSource, want: []string{"apps/reader/sub/dir", "shared/config.yaml"}},
		{
			name:       "globs kept verbatim",
			annotation: "/shared/*.yaml; values-?.yaml; [ab]/x",
			source:     readerSource,
			want:       []string{"shared/*.yaml", "apps/reader/values-?.yaml", "apps/reader/[ab]/x"},
		},
		{
			name:       "each source joins relative items to its own path",
			annotation: "config; /shared",
			sources: argoappv1.ApplicationSources{
				{RepoURL: "https://github.com/example/repo", Path: "apps/a"},
				{RepoURL: "https://github.com/example/repo", Path: "apps/b"},
			},
			want: []string{"apps/a/config", "shared", "apps/b/config"},
		},
		{name: "chart-only source keeps only absolute items", annotation: "values.yaml; /values/demo.yaml", source: &argoappv1.ApplicationSource{RepoURL: "https://charts.example.com", Chart: "demo"}, want: []string{"values/demo.yaml"}},
		{
			name:       "ref-only source keeps only absolute items",
			annotation: "values.yaml; /values/demo.yaml",
			sources: argoappv1.ApplicationSources{
				{RepoURL: "https://charts.example.com", Chart: "demo"},
				{RepoURL: "https://github.com/example/repo", Ref: "values"},
			},
			want: []string{"values/demo.yaml"},
		},
		{name: "OCI source owns nothing", annotation: ".; /shared", source: &argoappv1.ApplicationSource{RepoURL: "oci://registry.example.com/manifests", Path: "."}},
		{
			name:       "OCI source beside a local source",
			annotation: ".; /shared",
			sources: argoappv1.ApplicationSources{
				{RepoURL: "oci://registry.example.com/manifests", Path: "."},
				{RepoURL: "https://github.com/example/repo", Path: "apps/a"},
			},
			want: []string{"apps/a", "shared"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := testApplication("reader", tc.source, tc.sources)
			if tc.annotation != "" {
				app.Annotations = map[string]string{argoappv1.AnnotationKeyManifestGeneratePaths: tc.annotation}
			}
			if got := manifestGeneratePaths(app); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("manifestGeneratePaths(%q) = %q, want %q", tc.annotation, got, tc.want)
			}
		})
	}
}

func TestManifestGeneratePathMatches(t *testing.T) {
	for _, tc := range []struct {
		item    string
		changed string
		want    bool
	}{
		{item: "apps/x", changed: "apps/x", want: true},
		{item: "apps/x", changed: "apps/x/cm.yaml", want: true},
		{item: "apps/x", changed: "apps/xy/cm.yaml"},
		{item: "apps/*", changed: "apps/x", want: true},
		// * never crosses a /, as in Argo CD.
		{item: "apps/*", changed: "apps/x/y"},
		{item: "apps/*/values.yaml", changed: "apps/web/values.yaml", want: true},
		{item: "apps/*/values.yaml", changed: "apps/web/nested/values.yaml"},
		{item: "config/*.yaml", changed: "config/a.yaml", want: true},
		{item: "config/*.yaml", changed: "config/a.json"},
		{item: "config/*.yaml", changed: "config/nested/a.yaml"},
		{item: "values-?.yaml", changed: "values-a.yaml", want: true},
		{item: "values-?.yaml", changed: "values-ab.yaml"},
		{item: "[ab]/x", changed: "a/x", want: true},
		{item: "[ab]/x", changed: "c/x"},
		// A bad pattern globs nothing; like Argo CD, it still owns itself.
		{item: "shared/[", changed: "shared/a"},
		{item: "shared/[", changed: "shared/[", want: true},
	} {
		if got := manifestGeneratePathMatches(tc.item, tc.changed); got != tc.want {
			t.Errorf("manifestGeneratePathMatches(%q, %q) = %v, want %v", tc.item, tc.changed, got, tc.want)
		}
	}
}

func TestSelectChangedApplicationInputsUsesManifestGeneratePaths(t *testing.T) {
	reader := testApplication("reader", &argoappv1.ApplicationSource{RepoURL: "https://github.com/example/repo", Path: "apps/reader"}, nil)
	reader.Annotations = map[string]string{argoappv1.AnnotationKeyManifestGeneratePaths: "/shared; /config/*.yaml; /"}
	inputs := []ApplicationSelectionInput{
		{Application: reader},
		{Application: testApplication("other", &argoappv1.ApplicationSource{RepoURL: "https://github.com/example/repo", Path: "apps/other"}, nil)},
	}
	for _, tc := range []struct {
		name        string
		changed     string
		wantApps    []string
		wantUnowned []string
	}{
		{name: "declared directory", changed: "shared/config.yaml", wantApps: []string{"reader"}},
		{name: "declared glob", changed: "config/a.yaml", wantApps: []string{"reader"}},
		{name: "glob does not cross a slash", changed: "config/nested/a.yaml", wantUnowned: []string{"config/nested/a.yaml"}},
		{name: "repository root is not declared", changed: "README.md", wantUnowned: []string{"README.md"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selected, unowned := SelectChangedApplicationInputs(inputs, []string{tc.changed})
			assertApplicationNames(t, selected, tc.wantApps)
			assertStrings(t, unowned, tc.wantUnowned)
		})
	}
}

// TestApplicationInputsByKeyOmitsManifestGeneratePaths pins that declared
// manifest-generate-paths stay selection-only, like the Helm inputs in
// TestApplicationInputsByKeyOmitsHelmSelectionOnlyPaths: the per-Application
// input paths key the persistent render cache as required inputs, and a glob
// or absent declared path there would make the Application and its rendered
// children persistence-ineligible.
func TestApplicationInputsByKeyOmitsManifestGeneratePaths(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "reader.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: reader
  namespace: argocd
  annotations:
    argocd.argoproj.io/manifest-generate-paths: '/shared; /config/*.yaml'
spec:
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: workloads/reader
  destination:
    name: in-cluster
    namespace: reader
`)
	discovered, err := discovery.Scan(root, discovery.Options{})
	if err != nil {
		t.Fatalf("discovery.Scan() error = %v", err)
	}
	if len(discovered.Applications) != 1 || discovered.Applications[0].Application.Annotations[argoappv1.AnnotationKeyManifestGeneratePaths] == "" {
		t.Fatalf("discovered Applications = %#v, want reader with its annotation", discovered.Applications)
	}

	got := map[string][]string{}
	for key, inputs := range applicationInputsByKey(discovered) {
		got[key] = inputs.Paths
	}
	want := map[string][]string{
		applicationDiscoveryKey(argoappv1.Application{Namespace: "argocd", Name: "reader"}): {"apps/reader.yaml", "workloads/reader"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("applicationInputsByKey() = %v, want %v", got, want)
	}
}
