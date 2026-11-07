package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/argoproj/argo-cd/v3/util/app/discovery"
	"github.com/sholdee/drydock/internal/render"
)

// TestDiscoverLocalSourceTypeMatchesUpstream runs the Argo CD v3.6.1
// repo-server source type detection (discovery.AppType) on the same
// directories as discoverLocalSourceType. The plugin socket directory is
// empty, so the upstream config management plugin check finds nothing, as
// for a source that no plugin claims.
func TestDiscoverLocalSourceTypeMatchesUpstream(t *testing.T) {
	t.Setenv("ARGOCD_PLUGINSOCKFILEPATH", t.TempDir())
	type entry struct {
		name    string
		dir     bool
		symlink string
	}
	testCases := []struct {
		name    string
		entries []entry
		want    argoappv1.ApplicationSourceType
	}{
		{name: "empty directory", want: argoappv1.ApplicationSourceTypeDirectory},
		{name: "plain manifests", entries: []entry{{name: "configmap.yaml"}}, want: argoappv1.ApplicationSourceTypeDirectory},
		{name: "Chart.yaml", entries: []entry{{name: "Chart.yaml"}}, want: argoappv1.ApplicationSourceTypeHelm},
		{name: "kustomization.yaml", entries: []entry{{name: "kustomization.yaml"}}, want: argoappv1.ApplicationSourceTypeKustomize},
		{name: "kustomization.yml", entries: []entry{{name: "kustomization.yml"}}, want: argoappv1.ApplicationSourceTypeKustomize},
		{name: "Kustomization", entries: []entry{{name: "Kustomization"}}, want: argoappv1.ApplicationSourceTypeKustomize},
		// "k" sorts after "C", so the kustomization file is the last match.
		{name: "Chart.yaml before kustomization.yaml", entries: []entry{{name: "Chart.yaml"}, {name: "kustomization.yaml"}}, want: argoappv1.ApplicationSourceTypeKustomize},
		// Any name that ends in Chart.yaml is a chart, and "t" sorts after "k".
		{name: "HelmChart after kustomization.yaml", entries: []entry{{name: "kustomization.yaml"}, {name: "traefik-HelmChart.yaml"}, {name: "configmap.yaml"}}, want: argoappv1.ApplicationSourceTypeHelm},
		{name: "HelmChart before kustomization.yaml", entries: []entry{{name: "kustomization.yaml"}, {name: "a-HelmChart.yaml"}}, want: argoappv1.ApplicationSourceTypeKustomize},
		{name: "HelmChart after Kustomization", entries: []entry{{name: "Kustomization"}, {name: "a-HelmChart.yaml"}}, want: argoappv1.ApplicationSourceTypeHelm},
		{name: "HelmChart only", entries: []entry{{name: "traefik-HelmChart.yaml"}}, want: argoappv1.ApplicationSourceTypeHelm},
		{name: "suffix is case sensitive", entries: []entry{{name: "chart.yaml"}, {name: "values-chart.yaml"}}, want: argoappv1.ApplicationSourceTypeDirectory},
		{name: "yml is not a chart", entries: []entry{{name: "Chart.yml"}}, want: argoappv1.ApplicationSourceTypeDirectory},
		{name: "directory named Chart.yaml", entries: []entry{{name: "Chart.yaml", dir: true}}, want: argoappv1.ApplicationSourceTypeDirectory},
		{name: "directory named kustomization.yaml", entries: []entry{{name: "Chart.yaml"}, {name: "kustomization.yaml", dir: true}}, want: argoappv1.ApplicationSourceTypeHelm},
		{name: "chart in a subdirectory", entries: []entry{{name: "sub", dir: true}, {name: "sub/Chart.yaml"}}, want: argoappv1.ApplicationSourceTypeDirectory},
		{name: "subdirectory does not reset the root type", entries: []entry{{name: "Chart.yaml"}, {name: "sub", dir: true}, {name: "sub/kustomization.yaml"}}, want: argoappv1.ApplicationSourceTypeHelm},
		{name: "symlink named Chart.yaml", entries: []entry{{name: "target.yaml"}, {name: "Chart.yaml", symlink: "target.yaml"}}, want: argoappv1.ApplicationSourceTypeHelm},
		{name: "symlink to a directory named kustomization.yaml", entries: []entry{{name: "target", dir: true}, {name: "kustomization.yaml", symlink: "target"}}, want: argoappv1.ApplicationSourceTypeKustomize},
		{name: "dangling symlink named Chart.yaml", entries: []entry{{name: "Chart.yaml", symlink: "missing"}}, want: argoappv1.ApplicationSourceTypeHelm},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			for _, item := range testCase.entries {
				target := filepath.Join(root, filepath.FromSlash(item.name))
				var err error
				switch {
				case item.dir:
					err = os.MkdirAll(target, 0o755)
				case item.symlink != "":
					err = os.Symlink(item.symlink, target)
				default:
					err = os.WriteFile(target, []byte("{}\n"), 0o644)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			upstream, err := discovery.AppType(context.Background(), root, root, nil, nil, nil)
			if err != nil {
				t.Fatalf("discovery.AppType() error = %v", err)
			}
			if upstream != string(testCase.want) {
				t.Fatalf("discovery.AppType() = %q, want %q; the test case is wrong", upstream, testCase.want)
			}
			got, err := discoverLocalSourceType(root, sourceTypeGates{})
			if err != nil {
				t.Fatalf("discoverLocalSourceType() error = %v", err)
			}
			if got == "" {
				got = argoappv1.ApplicationSourceTypeDirectory
			}
			if got != testCase.want {
				t.Fatalf("discoverLocalSourceType() = %q, upstream %q", got, testCase.want)
			}
		})
	}
}

// TestSelectLocalRendererPicksHelmForHelmChartFile pins the renderer for a
// Kustomize directory that also holds a k3s HelmChart manifest named
// traefik-HelmChart.yaml. The name ends in Chart.yaml and sorts after
// kustomization.yaml, so Argo CD renders the directory as a Helm chart.
func TestSelectLocalRendererPicksHelmForHelmChartFile(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "apps", "traefik")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"kustomization.yaml", "traefik-HelmChart.yaml"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	renderer, err := selectLocalRenderer(render.ResolvedSource{RepoRoot: root, Path: "apps/traefik"}, sourceTypeGates{})
	if err != nil {
		t.Fatalf("selectLocalRenderer() error = %v", err)
	}
	if _, ok := renderer.(render.HelmRenderer); !ok {
		t.Fatalf("selectLocalRenderer() = %T, want render.HelmRenderer", renderer)
	}
	if got := settingsInstallSourceType(SourcePlan{}, root, "apps/traefik", true); got != argoappv1.ApplicationSourceTypeHelm {
		t.Fatalf("settingsInstallSourceType() = %q, want Helm", got)
	}
}
