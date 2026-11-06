package app

import (
	"context"
	"path/filepath"
	"testing"
)

// The discovery frontier renders a parent Application with the tracking
// settings of the repository, as the Argo CD controller does. So under label
// tracking, a child Application found in that render carries the instance
// label of its parent and no tracking-id annotation, and -l selects on it.
func TestRenderedChildApplicationCarriesRepositoryTracking(t *testing.T) {
	for _, tc := range []struct {
		name           string
		settings       string
		wantLabel      string
		wantAnnotation string
	}{
		{name: "label tracking", settings: "  application.resourceTrackingMethod: label\n", wantLabel: "root"},
		{name: "annotation tracking", settings: "  application.resourceTrackingMethod: annotation\n", wantAnnotation: "root:argoproj.io/Application:argocd/child"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, filepath.Join(root, "settings", "argocd-cm.yaml"), argocdCMYAML(tc.settings))
			writeTestFile(t, filepath.Join(root, "apps", "root.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: root
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: bootstrap-chart
  destination:
    name: in-cluster
    namespace: argocd
`)
			writeTestFile(t, filepath.Join(root, "bootstrap-chart", "Chart.yaml"), "apiVersion: v2\nname: bootstrap\nversion: 0.1.0\n")
			writeTestFile(t, filepath.Join(root, "bootstrap-chart", "templates", "child.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: child
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: workloads/child
  destination:
    name: in-cluster
    namespace: child
`)

			result, err := Orchestrator{}.ListApplications(context.Background(), BuildRequest{Path: root, Parallelism: 1})
			if err != nil {
				t.Fatalf("ListApplications() error = %v", err)
			}
			for _, application := range result.Applications {
				if application.Name != "child" {
					continue
				}
				if got := application.Labels["app.kubernetes.io/instance"]; got != tc.wantLabel {
					t.Fatalf("child label app.kubernetes.io/instance = %q, want %q (labels %v)", got, tc.wantLabel, application.Labels)
				}
				if got := application.Annotations["argocd.argoproj.io/tracking-id"]; got != tc.wantAnnotation {
					t.Fatalf("child tracking-id annotation = %q, want %q (annotations %v)", got, tc.wantAnnotation, application.Annotations)
				}
				return
			}
			t.Fatalf("Applications = %v, want the rendered child", applicationNames(result.Applications))
		})
	}
}
