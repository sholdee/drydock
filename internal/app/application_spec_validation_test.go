package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	argoutil "github.com/argoproj/argo-cd/v3/util/argo"
	git "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	sourcepkg "github.com/sholdee/drydock/internal/source"
	sigsyaml "sigs.k8s.io/yaml"
)

// upstreamDestinationStop is a destination that the Argo CD v3.6.1
// GetDestinationServer rejects before it reads the cluster database, so
// ValidatePermissions runs its source checks and then stops without one.
var upstreamDestinationStop = argoappv1.ApplicationDestination{Name: "stop", Server: "https://stop.invalid"}

const upstreamDestinationStopMessage = "application destination can't have both name and server defined"

// upstreamSourceConditions returns the InvalidSpecError messages that the
// Argo CD v3.6.1 controller sets for the sources of spec
// (argo.ValidatePermissions), without the destination one.
func upstreamSourceConditions(t *testing.T, spec argoappv1.ApplicationSpec) []string {
	t.Helper()
	spec.Destination = upstreamDestinationStop
	proj := &argoappv1.AppProject{Spec: argoappv1.AppProjectSpec{
		SourceRepos:  []string{"*"},
		Destinations: []argoappv1.ApplicationDestination{{Server: "*", Namespace: "*"}},
	}}
	proj.Name = argoappv1.DefaultAppProjectName
	conditions, err := argoutil.ValidatePermissions(context.Background(), &spec, proj, nil)
	if err != nil {
		t.Fatalf("argo.ValidatePermissions() error = %v", err)
	}
	var messages []string
	for _, condition := range conditions {
		if condition.Type != argoappv1.ApplicationConditionInvalidSpecError {
			t.Fatalf("argo.ValidatePermissions() condition type = %q, want InvalidSpecError", condition.Type)
		}
		if strings.Contains(condition.Message, upstreamDestinationStopMessage) {
			continue
		}
		messages = append(messages, condition.Message)
	}
	return messages
}

// TestValidateApplicationSpecSourcesMatchesUpstream runs the Argo CD v3.6.1
// controller's ValidatePermissions on each spec and requires
// validateApplicationSpecSources to fail exactly when it sets a source
// InvalidSpecError, with the same text.
func TestValidateApplicationSpecSourcesMatchesUpstream(t *testing.T) {
	const repo = "https://example.test/repo.git"
	const oci = "oci://registry.example.test/charts/demo"
	testCases := []struct {
		name string
		spec argoappv1.ApplicationSpec
	}{
		{name: "no source at all"},
		{name: "single path", spec: argoappv1.ApplicationSpec{Source: &argoappv1.ApplicationSource{RepoURL: repo, Path: "apps/demo"}}},
		{name: "single chart", spec: argoappv1.ApplicationSpec{Source: &argoappv1.ApplicationSource{RepoURL: "https://charts.example.test", Chart: "demo", TargetRevision: "1.0.0"}}},
		{name: "single chart without targetRevision", spec: argoappv1.ApplicationSpec{Source: &argoappv1.ApplicationSource{RepoURL: "https://charts.example.test", Chart: "demo"}}},
		{name: "single without path or chart", spec: argoappv1.ApplicationSpec{Source: &argoappv1.ApplicationSource{RepoURL: repo, TargetRevision: "HEAD"}}},
		{name: "single with only ref", spec: argoappv1.ApplicationSpec{Source: &argoappv1.ApplicationSource{RepoURL: repo, Ref: "values"}}},
		{name: "single without repoURL", spec: argoappv1.ApplicationSpec{Source: &argoappv1.ApplicationSource{Path: "apps/demo"}}},
		{name: "single OCI without path or chart", spec: argoappv1.ApplicationSpec{Source: &argoappv1.ApplicationSource{RepoURL: oci, TargetRevision: "1.0.0"}}},
		{name: "single OCI with path", spec: argoappv1.ApplicationSpec{Source: &argoappv1.ApplicationSource{RepoURL: oci, Path: ".", TargetRevision: "1.0.0"}}},
		{name: "multi path and ref", spec: argoappv1.ApplicationSpec{Sources: argoappv1.ApplicationSources{
			{RepoURL: repo, Path: "apps/demo"},
			{RepoURL: repo, Ref: "values"},
		}}},
		{name: "multi entry without path, chart, or ref", spec: argoappv1.ApplicationSpec{Sources: argoappv1.ApplicationSources{
			{RepoURL: repo, Path: "apps/demo"},
			{RepoURL: repo, TargetRevision: "HEAD"},
		}}},
		{name: "multi entry without repoURL", spec: argoappv1.ApplicationSpec{Sources: argoappv1.ApplicationSources{
			{Path: "apps/demo"},
		}}},
		{name: "multi chart without targetRevision", spec: argoappv1.ApplicationSpec{Sources: argoappv1.ApplicationSources{
			{RepoURL: repo, Ref: "values"},
			{RepoURL: "https://charts.example.test", Chart: "demo"},
		}}},
		{name: "multi OCI ref and chart without targetRevision", spec: argoappv1.ApplicationSpec{Sources: argoappv1.ApplicationSources{
			{RepoURL: oci, Chart: "demo", Ref: "values"},
		}}},
		{name: "sources win over an invalid source", spec: argoappv1.ApplicationSpec{
			Source:  &argoappv1.ApplicationSource{RepoURL: repo},
			Sources: argoappv1.ApplicationSources{{RepoURL: repo, Path: "apps/demo"}},
		}},
		{name: "hydrator ignores an invalid source", spec: argoappv1.ApplicationSpec{
			Source: &argoappv1.ApplicationSource{RepoURL: repo},
			SourceHydrator: &argoappv1.SourceHydrator{
				DrySource:  argoappv1.DrySource{RepoURL: repo, Path: "dry", TargetRevision: "HEAD"},
				SyncSource: argoappv1.SyncSource{TargetBranch: "env/prod", Path: "hydrated"},
			},
		}},
		{name: "hydrator without dry repoURL", spec: argoappv1.ApplicationSpec{SourceHydrator: &argoappv1.SourceHydrator{
			SyncSource: argoappv1.SyncSource{TargetBranch: "env/prod", Path: "hydrated"},
		}}},
		{name: "hydrator without targetBranch", spec: argoappv1.ApplicationSpec{SourceHydrator: &argoappv1.SourceHydrator{
			DrySource:  argoappv1.DrySource{RepoURL: repo, Path: "dry"},
			SyncSource: argoappv1.SyncSource{Path: "hydrated"},
		}}},
		{name: "hydrator with every check failing", spec: argoappv1.ApplicationSpec{SourceHydrator: &argoappv1.SourceHydrator{
			SyncSource: argoappv1.SyncSource{Path: "hydrated"},
			HydrateTo:  &argoappv1.HydrateTo{},
		}}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			want := upstreamSourceConditions(t, testCase.spec)
			err := validateApplicationSpecSources(testCase.spec)
			if len(want) == 0 {
				if err != nil {
					t.Fatalf("validateApplicationSpecSources() error = %v, want nil like Argo CD", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateApplicationSpecSources() error = nil, want Argo CD's %q", want)
			}
			for _, message := range want {
				// The multi-source message ends with the source in
				// protobuf text form; drydock names the source index.
				message, _, _ = strings.Cut(message, " for source ")
				if !strings.Contains(err.Error(), message) {
					t.Fatalf("validateApplicationSpecSources() error = %q, want it to contain Argo CD's %q", err, message)
				}
			}
		})
	}
}

// TestPlanRendersOnlyTheHydratorSyncSource pins the sources of an
// Application with spec.sourceHydrator: the controller compares only the
// sync source (ApplicationSpec.GetSources), so spec.source and
// spec.sources are ignored, and the sync source takes the dry source's
// repository when it names none.
func TestPlanRendersOnlyTheHydratorSyncSource(t *testing.T) {
	spec := argoappv1.ApplicationSpec{
		Source:  &argoappv1.ApplicationSource{RepoURL: "https://example.test/ignored.git", Path: "ignored"},
		Sources: argoappv1.ApplicationSources{{RepoURL: "https://example.test/ignored.git", Path: "ignored-too"}},
		SourceHydrator: &argoappv1.SourceHydrator{
			DrySource:  argoappv1.DrySource{RepoURL: "https://example.test/repo.git", Path: "dry", TargetRevision: "main"},
			SyncSource: argoappv1.SyncSource{TargetBranch: "env/prod", Path: "hydrated/prod"},
		},
	}
	plan, err := Plan(argoappv1.Application{Spec: spec})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	upstream := spec.GetSources()
	if len(plan.Sources) != len(upstream) {
		t.Fatalf("Plan() sources = %d, want %d like ApplicationSpec.GetSources", len(plan.Sources), len(upstream))
	}
	if !plan.Sources[0].Source.Equals(&upstream[0]) {
		t.Fatalf("Plan() source = %#v, want the sync source %#v", plan.Sources[0].Source, upstream[0])
	}
	if got := plan.Sources[0].Source; got.RepoURL != "https://example.test/repo.git" || got.Path != "hydrated/prod" || got.TargetRevision != "env/prod" {
		t.Fatalf("Plan() source = %#v, want the dry repository at env/prod, path hydrated/prod", got)
	}
}

// writeSpecValidationRepo writes one Application and the directories that
// a hydrator's dry source, sync source, and spec.source would render, each
// with its own ConfigMap.
func writeSpecValidationRepo(t *testing.T, root, application string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", "app.yaml"), application)
	for _, name := range []string{"dry", "hydrated", "ignored"} {
		writeTestFile(t, filepath.Join(root, "workloads", name, "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: from-`+name+`
`)
	}
}

func specValidationBuildRequest(root string) BuildRequest {
	return BuildRequest{Path: root, RepoMaps: []sourcepkg.RepoMap{{URL: "https://example.test/repo.git", Path: root}}}
}

// hydratorBranchRepo makes a Git repository at root whose master branch
// and env/prod branch hold different ConfigMaps at workloads/hydrated, with
// master checked out and origin at https://example.test/repo.git.
func hydratorBranchRepo(t *testing.T, root string) {
	t.Helper()
	writeSpecValidationRepo(t, root, `apiVersion: v1
kind: ConfigMap
metadata:
  name: placeholder
`)
	gitCommitAll(t, root, "master")
	repo, err := git.PlainOpen(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateRemote(&gitconfig.RemoteConfig{Name: "origin", URLs: []string{"https://example.test/repo.git"}}); err != nil {
		t.Fatal(err)
	}
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if err := worktree.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("env/prod"), Create: true}); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(root, "workloads", "hydrated", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: from-hydrated-branch\n")
	gitCommitAll(t, root, "env/prod")
	if err := worktree.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("master")}); err != nil {
		t.Fatal(err)
	}
}

func hydratorApplication(syncRepo, branch, syncPath string) string {
	return `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: hydrated
  namespace: argocd
spec:
  project: default
  destination:
    server: https://kubernetes.default.svc
    namespace: demo
  source:
    repoURL: https://example.test/repo.git
    targetRevision: HEAD
    path: workloads/ignored
  sourceHydrator:
    drySource:
      repoURL: https://example.test/repo.git
      targetRevision: HEAD
      path: workloads/dry
    syncSource:
      repoURL: ` + syncRepo + `
      targetBranch: ` + branch + `
      path: ` + syncPath + `
`
}

// TestBuildRendersHydratorSyncSource renders an Application with
// spec.sourceHydrator. drydock renders the sync source at its branch, as
// the Argo CD controller compares it. It never renders the dry source or
// spec.source, and it reads the local tree only when the tree holds the
// sync branch.
func TestBuildRendersHydratorSyncSource(t *testing.T) {
	const self = "https://example.test/repo.git"
	testCases := []struct {
		name     string
		syncRepo string
		branch   string
		syncPath string
		repoMap  bool
		want     string
		wantErr  string
	}{
		{name: "repo-mapped Git repository at another branch", syncRepo: self, branch: "env/prod", syncPath: "workloads/hydrated", repoMap: true, want: "from-hydrated-branch"},
		{name: "repo-mapped Git repository at the checked-out branch", syncRepo: self, branch: "master", syncPath: "workloads/hydrated", repoMap: true, want: "from-hydrated"},
		{name: "local repository at the checked-out branch", syncRepo: self, branch: "master", syncPath: "workloads/hydrated", want: "from-hydrated"},
		{name: "local repository at another branch", syncRepo: self, branch: "env/prod", syncPath: "workloads/hydrated", wantErr: "env/prod"},
		// The review repro: the sync path is also the local dry path, in
		// another repository.
		{name: "other repository whose path exists locally", syncRepo: "https://github.com/my-org/deployments", branch: "environments/dev", syncPath: "workloads/dry", wantErr: "environments/dev"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			hydratorBranchRepo(t, root)
			writeTestFile(t, filepath.Join(root, "apps", "app.yaml"), hydratorApplication(testCase.syncRepo, testCase.branch, testCase.syncPath))
			request := BuildRequest{Path: root, Strict: true, Offline: true, GitCacheDir: t.TempDir()}
			if testCase.repoMap {
				request.RepoMaps = []sourcepkg.RepoMap{{URL: self, Path: root}}
			}
			result, err := Orchestrator{}.Build(context.Background(), request)
			if testCase.wantErr != "" {
				if err == nil {
					t.Fatalf("Build() rendered %d manifests, want a failure: the sync branch is not in the local tree or the Git cache", len(result.Manifests))
				}
				if len(result.Statuses) != 1 || !strings.Contains(result.Statuses[0].Message, testCase.wantErr) {
					t.Fatalf("Statuses = %#v, want a failure naming %q", result.Statuses, testCase.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Build() error = %v; the hydrator advisory must not fail --strict", err)
			}
			if len(result.Manifests) != 1 {
				t.Fatalf("Manifests = %d, want only the sync source's ConfigMap", len(result.Manifests))
			}
			assertManifestNamed(t, result.Manifests, testCase.want)
			advisories := 0
			for _, diag := range result.Diagnostics {
				if diag.Code == sourceHydratorCode {
					advisories++
					if !strings.Contains(diag.Message, `branch "`+testCase.branch+`", path "`+testCase.syncPath+`"`) {
						t.Fatalf("hydrator advisory = %q, want the sync branch and path", diag.Message)
					}
				}
			}
			if advisories != 1 {
				t.Fatalf("hydrator advisories = %d in %#v, want 1", advisories, result.Diagnostics)
			}
		})
	}
}

// TestBuildFailsHydratorSyncFromPlainRepoMap pins that a --repo-map target
// that is no Git repository cannot stand in for a sync branch.
func TestBuildFailsHydratorSyncFromPlainRepoMap(t *testing.T) {
	root := t.TempDir()
	writeSpecValidationRepo(t, root, hydratorApplication("https://example.test/repo.git", "env/prod", "workloads/hydrated"))
	result, err := Orchestrator{}.Build(context.Background(), specValidationBuildRequest(root))
	if err == nil || len(result.Statuses) != 1 || !strings.Contains(result.Statuses[0].Message, "is not the top of a Git repository") {
		t.Fatalf("Build() error = %v, statuses = %#v, want the plain --repo-map target refused", err, result.Statuses)
	}
}

// TestBuildFailsInvalidSpecSources pins the render of Applications whose
// sources the Argo CD controller rejects with InvalidSpecError. drydock
// used to render nothing for them and pass.
func TestBuildFailsInvalidSpecSources(t *testing.T) {
	testCases := []struct {
		name   string
		source string
		want   string
	}{
		{
			name:   "single source without path or chart",
			source: "  source:\n    repoURL: https://example.test/repo.git\n    targetRevision: HEAD\n",
			want:   "spec.source.repoURL and either spec.source.path or spec.source.chart are required",
		},
		{
			name:   "multi-source entry without path, chart, or ref",
			source: "  sources:\n    - repoURL: https://example.test/repo.git\n      path: workloads/dry\n    - repoURL: https://example.test/repo.git\n      targetRevision: HEAD\n",
			want:   "sources[1]: invalid spec: spec.source.repoURL and either source.path, source.chart, or source.ref are required",
		},
		{
			name:   "chart without targetRevision",
			source: "  source:\n    repoURL: https://charts.example.test\n    chart: demo\n",
			want:   "spec.source.targetRevision is required if the manifest source is a helm chart",
		},
		{
			name:   "hydrator without targetBranch",
			source: "  sourceHydrator:\n    drySource:\n      repoURL: https://example.test/repo.git\n      targetRevision: HEAD\n      path: workloads/dry\n    syncSource:\n      targetBranch: \"\"\n      path: workloads/hydrated\n",
			want:   "spec.sourceHydrator.syncSource.targetBranch is required",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			writeSpecValidationRepo(t, root, `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: invalid
  namespace: argocd
spec:
  project: default
  destination:
    server: https://kubernetes.default.svc
    namespace: demo
`+testCase.source)
			result, err := Orchestrator{}.Build(context.Background(), specValidationBuildRequest(root))
			if err == nil {
				t.Fatalf("Build() error = nil with %d manifests, want the invalid spec failure", len(result.Manifests))
			}
			if len(result.Statuses) != 1 || result.Statuses[0].Status != ApplicationStatusFail || !strings.Contains(result.Statuses[0].Message, testCase.want) {
				t.Fatalf("Statuses = %#v, want one failure containing %q", result.Statuses, testCase.want)
			}
		})
	}
}

// TestParitySpecCasesMatchUpstream runs the spec cases of the Argo CD render
// parity smoke (testdata/argocd-parity/directory-decode-expected.yaml) on
// their committed Applications offline. drydock must fail with the
// drydockMessage the smoke expects, and the upstream ValidatePermissions
// must set an InvalidSpecError with the argocdMessage.
func TestParitySpecCasesMatchUpstream(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "argocd-parity", "directory-decode-expected.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var expected struct {
		Cases []struct {
			Name           string `json:"name"`
			Source         string `json:"source"`
			ArgoCDMessage  string `json:"argocdMessage"`
			DrydockMessage string `json:"drydockMessage"`
		} `json:"cases"`
	}
	if err := sigsyaml.Unmarshal(data, &expected); err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, tc := range expected.Cases {
		if tc.Source != "spec" {
			continue
		}
		seen++
		t.Run(tc.Name, func(t *testing.T) {
			file := filepath.Join("..", "..", "testdata", "argocd-parity", "repo", "directory-decode-applications", strings.TrimPrefix(tc.Name, "parity-decode-")+".yaml")
			manifest, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			var application argoappv1.Application
			if err := sigsyaml.Unmarshal(manifest, &application); err != nil {
				t.Fatal(err)
			}
			err = validateApplicationSpecSources(application.Spec)
			if err == nil || !strings.Contains(err.Error(), tc.DrydockMessage) {
				t.Fatalf("validateApplicationSpecSources() error = %v, want %q", err, tc.DrydockMessage)
			}
			upstream := upstreamSourceConditions(t, application.Spec)
			if len(upstream) != 1 || !strings.Contains(upstream[0], tc.ArgoCDMessage) {
				t.Fatalf("upstream InvalidSpecError = %q, want one containing %q", upstream, tc.ArgoCDMessage)
			}
		})
	}
	if seen == 0 {
		t.Fatal("the expected file has no spec case")
	}
}

// TestHydratorBranchForRequest pins the branch that a tree holds: the
// --ref or --ref-orig name of a diff side that renders a Git ref, never a
// commit SHA, and the checked-out branch otherwise.
func TestHydratorBranchForRequest(t *testing.T) {
	root := t.TempDir()
	hydratorBranchRepo(t, root)
	if got := hydratorBranchForRequest(root, BuildRequest{}); got != "master" {
		t.Fatalf("worktree branch = %q, want master", got)
	}
	if got := hydratorBranchForRequest(root, BuildRequest{rootRevision: "0123456789abcdef0123456789abcdef01234567", rootRefName: "env/prod"}); got != "env/prod" {
		t.Fatalf("ref side branch = %q, want env/prod", got)
	}
	if got := hydratorBranchForRequest(root, BuildRequest{rootRevision: "0123456789abcdef0123456789abcdef01234567", rootRefName: "0123456789abcdef0123456789abcdef01234567"}); got != "" {
		t.Fatalf("SHA side branch = %q, want empty", got)
	}
}

// TestValidateApplicationSpecSourcesAppliesSyncPathSchema pins the CRD
// schema of spec.sourceHydrator.syncSource.path (minLength 1 and the
// pattern ^.{2,}|[^./]$, pkg/apis/application/v1alpha1/types.go:510-512 at
// v3.6.1). The API server rejects such an Application, so Argo CD never
// holds it. The check runs before the controller checks.
func TestValidateApplicationSpecSourcesAppliesSyncPathSchema(t *testing.T) {
	spec := func(path string) argoappv1.ApplicationSpec {
		return argoappv1.ApplicationSpec{SourceHydrator: &argoappv1.SourceHydrator{
			DrySource:  argoappv1.DrySource{RepoURL: "https://example.test/repo.git", Path: "dry"},
			SyncSource: argoappv1.SyncSource{TargetBranch: "env/prod", Path: path},
		}}
	}
	for path, want := range map[string]string{
		"":  "should be at least 1 chars long",
		".": `Invalid value: ".": spec.sourceHydrator.syncSource.path in body should match '^.{2,}|[^./]$'`,
		"/": `Invalid value: "/": spec.sourceHydrator.syncSource.path in body should match '^.{2,}|[^./]$'`,
	} {
		err := validateApplicationSpecSources(spec(path))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("path %q: error = %v, want %q", path, err, want)
		}
	}
	for _, path := range []string{"a", "..", "./", "hydrated/prod", "x."} {
		if err := validateApplicationSpecSources(spec(path)); err != nil {
			t.Fatalf("path %q: error = %v, want nil", path, err)
		}
	}
}
