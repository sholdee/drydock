package render

import (
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
)

// helmTemplateName matches action.TemplateName. The table is
// TestNameTemplate of helm v4.3.1 (pkg/action/install_test.go:765-818):
// importing pkg/action would add modules to go.mod.
func TestHelmTemplateNameMatchesHelm(t *testing.T) {
	for _, tc := range []struct {
		tpl, expected, expectedErrorStr string
	}{
		{tpl: "foobar", expected: "foobar"},
		{tpl: "foobar-{{randNumeric 6}}", expected: "foobar-[0-9]{6}$"},
		{tpl: "foobar-{{randNumeric 4}}-baz", expected: "foobar-[0-9]{4}-baz$"},
		{tpl: "foobar-{{randInteger}}", expectedErrorStr: "function \"randInteger\" not defined"},
		{tpl: "foobar-{{", expectedErrorStr: "template: name-template:1: unclosed action"},
	} {
		t.Run(tc.tpl, func(t *testing.T) {
			name, err := helmTemplateName(tc.tpl)
			if tc.expectedErrorStr == "" {
				if err != nil {
					t.Fatalf("helmTemplateName() error = %v", err)
				}
			} else if err == nil || !regexp.MustCompile(tc.expectedErrorStr).MatchString(err.Error()) {
				t.Fatalf("helmTemplateName() error = %v, want %q", err, tc.expectedErrorStr)
			}
			if tc.expected != "" && !regexp.MustCompile(tc.expected).MatchString(name) {
				t.Fatalf("helmTemplateName() = %q, want %q", name, tc.expected)
			}
		})
	}
}

// With valuesInline and valuesMerge replace, kustomize never loads
// valuesFile, so a valuesFile that does not exist, with or without glob
// characters, does not fail.
func TestKustomizeHelmChartsReplaceModeIgnoresValuesFile(t *testing.T) {
	for _, valuesFile := range []string{"'values-*.yaml'", "nope.yaml"} {
		kustomization := "helmCharts:\n  - name: probe\n    releaseName: probe\n    valuesInline:\n      a: b\n    valuesMerge: replace\n    valuesFile: " + valuesFile + "\n"
		if _, err := renderReleaseProbeKustomize(t, kustomization, RenderOptions{}, map[string]string{"values-a.yaml": "x: 1\n"}); err != nil {
			t.Fatalf("Render(valuesFile %s) error = %v", valuesFile, err)
		}
	}
}

// kustomize configures every helmCharts entry of a kustomization before it
// renders any, so the argument checks of a later entry come before the
// chart and values errors of an earlier one.
func TestKustomizeHelmChartsCheckEveryEntryFirst(t *testing.T) {
	for _, tc := range []struct {
		name, kustomization, want string
	}{
		{
			name:          "releaseName of a later entry",
			kustomization: "helmCharts:\n  - name: probe\n    releaseName: ok\n    valuesFile: missing.yaml\n  - name: probe\n    releaseName: -bad\n",
			want:          `releaseName must not start with '-', got "-bad"`,
		},
		{
			name:          "additionalValuesFiles of a later entry",
			kustomization: "helmCharts:\n  - name: missing-chart\n    releaseName: first\n  - name: probe\n    releaseName: second\n    additionalValuesFiles:\n      - nope.yaml\n",
			want:          `could not load additionalValuesFile: read helm value file "nope.yaml"`,
		},
		{
			name:          "additionalValuesFiles before the chart of the same entry",
			kustomization: "helmCharts:\n  - name: missing-chart\n    releaseName: probe\n    additionalValuesFiles:\n      - nope.yaml\n",
			want:          "could not load additionalValuesFile",
		},
		{
			name:          "valuesMerge",
			kustomization: "helmCharts:\n  - name: missing-chart\n    releaseName: probe\n  - name: probe\n    releaseName: probe\n    valuesMerge: append\n",
			want:          "valuesMerge must be one of [merge override replace]",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := renderReleaseProbeKustomize(t, tc.kustomization, RenderOptions{}, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Render() error = %v, want %q", err, tc.want)
			}
		})
	}
}

// Without a namespace, helm uses the namespace of the repo-server
// environment, which the rendered install decides (HelmDefaultNamespace).
func TestHelmRendererUsesRepoServerNamespace(t *testing.T) {
	opts := RenderOptions{AppName: "demo", HelmDefaultNamespace: "argocd"}
	manifests, err := renderReleaseProbeHelm(t, "", opts)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	assertConfigMapData(t, manifests, "namespace", "argocd")
	manifests, err = renderReleaseProbeKustomize(t, "helmCharts:\n  - name: probe\n", RenderOptions{HelmDefaultNamespace: "argocd"}, nil)
	if err != nil {
		t.Fatalf("Kustomize Render() error = %v", err)
	}
	assertConfigMapData(t, manifests, "namespace", "argocd")
	opts.Namespace = "explicit"
	manifests, err = renderReleaseProbeHelm(t, "", opts)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	assertConfigMapData(t, manifests, "namespace", "explicit")
}

// Without an explicit kube version, drydock does not know the destination
// cluster version that Argo CD checks, so it skips the check.
func TestHelmRendererSkipsKubeVersionCheckWithoutExplicitVersion(t *testing.T) {
	if _, err := renderReleaseProbeHelm(t, "kubeVersion: '>=99.0.0-0'\n", RenderOptions{AppName: "demo"}); err != nil {
		t.Fatalf("Render() without a kube version error = %v, want the check skipped", err)
	}
}

// A tool block error waits for the renderer of that tool.
func TestRenderersReturnToolSpecErrors(t *testing.T) {
	_, err := renderReleaseProbeHelm(t, "", RenderOptions{AppName: "demo", HelmSpecError: errFakeSpec})
	if !errors.Is(err, errFakeSpec) {
		t.Fatalf("Helm Render() error = %v, want the spec error", err)
	}
	_, err = renderReleaseProbeKustomize(t, "helmCharts:\n  - name: probe\n", RenderOptions{KustomizeSpecError: errFakeSpec}, nil)
	if !errors.Is(err, errFakeSpec) {
		t.Fatalf("Kustomize Render() error = %v, want the spec error", err)
	}
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a\n")
	if _, _, err := (DirectoryRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: root, Path: "app"}, RenderOptions{HelmSpecError: errFakeSpec, KustomizeSpecError: errFakeSpec}); err != nil {
		t.Fatalf("Directory Render() error = %v, want the tool errors ignored", err)
	}
}

var errFakeSpec = &fakeSpecError{}

type fakeSpecError struct{}

func (*fakeSpecError) Error() string { return "fake spec error" }

// With Jsonnet off, a directory source reads no Jsonnet library or import,
// so they are not cache inputs, also when a library is outside the
// repository.
func TestDirectoryInputDigestPathsWithJsonnetOff(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a\n")
	writeFile(t, filepath.Join(root, "app", "broken.jsonnet"), "{ this is not jsonnet")
	opts := RenderOptions{
		DirectoryJsonnetDisabled: true,
		Jsonnet:                  argoappv1.ApplicationSourceJsonnet{Libs: []string{"../../../../etc"}},
	}
	manifests, _, err := (DirectoryRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: root, Path: "app"}, opts)
	if err != nil || len(manifests) != 1 {
		t.Fatalf("Render() = %d manifests, %v, want the ConfigMap only", len(manifests), err)
	}
	paths, err := DirectoryInputDigestPaths(context.Background(), ResolvedSource{RepoRoot: root, Path: "app"}, opts)
	if err != nil {
		t.Fatalf("DirectoryInputDigestPaths() error = %v", err)
	}
	names := make([]string, 0, len(paths))
	for _, item := range paths {
		names = append(names, item.Path)
	}
	if !slices.Equal(names, []string{"app"}) {
		t.Fatalf("DirectoryInputDigestPaths() = %v, want only the source directory", names)
	}
	opts.DirectoryJsonnetDisabled = false
	if _, err := DirectoryInputDigestPaths(context.Background(), ResolvedSource{RepoRoot: root, Path: "app"}, opts); err == nil {
		t.Fatal("DirectoryInputDigestPaths() with Jsonnet on error = nil, want the library outside the repository to fail")
	}
}

// kustomize loads helmCharts values files with its file loader. Under the
// default LoadRestrictionsRootOnly a file outside the kustomization
// directory fails, and a directory fails, with the texts of kustomize 5.8.3.
func TestKustomizeHelmChartsValuesFilesFollowTheLoadRestrictor(t *testing.T) {
	files := map[string]string{"../shared.yaml": "x: 1\n", "vals/keep.yaml": "x: 1\n"}
	for _, tc := range []struct {
		name, chart, want string
	}{
		{name: "additionalValuesFiles outside the root", chart: "    additionalValuesFiles:\n      - ../shared.yaml\n", want: "could not load additionalValuesFile: security; file 'shared.yaml' is not in or below 'app'"},
		{name: "valuesFile outside the root", chart: "    valuesFile: ../shared.yaml\n", want: "security; file 'shared.yaml' is not in or below 'app'"},
		{name: "valuesFile outside the root with valuesInline", chart: "    valuesFile: ../shared.yaml\n    valuesInline:\n      a: b\n", want: "security; file 'shared.yaml' is not in or below 'app'"},
		{name: "additionalValuesFiles directory", chart: "    additionalValuesFiles:\n      - vals\n", want: "could not load additionalValuesFile: 'app/vals' must resolve to a file"},
		{name: "valuesFile directory", chart: "    valuesFile: vals\n", want: "'app/vals' must resolve to a file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kustomization := "helmCharts:\n  - name: probe\n    releaseName: probe\n" + tc.chart
			_, err := renderReleaseProbeKustomize(t, kustomization, RenderOptions{}, files)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Render() error = %v, want %q", err, tc.want)
			}
		})
	}
	// LoadRestrictionsNone lets kustomize load a file outside the root.
	for _, chart := range []string{"    additionalValuesFiles:\n      - ../shared.yaml\n", "    valuesFile: ../shared.yaml\n"} {
		kustomization := "helmCharts:\n  - name: probe\n    releaseName: probe\n" + chart
		if _, err := renderReleaseProbeKustomize(t, kustomization, RenderOptions{BuildOptions: []string{"--load-restrictor", "LoadRestrictionsNone"}}, files); err != nil {
			t.Fatalf("Render(%q) with LoadRestrictionsNone error = %v", chart, err)
		}
	}
}

// kustomize accumulates the resources of a kustomization before it runs
// its generators, so a missing resource is reported before a helmCharts
// error of the same kustomization.
func TestKustomizeMissingResourceComesBeforeHelmCharts(t *testing.T) {
	kustomization := "resources:\n  - missing.yaml\nhelmCharts:\n  - name: probe\n    releaseName: probe\n    additionalValuesFiles:\n      - nope.yaml\n"
	_, err := renderReleaseProbeKustomize(t, kustomization, RenderOptions{}, nil)
	want := "accumulating resources: accumulation err='accumulating resources from 'missing.yaml'"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Render() error = %v, want %q", err, want)
	}
}

// A helm block error is the Helm renderer's first error.
func TestHelmRendererReturnsSpecErrorFirst(t *testing.T) {
	root := t.TempDir()
	writeReleaseProbeChart(t, filepath.Join(root, "app"), "")
	_, _, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: root, Path: "app"}, RenderOptions{AppName: "x", HelmSpecError: errFakeSpec, ReleaseName: "Bad_Name"})
	if !errors.Is(err, errFakeSpec) {
		t.Fatalf("Render() error = %v, want the spec error first", err)
	}
}
