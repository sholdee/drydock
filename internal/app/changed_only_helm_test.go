package app

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// writeHelmSiblingKustomizeApps writes web-prod, a Helm Application over
// charts/web whose helm block reads an input from envs/prod, outside its
// spec.source.path, and cluster-prod, a Kustomize Application whose overlay
// includes envs/prod as a base. The sibling's graph owning envs/prod must not
// leave web-prod unselected. An unrelated Application proves selection stays
// narrow.
func writeHelmSiblingKustomizeApps(t *testing.T, root, helm string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", "web-prod.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: web-prod
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: charts/web
    helm:
`+helm+`
  destination:
    name: in-cluster
    namespace: web
`)
	writeTestFile(t, filepath.Join(root, "charts", "web", "Chart.yaml"), `apiVersion: v2
name: web
version: 0.1.0
`)
	writeTestFile(t, filepath.Join(root, "charts", "web", "templates", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: web
data:
  value: {{ .Values.value | quote }}
`)
	writeTestFile(t, filepath.Join(root, "apps", "cluster-prod.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: cluster-prod
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: clusters/prod
  destination:
    name: in-cluster
    namespace: cluster
`)
	writeTestFile(t, filepath.Join(root, "clusters", "prod", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ../../envs/prod
`)
	writeTestFile(t, filepath.Join(root, "envs", "prod", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - configmap.yaml
`)
	writeTestFile(t, filepath.Join(root, "envs", "prod", "configmap.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: cluster
data:
  value: same
`)
	writeDiffApplication(t, root, "other", "other", "same")
}

// writeHelmEscapedValueFileApps reads the value file through ../../, which
// resolves under the repository root but outside spec.source.path.
func writeHelmEscapedValueFileApps(t *testing.T, root, value string) {
	t.Helper()
	writeHelmSiblingKustomizeApps(t, root, `      valueFiles:
        - ../../envs/prod/web-values.yaml`)
	writeTestFile(t, filepath.Join(root, "envs", "prod", "web-values.yaml"), "value: "+value+"\n")
}

// writeHelmEscapedFileParameterApps reads the same kind of escaped input
// through helm.fileParameters.
func writeHelmEscapedFileParameterApps(t *testing.T, root, value string) {
	t.Helper()
	writeHelmSiblingKustomizeApps(t, root, `      fileParameters:
        - name: value
          path: ../../envs/prod/web-value.txt`)
	writeTestFile(t, filepath.Join(root, "envs", "prod", "web-value.txt"), value)
}

// writeHelmValueFileGlobApps reads the value file through a glob, which owns
// its static directory.
func writeHelmValueFileGlobApps(t *testing.T, root, value string) {
	t.Helper()
	writeHelmSiblingKustomizeApps(t, root, `      valueFiles:
        - ../../envs/prod/web-*.yaml`)
	writeTestFile(t, filepath.Join(root, "envs", "prod", "web-values.yaml"), "value: "+value+"\n")
}

func TestOrchestratorDiffAppsStrictChangedOnlyOwnsHelmLocalInputs(t *testing.T) {
	for name, write := range map[string]func(*testing.T, string, string){
		"escaped value file":     writeHelmEscapedValueFileApps,
		"escaped file parameter": writeHelmEscapedFileParameterApps,
		"value file glob":        writeHelmValueFileGlobApps,
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			left := filepath.Join(root, "left")
			right := filepath.Join(root, "right")
			write(t, left, "old")
			write(t, right, "new")

			result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
				LeftPath:          left,
				RightPath:         right,
				ChangedOnly:       true,
				StrictChangedOnly: true,
				Unified:           3,
			})
			if err != nil {
				t.Fatalf("DiffApps() error = %v, diagnostics = %#v", err, result.Diagnostics)
			}
			if len(result.Diagnostics) != 0 {
				t.Fatalf("Diagnostics = %#v, want none", result.Diagnostics)
			}
			if len(result.Results) != 1 || result.Results[0].Parent.Name != "web-prod" {
				t.Fatalf("Results = %#v, want one web-prod diff", result.Results)
			}
			for _, want := range []string{"argocd/web-prod", "-  value: old", "+  value: new"} {
				if !strings.Contains(result.Results[0].Diff, want) {
					t.Fatalf("diff missing %q:\n%s", want, result.Results[0].Diff)
				}
			}
		})
	}
}

// initSelfRepoRefValuesSiblingRepo commits, on master, the multi-source demo
// Application (chart from a Helm repository, $repo values from the repository
// under diff) and cluster-prod, a Kustomize Application whose overlay
// includes values/ as a base, so the sibling's graph owns values/demo.yaml.
// Branch feature changes only that value file. Diff it in Repo mode with
// RefOrig master and Ref feature, a git acquirer that errors, and
// newSelfRepoValueChartAcquirer.
func initSelfRepoRefValuesSiblingRepo(t *testing.T) string {
	t.Helper()
	root, fixture := initSelfRepoDiffGitRepo(t, selfRepoRemoteURL)
	writeSelfRepoRefValuesApp(t, root, "demo", selfRepoSpecURL, "HEAD", "old")
	writeTestFile(t, filepath.Join(root, "apps", "cluster-prod.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: cluster-prod
  namespace: argocd
spec:
  source:
    repoURL: `+selfRepoSpecURL+`
    targetRevision: HEAD
    path: clusters/prod
  destination:
    name: in-cluster
    namespace: cluster
`)
	writeTestFile(t, filepath.Join(root, "clusters", "prod", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ../../values
`)
	writeTestFile(t, filepath.Join(root, "values", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - configmap.yaml
`)
	writeTestFile(t, filepath.Join(root, "values", "configmap.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: cluster
data:
  value: same
`)
	commitDiffGitRepo(t, fixture.repo, fixture.wt, "baseline")
	checkoutDiffGitBranch(t, fixture.wt, "feature")
	writeSelfRepoRefValuesFile(t, root, "demo", "new")
	commitDiffGitRepo(t, fixture.repo, fixture.wt, "feature values")
	return root
}

func TestOrchestratorDiffAppsStrictChangedOnlyOwnsSelfRepoRefValues(t *testing.T) {
	root := initSelfRepoRefValuesSiblingRepo(t)

	gitAcquirer := &countingGitAcquirer{err: errors.New("self-repo ref must not be acquired")}
	result, err := (Orchestrator{
		GitAcquirer:   gitAcquirer,
		ChartAcquirer: newSelfRepoValueChartAcquirer(t),
	}).DiffApps(context.Background(), DiffRequest{
		Repo:              root,
		RefOrig:           "master",
		Ref:               "feature",
		ChangedOnly:       true,
		StrictChangedOnly: true,
		Unified:           3,
		Parallelism:       1,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v, diagnostics = %#v", err, result.Diagnostics)
	}
	if len(result.Diagnostics) != 0 {
		t.Fatalf("Diagnostics = %#v, want none", result.Diagnostics)
	}
	assertSingleDiffContains(t, result, "argocd/demo", "-  value: old", "+  value: new")
	if got := gitAcquirer.calls(); got != 0 {
		t.Fatalf("git acquire calls = %d, want 0: %#v", got, gitAcquirer.requests)
	}
}
