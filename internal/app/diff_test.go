package app

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/home-operations/argocd-local/internal/diagnostic"
)

func TestOrchestratorDiffAppsReportsManifestChange(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeSimpleApp(t, left, "old")
	writeSimpleApp(t, right, "new")

	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		LeftPath:  left,
		RightPath: right,
		Unified:   3,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v", err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("len(Results) = %d, want 1", len(result.Results))
	}
	if result.Results[0].Change != "modified" {
		t.Fatalf("Change = %s, want modified", result.Results[0].Change)
	}
}

func TestOrchestratorDiffImagesReportsImageChange(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeImageApp(t, left, "example/app:v1")
	writeImageApp(t, right, "example/app:v2")

	result, err := Orchestrator{}.DiffImages(context.Background(), DiffRequest{
		LeftPath:  left,
		RightPath: right,
	})
	if err != nil {
		t.Fatalf("DiffImages() error = %v", err)
	}
	if !reflect.DeepEqual(result.Added, []string{"example/app:v2"}) {
		t.Fatalf("Added = %#v, want example/app:v2", result.Added)
	}
	if !reflect.DeepEqual(result.Removed, []string{"example/app:v1"}) {
		t.Fatalf("Removed = %#v, want example/app:v1", result.Removed)
	}
}

func TestOrchestratorDiffImagesReportsUnchangedImage(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeImageApp(t, left, "example/app:v1")
	writeImageApp(t, right, "example/app:v1")

	result, err := Orchestrator{}.DiffImages(context.Background(), DiffRequest{
		LeftPath:  left,
		RightPath: right,
	})
	if err != nil {
		t.Fatalf("DiffImages() error = %v", err)
	}
	if len(result.Added) != 0 {
		t.Fatalf("Added = %#v, want empty", result.Added)
	}
	if len(result.Removed) != 0 {
		t.Fatalf("Removed = %#v, want empty", result.Removed)
	}
	if !reflect.DeepEqual(result.Unchanged, []string{"example/app:v1"}) {
		t.Fatalf("Unchanged = %#v, want example/app:v1", result.Unchanged)
	}
}

func TestDiffAppsRejectsRemoteCacheInsideEitherRoot(t *testing.T) {
	left := t.TempDir()
	right := t.TempDir()

	_, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		LeftPath:               left,
		RightPath:              right,
		RemoteResourceCacheDir: filepath.Join(right, ".argocd-local", "remotes"),
	})
	if err == nil {
		t.Fatal("DiffApps() error = nil, want cache containment error")
	}
	if !strings.Contains(err.Error(), "must not be inside repository root") {
		t.Fatalf("DiffApps() error = %v, want cache containment error", err)
	}
}

func TestOrchestratorDiffAppsChangedOnlyFallsBackOnUnownedCurrentPath(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeSimpleApp(t, left, "old")
	writeSimpleApp(t, right, "new")
	writeTestFile(t, filepath.Join(left, "README.md"), "left\n")
	writeTestFile(t, filepath.Join(right, "README.md"), "right\n")

	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		LeftPath:    left,
		RightPath:   right,
		ChangedOnly: true,
		Unified:     3,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v", err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("len(Results) = %d, want 1", len(result.Results))
	}
	if len(result.Diagnostics) != 1 {
		t.Fatalf("len(Diagnostics) = %d, want 1: %#v", len(result.Diagnostics), result.Diagnostics)
	}
	diag := result.Diagnostics[0]
	if diag.Severity != diagnostic.SeverityWarning {
		t.Fatalf("diagnostic severity = %s, want warning", diag.Severity)
	}
	if diag.Category != "changed-only" || !strings.Contains(diag.Message, "README.md") {
		t.Fatalf("diagnostic = %#v, want changed-only warning for README.md", diag)
	}

	strictResult, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		LeftPath:          left,
		RightPath:         right,
		ChangedOnly:       true,
		StrictChangedOnly: true,
		Unified:           3,
	})
	if err == nil {
		t.Fatal("DiffApps() error = nil, want strict changed-only error")
	}
	if len(strictResult.Diagnostics) != 1 {
		t.Fatalf("len(Diagnostics) = %d, want 1: %#v", len(strictResult.Diagnostics), strictResult.Diagnostics)
	}
	if strictResult.Diagnostics[0].Severity != diagnostic.SeverityError {
		t.Fatalf("strict diagnostic severity = %s, want error", strictResult.Diagnostics[0].Severity)
	}
}

func TestOrchestratorDiffAppsStrictChangedOnlyOwnsApplicationManifestChanges(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeSimpleAppWithDestination(t, left, "old")
	writeSimpleAppWithDestination(t, right, "new")

	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		LeftPath:          left,
		RightPath:         right,
		ChangedOnly:       true,
		StrictChangedOnly: true,
		Unified:           3,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v", err)
	}
	if len(result.Diagnostics) != 0 {
		t.Fatalf("len(Diagnostics) = %d, want 0: %#v", len(result.Diagnostics), result.Diagnostics)
	}
	if len(result.Results) == 0 {
		t.Fatal("len(Results) = 0, want rendered diff from Application manifest change")
	}
	var sawNewNamespace bool
	for _, item := range result.Results {
		if item.Resource.Namespace == "new" {
			sawNewNamespace = true
		}
	}
	if !sawNewNamespace {
		t.Fatalf("Results = %#v, want diff for new namespace", result.Results)
	}
}

func TestOrchestratorDiffAppsStrictChangedOnlyOwnsDeletedApplicationManifestFromLeft(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeSimpleApp(t, left, "old")
	writeTestFile(t, filepath.Join(right, "manifests", "demo", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
data:
  value: old
`)

	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		LeftPath:          left,
		RightPath:         right,
		ChangedOnly:       true,
		StrictChangedOnly: true,
		Unified:           3,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v", err)
	}
	if len(result.Diagnostics) != 0 {
		t.Fatalf("len(Diagnostics) = %d, want 0: %#v", len(result.Diagnostics), result.Diagnostics)
	}
	if len(result.Results) != 1 {
		t.Fatalf("len(Results) = %d, want 1", len(result.Results))
	}
	if result.Results[0].Change != "removed" {
		t.Fatalf("Change = %s, want removed", result.Results[0].Change)
	}
}

func TestOrchestratorDiffAppsStrictChangedOnlyOwnsSameRepoRefHelmValueFile(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeHelmAppWithRefValues(t, left, "old")
	writeHelmAppWithRefValues(t, right, "new")

	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		LeftPath:          left,
		RightPath:         right,
		ChangedOnly:       true,
		StrictChangedOnly: true,
		Unified:           3,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v", err)
	}
	if len(result.Diagnostics) != 0 {
		t.Fatalf("len(Diagnostics) = %d, want 0: %#v", len(result.Diagnostics), result.Diagnostics)
	}
	if len(result.Results) != 1 {
		t.Fatalf("len(Results) = %d, want 1", len(result.Results))
	}
	for _, want := range []string{"-  value: old", "+  value: new"} {
		if !strings.Contains(result.Results[0].Diff, want) {
			t.Fatalf("Diff = %q, want %q", result.Results[0].Diff, want)
		}
	}
}

func TestOrchestratorDiffAppReportsOnlyNamedApplicationChange(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeDiffApplication(t, left, "demo", "demo", "old")
	writeDiffApplication(t, left, "other", "other", "same")
	writeDiffApplication(t, right, "demo", "demo", "new")
	writeDiffApplication(t, right, "other", "other", "changed-but-not-selected")

	result, err := Orchestrator{}.DiffApp(context.Background(), DiffAppRequest{
		Name: "demo",
		DiffRequest: DiffRequest{
			LeftPath:    left,
			RightPath:   right,
			Unified:     3,
			ChangedOnly: true,
		},
	})
	if err != nil {
		t.Fatalf("DiffApp() error = %v", err)
	}
	if len(result.Results) == 0 {
		t.Fatal("len(Results) = 0, want diff")
	}
	got := result.Results[0].Diff
	for _, want := range []string{"Application: argocd/demo", "-  value: old", "+  value: new"} {
		if !strings.Contains(got, want) {
			t.Fatalf("diff missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "other") || strings.Contains(got, "changed-but-not-selected") {
		t.Fatalf("diff included non-selected Application:\n%s", got)
	}
	if len(result.Diagnostics) != 0 {
		t.Fatalf("len(Diagnostics) = %d, want 0: %#v", len(result.Diagnostics), result.Diagnostics)
	}
}

func TestOrchestratorDiffAppShowsAddedApplication(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeTestFile(t, filepath.Join(left, ".keep"), "left\n")
	writeDiffApplication(t, right, "demo", "demo", "new")

	result, err := Orchestrator{}.DiffApp(context.Background(), DiffAppRequest{
		Name:        "demo",
		DiffRequest: DiffRequest{LeftPath: left, RightPath: right},
	})
	if err != nil {
		t.Fatalf("DiffApp() error = %v", err)
	}
	if len(result.Results) == 0 || !strings.Contains(result.Results[0].Diff, "+  value: new") {
		t.Fatalf("DiffApp() result = %#v, want added manifest diff", result.Results)
	}
}

func TestOrchestratorDiffAppShowsDeletedApplication(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeDiffApplication(t, left, "demo", "demo", "old")
	writeTestFile(t, filepath.Join(right, ".keep"), "right\n")

	result, err := Orchestrator{}.DiffApp(context.Background(), DiffAppRequest{
		Name:        "demo",
		DiffRequest: DiffRequest{LeftPath: left, RightPath: right},
	})
	if err != nil {
		t.Fatalf("DiffApp() error = %v", err)
	}
	if len(result.Results) == 0 || !strings.Contains(result.Results[0].Diff, "-  value: old") {
		t.Fatalf("DiffApp() result = %#v, want deleted manifest diff", result.Results)
	}
}

func TestOrchestratorDiffAppReportsMissingBothSides(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeDiffApplication(t, left, "other", "other", "left")
	writeDiffApplication(t, right, "other", "other", "right")

	_, err := Orchestrator{}.DiffApp(context.Background(), DiffAppRequest{
		Name:        "missing",
		DiffRequest: DiffRequest{LeftPath: left, RightPath: right},
	})
	if err == nil {
		t.Fatal("DiffApp() error = nil, want missing error")
	}
	if !strings.Contains(err.Error(), `application "missing" not found in either tree`) {
		t.Fatalf("DiffApp() error = %v, want missing both sides message", err)
	}
}

func TestDiffAppRejectsRemoteCacheInsideEitherRoot(t *testing.T) {
	left := t.TempDir()
	right := t.TempDir()

	for _, root := range []string{left, right} {
		_, err := Orchestrator{}.DiffApp(context.Background(), DiffAppRequest{
			Name: "demo",
			DiffRequest: DiffRequest{
				LeftPath:               left,
				RightPath:              right,
				RemoteResourceCacheDir: filepath.Join(root, ".argocd-local", "remotes"),
			},
		})
		if err == nil {
			t.Fatal("DiffApp() error = nil, want cache containment error")
		}
		if !strings.Contains(err.Error(), "must not be inside repository root") {
			t.Fatalf("DiffApp() error = %v, want cache containment error", err)
		}
	}
}

func writeSimpleApp(t *testing.T, root, value string) {
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
	writeTestFile(t, filepath.Join(root, "manifests", "demo", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
data:
  value: `+value+`
`)
}

func writeSimpleAppWithDestination(t *testing.T, root, namespace string) {
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
    namespace: `+namespace+`
`)
	writeTestFile(t, filepath.Join(root, "manifests", "demo", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
data:
  value: same
`)
}

func writeImageApp(t *testing.T, root, image string) {
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
	writeTestFile(t, filepath.Join(root, "manifests", "demo", "deployment.yaml"), `apiVersion: apps/v1
kind: Deployment
metadata:
  name: demo
spec:
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
}

func writeHelmAppWithRefValues(t *testing.T, root, value string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", "demo.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo
  namespace: argocd
spec:
  sources:
    - repoURL: https://github.com/example/repo
      targetRevision: main
      ref: values
    - repoURL: https://github.com/example/repo.git
      targetRevision: main
      path: charts/demo
      helm:
        valueFiles:
          - $values/values/demo.yaml
  destination:
    name: in-cluster
    namespace: demo
`)
	writeTestFile(t, filepath.Join(root, "charts", "demo", "Chart.yaml"), `apiVersion: v2
name: demo
version: 0.1.0
`)
	writeTestFile(t, filepath.Join(root, "charts", "demo", "templates", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
data:
  value: {{ .Values.value | quote }}
`)
	writeTestFile(t, filepath.Join(root, "values", "demo.yaml"), `value: `+value+`
`)
}

func writeDiffApplication(t *testing.T, root, appName, configMapName, value string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", appName+".yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: `+appName+`
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    path: manifests/`+appName+`
    targetRevision: main
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "manifests", appName, "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: `+configMapName+`
data:
  value: `+value+`
`)
}
