package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"testing"

	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/diff"
	sourcepkg "github.com/sholdee/drydock/internal/source"
)

// changedOnlyOracleCase is one differential row: changed-only selection must
// reproduce the full diff, or fall back to rendering all with wantFallback.
type changedOnlyOracleCase struct {
	name string
	// setup writes fresh trees and returns the Orchestrator (acquirer fakes)
	// and the request that diffs them.
	setup func(*testing.T) (Orchestrator, DiffRequest)
	// wantFallback is "" or the diagnostic code of the expected render-all:
	// diff.changed-only-incomplete or diff.changed-only-settings.
	wantFallback string
	// wantApplications are the Applications the full diff reports, so a
	// fixture that stops diffing cannot make the row pass vacuously.
	wantApplications []string
}

// pathPairOracleSetup diffs two trees write fills with left and right.
func pathPairOracleSetup(write func(*testing.T, string, string), left, right string, discovery DiscoveryOptions) func(*testing.T) (Orchestrator, DiffRequest) {
	return func(t *testing.T) (Orchestrator, DiffRequest) {
		root := t.TempDir()
		leftPath := filepath.Join(root, "left")
		rightPath := filepath.Join(root, "right")
		write(t, leftPath, left)
		write(t, rightPath, right)
		return Orchestrator{}, DiffRequest{
			LeftPath:         leftPath,
			RightPath:        rightPath,
			DiscoveryOptions: discovery,
			Unified:          3,
		}
	}
}

// repoOracleSetup diffs master against feature in the repository init
// commits, with a git acquirer that fails: every source must resolve to the
// side trees.
func repoOracleSetup(init func(*testing.T) string, chartAcquirer bool, repoMaps func(root string) []sourcepkg.RepoMap) func(*testing.T) (Orchestrator, DiffRequest) {
	return func(t *testing.T) (Orchestrator, DiffRequest) {
		root := init(t)
		o := Orchestrator{GitAcquirer: &countingGitAcquirer{err: errors.New("the repository under diff must not be acquired")}}
		if chartAcquirer {
			o.ChartAcquirer = newSelfRepoValueChartAcquirer(t)
		}
		request := DiffRequest{
			Repo:    root,
			RefOrig: "master",
			Ref:     "feature",
			Unified: 3,
			// recordingChartAcquirer is not mutex-guarded.
			Parallelism: 1,
		}
		if repoMaps != nil {
			request.RepoMaps = repoMaps(root)
		}
		return o, request
	}
}

// repoMappedValuesURL is a values repository that is not the repository
// under diff; only an explicit --repo-map makes its $ref root local.
const repoMappedValuesURL = "https://github.com/example/values"

// initRepoMappedRefValuesSiblingRepo is initSelfRepoRefValuesSiblingRepo
// with the $repo ref naming repoMappedValuesURL, which the request repo-maps
// to the checkout under diff: diff.go rewrites that map to each side's tree.
func initRepoMappedRefValuesSiblingRepo(t *testing.T) string {
	t.Helper()
	root, fixture := initSelfRepoDiffGitRepo(t, selfRepoRemoteURL)
	writeSelfRepoRefValuesApp(t, root, "demo", repoMappedValuesURL, "HEAD", "old")
	writeKustomizeSiblingOver(t, root, "values")
	commitDiffGitRepo(t, fixture.repo, fixture.wt, "baseline")
	checkoutDiffGitBranch(t, fixture.wt, "feature")
	writeSelfRepoRefValuesFile(t, root, "demo", "new")
	commitDiffGitRepo(t, fixture.repo, fixture.wt, "feature values")
	return root
}

// TestChangedOnlyMatchesFullDiff is the differential guard over every
// changed-only ownership channel: for each fixture, the changed-only diff
// equals the full diff, and render-all happens only where a row expects it.
// It catches dropped Applications and wrong or one-sided renders; it cannot
// see over-selection, which only costs render time.
//
// To guard a new ownership channel, add a row whose writer takes (t, root,
// value) and whose two values change only an input that channel alone
// reaches, with the Applications the full diff reports in wantApplications.
// Without an expected fallback, the row fails whether the gap is a silent
// drop or an unowned-path render-all.
func TestChangedOnlyMatchesFullDiff(t *testing.T) {
	settingsDiscovery := DiscoveryOptions{
		DiscoverKustomizePaths: []string{"clusters/prod-apps"},
		// Hides the static argocd-cm and child, which would duplicate the
		// rendered ones.
		DiscoverIgnoreGlobs: []string{"argocd/**"},
	}
	for _, tc := range []changedOnlyOracleCase{
		// Kustomize graph ownership.
		{name: "plain base resource", setup: pathPairOracleSetup(writeKustomizePlainBaseApps, "old", "new", DiscoveryOptions{}), wantApplications: []string{"demo-production", "demo-staging"}},
		{name: "base helmCharts values", setup: pathPairOracleSetup(writeKustomizeOverlayApps, "old", "new", DiscoveryOptions{}), wantApplications: []string{"demo-production", "demo-staging"}},
		{name: "source kustomize components", setup: pathPairOracleSetup(writeSourceKustomizeComponentApps, "old", "new", DiscoveryOptions{}), wantApplications: []string{"demo-production", "demo-staging"}},
		{name: "avp plugin source", setup: pathPairOracleSetup(writeAVPPluginOverlayApps, "old", "new", DiscoveryOptions{}), wantApplications: []string{"demo-production", "demo-staging"}},
		{name: "native kustomize plugin", setup: pathPairOracleSetup(writeNativeKustomizePluginOverlayApps, "old", "new", DiscoveryOptions{}), wantApplications: []string{"demo-production", "demo-staging"}},
		// Helm local value files and file parameters.
		{name: "escaped value file", setup: pathPairOracleSetup(writeHelmEscapedValueFileApps, "old", "new", DiscoveryOptions{}), wantApplications: []string{"web-prod"}},
		{name: "escaped file parameter", setup: pathPairOracleSetup(writeHelmEscapedFileParameterApps, "old", "new", DiscoveryOptions{}), wantApplications: []string{"web-prod"}},
		{name: "value file glob", setup: pathPairOracleSetup(writeHelmValueFileGlobApps, "old", "new", DiscoveryOptions{}), wantApplications: []string{"web-prod"}},
		// Local $ref value sources.
		{name: "self-repo $values ref", setup: repoOracleSetup(initSelfRepoRefValuesSiblingRepo, true, nil), wantApplications: []string{"demo"}},
		{
			name: "repo-mapped $values ref at the diffed repo",
			setup: repoOracleSetup(initRepoMappedRefValuesSiblingRepo, true, func(root string) []sourcepkg.RepoMap {
				return []sourcepkg.RepoMap{{URL: repoMappedValuesURL, Path: root}}
			}),
			wantApplications: []string{"demo"},
		},
		// Rendered children.
		{name: "discover-kustomize child", setup: pathPairOracleSetup(writeDiscoverKustomizeChildApps, "v1", "v2", DiscoveryOptions{DiscoverKustomizePaths: []string{"clusters/prod-apps"}}), wantApplications: []string{"child"}},
		{name: "rendered-fleet Kustomize child", setup: pathPairOracleSetup(writeRenderedFleetKustomizeChildApps, "v1", "v2", DiscoveryOptions{DiscoverIgnoreGlobs: []string{"argocd/base/**"}}), wantApplications: []string{"child", "parent"}},
		{name: "local-chart app-of-apps escaped value file", setup: pathPairOracleSetup(writeHelmValuesAppOfAppsApps, "v1", "v2", DiscoveryOptions{}), wantApplications: []string{"apps", "child"}},
		{name: "local-chart app-of-apps self-repo $values ref", setup: repoOracleSetup(initSelfRepoRefAppOfAppsRepo, false, nil), wantApplications: []string{"apps", "child"}},
		{name: "nested app-of-apps grandchild", setup: pathPairOracleSetup(writeNestedHelmAppOfAppsApps, "v1", "v2", DiscoveryOptions{}), wantApplications: []string{"apps", "child", "mid"}},
		// Argo CD settings.
		{
			name:             "settings change through a discover-kustomize graph",
			setup:            pathPairOracleSetup(writeDiscoverKustomizeSettingsApps, trackingAnnotationSettings, trackingLabelSettings, settingsDiscovery),
			wantFallback:     changedOnlySettingsCode,
			wantApplications: []string{"child", "other"},
		},
		{
			name:             "settings change under a self-managed Application",
			setup:            pathPairOracleSetup(writeSelfManagedSettingsApps, trackingAnnotationSettings, excludeConfigMapSettings, DiscoveryOptions{}),
			wantFallback:     changedOnlySettingsCode,
			wantApplications: []string{"argocd", "other"},
		},
		{
			// Settings files present and identical: no settings render-all,
			// and the result still equals the full diff.
			name: "identical settings with an unrelated change",
			setup: pathPairOracleSetup(func(t *testing.T, root, value string) {
				t.Helper()
				writeDiscoverKustomizeSettingsApps(t, root, trackingAnnotationSettings)
				writeDiffApplication(t, root, "other", "other", value)
			}, "old", "new", settingsDiscovery),
			wantApplications: []string{"other"},
		},
		// Control: a path no Application owns still renders all.
		{
			name: "unowned README",
			setup: pathPairOracleSetup(func(t *testing.T, root, readme string) {
				t.Helper()
				writeKustomizePlainBaseApps(t, root, "same")
				writeTestFile(t, filepath.Join(root, "README.md"), readme)
			}, "left\n", "right\n", DiscoveryOptions{}),
			wantFallback: "diff.changed-only-incomplete",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, request := tc.setup(t)
			full := assertChangedOnlyMatchesFullDiff(t, o, request, tc.wantFallback)
			if got := diffedApplications(DiffResult{Results: full}); !slices.Equal(got, tc.wantApplications) {
				t.Fatalf("full diff Applications = %v, want %v", got, tc.wantApplications)
			}
		})
	}
}

// assertChangedOnlyMatchesFullDiff runs request as a full diff and as a
// non-strict changed-only diff and returns the full results. wantFallback ""
// requires no changed-only diagnostic at all; otherwise exactly one, with
// that code. Either way the changed-only results must equal the full results
// (a render-all is still a full render), compared field by field after a
// deterministic sort.
func assertChangedOnlyMatchesFullDiff(t *testing.T, o Orchestrator, request DiffRequest, wantFallback string) []diff.Result {
	t.Helper()
	request.ChangedOnly = false
	request.StrictChangedOnly = false
	request.Strict = false
	full, err := o.DiffApps(context.Background(), request)
	if err != nil {
		t.Fatalf("full DiffApps() error = %v, diagnostics = %#v", err, full.Diagnostics)
	}
	request.ChangedOnly = true
	changedOnly, err := o.DiffApps(context.Background(), request)
	if err != nil {
		t.Fatalf("changed-only DiffApps() error = %v, diagnostics = %#v", err, changedOnly.Diagnostics)
	}

	var codes []string
	for _, diag := range diagnostic.WithStableCodes(changedOnly.Diagnostics) {
		if diag.Category == "changed-only" {
			codes = append(codes, diag.Code)
		}
	}
	switch {
	case wantFallback == "" && len(codes) != 0:
		t.Fatalf("changed-only diagnostics %v, want none: %#v", codes, changedOnly.Diagnostics)
	case wantFallback != "" && !slices.Equal(codes, []string{wantFallback}):
		t.Fatalf("changed-only diagnostics %v, want exactly [%s]: %#v", codes, wantFallback, changedOnly.Diagnostics)
	}
	assertDiffResultsEqual(t, full, changedOnly)
	return full.Results
}

// assertDiffResultsEqual reports the first result, by Application and
// resource, where the changed-only diff departs from the full diff.
func assertDiffResultsEqual(t *testing.T, full, changedOnly DiffResult) {
	t.Helper()
	want := sortedDiffResults(full.Results)
	got := sortedDiffResults(changedOnly.Results)
	for i := range max(len(want), len(got)) {
		if i < len(want) && i < len(got) && want[i] == got[i] {
			continue
		}
		detail := ""
		if i < len(want) && i < len(got) && diffResultIdentity(want[i]) == diffResultIdentity(got[i]) {
			detail = fmt.Sprintf("\nfull:\n%#v\nchanged-only:\n%#v", want[i], got[i])
		}
		t.Fatalf("changed-only result %d = %s, full diff has %s (Applications: full %v, changed-only %v)%s",
			i, describeDiffResult(got, i), describeDiffResult(want, i), diffedApplications(full), diffedApplications(changedOnly), detail)
	}
}

// sortedDiffResults orders results by Application, source, and resource
// identity, with the remaining fields as tie-breakers.
func sortedDiffResults(results []diff.Result) []diff.Result {
	sorted := slices.Clone(results)
	slices.SortFunc(sorted, func(a, b diff.Result) int {
		return cmp.Or(
			cmp.Compare(a.Parent.Namespace, b.Parent.Namespace),
			cmp.Compare(a.Parent.Name, b.Parent.Name),
			cmp.Compare(a.Parent.SourceIndex, b.Parent.SourceIndex),
			cmp.Compare(a.Resource.Group, b.Resource.Group),
			cmp.Compare(a.Resource.Kind, b.Resource.Kind),
			cmp.Compare(a.Resource.Namespace, b.Resource.Namespace),
			cmp.Compare(a.Resource.Name, b.Resource.Name),
			cmp.Compare(a.Parent.SourceName, b.Parent.SourceName),
			cmp.Compare(a.Parent.SourcePath, b.Parent.SourcePath),
			cmp.Compare(a.Change, b.Change),
			cmp.Compare(a.Diff, b.Diff),
		)
	})
	return sorted
}

func diffResultIdentity(result diff.Result) string {
	return fmt.Sprintf("%s/%s[%d] %s/%s %s/%s", result.Parent.Namespace, result.Parent.Name, result.Parent.SourceIndex,
		result.Resource.Group, result.Resource.Kind, result.Resource.Namespace, result.Resource.Name)
}

func describeDiffResult(results []diff.Result, i int) string {
	if i >= len(results) {
		return "nothing"
	}
	return diffResultIdentity(results[i]) + " (" + string(results[i].Change) + ")"
}
