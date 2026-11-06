package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	sourcepkg "github.com/sholdee/drydock/internal/source"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// writeHelmRefPathRepo writes a chart at chart whose ConfigMap prints its
// values, with files whose paths start with "$", and returns the root.
func writeHelmRefPathRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "chart", "Chart.yaml"), "apiVersion: v2\nname: probe\nversion: 0.1.0\n")
	writeTestFile(t, filepath.Join(root, "chart", "values.yaml"), "{}\n")
	writeTestFile(t, filepath.Join(root, "chart", "templates", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: probe\ndata:\n  values: {{ toJson .Values | quote }}\n")
	writeTestFile(t, filepath.Join(root, "chart", "$values", "x.yaml"), "fromEscaped: yes-escaped\n")
	writeTestFile(t, filepath.Join(root, "chart", "$values", "f.txt"), "escaped-file")
	writeTestFile(t, filepath.Join(root, "chart", "demo", "values.yaml"), "fromAppName: yes-app-name\n")
	writeTestFile(t, filepath.Join(root, "chart", "demo", "f.txt"), "app-name-file")
	writeTestFile(t, filepath.Join(root, "values.yaml"), "fromRef: wrong-file\n")
	return root
}

// TestRenderApplicationHelmDollarPathsWithOneSource pins that an
// Application with one source reads a value file or file parameter path
// that starts with "$" as a local path. The Argo CD v3.6.1 repo-server
// checks refs only for an Application with sources (resolveReferencedSources,
// reposerver/repository/repository.go:585-589), so the path is
// env-substituted (Env.Envsubst, pkg/apis/application/v1alpha1/types.go:
// 180-192): "$$" becomes "$", and $ARGOCD_APP_NAME becomes the instance name.
func TestRenderApplicationHelmDollarPathsWithOneSource(t *testing.T) {
	root := writeHelmRefPathRepo(t)
	application := argoappv1.Application{
		Name: "demo", Namespace: "argocd",
		Spec: argoappv1.ApplicationSpec{
			Source: &argoappv1.ApplicationSource{
				RepoURL: "https://git.example.test/repo.git", TargetRevision: "main", Path: "chart",
				Helm: &argoappv1.ApplicationSourceHelm{
					ValueFiles: []string{"$$values/x.yaml", "$ARGOCD_APP_NAME/values.yaml"},
					FileParameters: []argoappv1.HelmFileParameter{
						{Name: "escaped", Path: "$$values/f.txt"},
						{Name: "appName", Path: "$ARGOCD_APP_NAME/f.txt"},
					},
				},
			},
			Destination: argoappv1.ApplicationDestination{Namespace: "default"},
		},
	}
	provider := localProvider{repoRoot: root, sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{})}
	result, err := RenderApplication(context.Background(), application, provider)
	if err != nil {
		t.Fatalf("RenderApplication() error = %v", err)
	}
	got, _, _ := unstructured.NestedString(result.Manifests[0].Object.Object, "data", "values")
	want := `{"appName":"app-name-file","escaped":"escaped-file","fromAppName":"yes-app-name","fromEscaped":"yes-escaped"}`
	if got != want {
		t.Fatalf("values = %s, want %s", got, want)
	}
}

// TestRenderApplicationHelmUnknownRefWithSources pins the repo-server's
// error for an Application with sources whose value file or file parameter
// path starts with a "$" segment that names no source
// (resolveReferencedSources, reposerver/repository/repository.go:597-611 at
// v3.6.1). "$$values" and "$ARGOCD_APP_NAME" are such segments there.
func TestRenderApplicationHelmUnknownRefWithSources(t *testing.T) {
	for _, tt := range []struct {
		name       string
		withRef    bool
		valueFiles []string
		parameters []argoappv1.HelmFileParameter
		want       string
	}{
		{name: "escaped dollar value file", withRef: true, valueFiles: []string{"$$values/x.yaml"}, want: `source referenced "$$values", which is not one of the available sources ($values)`},
		{name: "env file parameter", withRef: true, parameters: []argoappv1.HelmFileParameter{{Name: "f", Path: "$ARGOCD_APP_NAME/f.txt"}}, want: `source referenced "$ARGOCD_APP_NAME", which is not one of the available sources ($values)`},
		{name: "no ref source", valueFiles: []string{"$values/x.yaml"}, want: `source referenced "$values", but no source has a 'ref' field defined`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := writeHelmRefPathRepo(t)
			sources := argoappv1.ApplicationSources{{
				RepoURL: "https://git.example.test/repo.git", TargetRevision: "main", Path: "chart",
				Helm: &argoappv1.ApplicationSourceHelm{ValueFiles: tt.valueFiles, FileParameters: tt.parameters},
			}}
			if tt.withRef {
				sources = append(sources, argoappv1.ApplicationSource{RepoURL: "https://git.example.test/repo.git", TargetRevision: "main", Ref: "values"})
			}
			application := argoappv1.Application{
				Name: "demo", Namespace: "argocd",
				Spec: argoappv1.ApplicationSpec{Sources: sources, Destination: argoappv1.ApplicationDestination{Namespace: "default"}},
			}
			provider := localProvider{repoRoot: root, sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{})}
			_, err := RenderApplication(context.Background(), application, provider)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("RenderApplication() error = %v, want %q", err, tt.want)
			}
		})
	}
}
