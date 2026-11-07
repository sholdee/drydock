package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	argoutil "github.com/argoproj/argo-cd/v3/util/argo"
	sourcepkg "github.com/sholdee/drydock/internal/source"
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
			HydrateTo: &argoappv1.HydrateTo{},
		}}},
		{name: "hydrator without sync path", spec: argoappv1.ApplicationSpec{SourceHydrator: &argoappv1.SourceHydrator{
			DrySource:  argoappv1.DrySource{RepoURL: repo, Path: "dry"},
			SyncSource: argoappv1.SyncSource{TargetBranch: "env/prod"},
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

// TestBuildRendersHydratorSyncSource renders an Application with
// spec.sourceHydrator. drydock renders the sync source, as the Argo CD
// controller compares it; it never renders the dry source or spec.source.
func TestBuildRendersHydratorSyncSource(t *testing.T) {
	root := t.TempDir()
	writeSpecValidationRepo(t, root, `apiVersion: argoproj.io/v1alpha1
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
      targetBranch: main
      path: workloads/hydrated
`)
	request := specValidationBuildRequest(root)
	request.Strict = true
	result, err := Orchestrator{}.Build(context.Background(), request)
	if err != nil {
		t.Fatalf("Build() error = %v; the hydrator advisory must not fail --strict", err)
	}
	if len(result.Manifests) != 1 {
		t.Fatalf("Manifests = %d, want only the sync source's ConfigMap", len(result.Manifests))
	}
	assertManifestNamed(t, result.Manifests, "from-hydrated")
	advisories := 0
	for _, diag := range result.Diagnostics {
		if diag.Code == sourceHydratorCode {
			advisories++
			if diag.Severity != "warning" || !strings.Contains(diag.Message, `branch "main", path "workloads/hydrated"`) {
				t.Fatalf("hydrator advisory = %#v, want a warning naming the sync branch and path", diag)
			}
		}
	}
	if advisories != 1 {
		t.Fatalf("hydrator advisories = %d in %#v, want 1", advisories, result.Diagnostics)
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
