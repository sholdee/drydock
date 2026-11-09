package app

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/cacheevent"
	sigsyaml "sigs.k8s.io/yaml"
)

// jsonnetCodeImportApplicationYAML is a directory source whose Jsonnet
// file reads extVar and TLA code that imports files outside the source
// path: an import with a nested import, an import whose path names
// ARGOCD_APP_NAME, and a TLA import. The plain extVar has code: false, so
// it is a string that only looks like an import.
const jsonnetCodeImportApplicationYAML = `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: app
    directory:
      jsonnet:
        extVars:
          - name: cfg
            value: "import '../shared/cfg.libsonnet'"
            code: true
          - name: named
            value: "import '../shared/${ARGOCD_APP_NAME}.libsonnet'"
            code: true
          - name: plain
            value: "import '../shared/plain.libsonnet'"
        tlas:
          - name: tla
            value: "import '../shared/tla.libsonnet'"
            code: true
  destination:
    name: in-cluster
    namespace: default
`

// writeJsonnetCodeImportRepo writes the Application of
// jsonnetCodeImportApplicationYAML and its files. Only the extVar code
// imports shared/cfg.libsonnet, which holds value.
func writeJsonnetCodeImportRepo(t *testing.T, root, value string) argoappv1.Application {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", "demo.yaml"), jsonnetCodeImportApplicationYAML)
	writeTestFile(t, filepath.Join(root, "app", "main.jsonnet"), `function(tla) {
  apiVersion: 'v1',
  kind: 'ConfigMap',
  metadata: { name: 'demo' },
  data: {
    value: std.extVar('cfg').value,
    nested: std.extVar('cfg').nested,
    named: std.extVar('named').value,
    plain: std.extVar('plain'),
    tla: tla.value,
  },
}
`)
	writeTestFile(t, filepath.Join(root, "shared", "cfg.libsonnet"), "{ value: '"+value+"', nested: (import 'nested.libsonnet').value }\n")
	writeTestFile(t, filepath.Join(root, "shared", "nested.libsonnet"), "{ value: 'nested' }\n")
	writeTestFile(t, filepath.Join(root, "shared", "demo.libsonnet"), "{ value: 'named' }\n")
	writeTestFile(t, filepath.Join(root, "shared", "tla.libsonnet"), "{ value: 'tla' }\n")
	writeTestFile(t, filepath.Join(root, "shared", "plain.libsonnet"), "{ value: 'not read' }\n")
	writeTestFile(t, filepath.Join(root, "unrelated", "README.md"), "not a render input\n")
	var application argoappv1.Application
	if err := sigsyaml.Unmarshal([]byte(jsonnetCodeImportApplicationYAML), &application); err != nil {
		t.Fatalf("decode Application: %v", err)
	}
	return application
}

// TestRenderInputCoverageDirectoryJsonnetCodeImports pins that the
// persistent render cache digest covers the files that extVar and TLA code
// imports, also through a nested import and an import path that names a
// build environment variable.
func TestRenderInputCoverageDirectoryJsonnetCodeImports(t *testing.T) {
	root := t.TempDir()
	application := writeJsonnetCodeImportRepo(t, root, "one")
	assertRenderInputCoverage(t, root, application)
}

// TestPersistentCacheJsonnetCodeImportInvalidatesCache pins the stale-cache
// repro: a commit that edits only a file that extVar code imports makes a
// warm build render again and give the new value, the same output as a
// build with an empty cache.
func TestPersistentCacheJsonnetCodeImportInvalidatesCache(t *testing.T) {
	root := t.TempDir()
	writeJsonnetCodeImportRepo(t, root, "one")
	gitCommitAll(t, root, "initial")
	request := persistentBuildRequest(root, t.TempDir())
	request.RecordCacheEvents = true

	coldResult, err := (Orchestrator{}).Build(context.Background(), request)
	if err != nil {
		t.Fatalf("cold Build() error = %v", err)
	}
	if got := persistentConfigMapDataValue(t, coldResult, "demo", "demo", "value"); got != "one" {
		t.Fatalf("cold value = %q, want %q", got, "one")
	}
	if !hasRenderCacheEvent(coldResult.CacheEvents, cacheevent.ActionStore, "argocd/demo", "") {
		t.Fatalf("CacheEvents = %#v, want a persistent store for demo", coldResult.CacheEvents)
	}

	writeTestFile(t, filepath.Join(root, "shared", "cfg.libsonnet"), "{ value: 'two', nested: (import 'nested.libsonnet').value }\n")
	gitCommitAll(t, root, "change only the file that extVar code imports")

	warmOrchestrator := Orchestrator{}
	warmRendered := renderedSourceCounts(&warmOrchestrator)
	warmResult, err := warmOrchestrator.Build(context.Background(), request)
	if err != nil {
		t.Fatalf("warm Build() error = %v", err)
	}
	if warmRendered["app"] == 0 {
		t.Fatal("warm build after the edit rendered nothing; a stale cache entry served the old render")
	}
	if got := persistentConfigMapDataValue(t, warmResult, "demo", "demo", "value"); got != "two" {
		t.Fatalf("warm value = %q, want %q", got, "two")
	}
	freshResult, err := (Orchestrator{}).Build(context.Background(), persistentBuildRequest(root, t.TempDir()))
	if err != nil {
		t.Fatalf("fresh-cache Build() error = %v", err)
	}
	if !reflect.DeepEqual(applicationManifestObjects(warmResult, "demo"), applicationManifestObjects(freshResult, "demo")) {
		t.Fatal("warm manifests != fresh-cache manifests; the warm build served a stale render")
	}
}

// TestWithSelectionOnlyPathsOwnsJsonnetCodeImports pins that changed-only
// selection owns the files that extVar and TLA code imports. A value with
// code: false is a string, so the file it names is not owned.
func TestWithSelectionOnlyPathsOwnsJsonnetCodeImports(t *testing.T) {
	root := t.TempDir()
	application := writeJsonnetCodeImportRepo(t, root, "one")
	side := withSelectionOnlyPaths(context.Background(), root, nil, selfRepoRefs{}, sourceTypeGates{}, []ApplicationSelectionInput{{Application: application}})
	if len(side.unscanned) != 0 {
		t.Fatalf("unscanned = %v, want the imports known", side.unscanned)
	}
	for _, changed := range []string{"shared/cfg.libsonnet", "shared/nested.libsonnet", "shared/demo.libsonnet", "shared/tla.libsonnet"} {
		if selected, _ := SelectChangedApplicationInputs(side.inputs, []string{changed}); len(selected) != 1 {
			t.Errorf("change to %s selected %d Applications, want the Jsonnet source (paths %v)", changed, len(selected), side.inputs[0].Paths)
		}
	}
	for _, changed := range []string{"shared/plain.libsonnet", "unrelated/README.md"} {
		if selected, _ := SelectChangedApplicationInputs(side.inputs, []string{changed}); len(selected) != 0 {
			t.Errorf("change to %s selected the Jsonnet source, want nothing (paths %v)", changed, side.inputs[0].Paths)
		}
	}
}

// TestChangedOnlyDiffSelectsJsonnetCodeImport pins the changed-only repro:
// two trees that differ only in a file that extVar code imports. The diff
// selects the source through that file and shows the change, without the
// render-all fallback for a changed path that no Application owns.
func TestChangedOnlyDiffSelectsJsonnetCodeImport(t *testing.T) {
	parent := t.TempDir()
	left := filepath.Join(parent, "left")
	right := filepath.Join(parent, "right")
	writeJsonnetCodeImportRepo(t, left, "one")
	writeJsonnetCodeImportRepo(t, right, "two")
	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{LeftPath: left, RightPath: right, ChangedOnly: true, StrictChangedOnly: true, Unified: 3})
	if err != nil {
		t.Fatalf("DiffApps() error = %v, diagnostics = %#v", err, result.Diagnostics)
	}
	for _, diag := range result.Diagnostics {
		if diag.Code == "diff.changed-only-incomplete" {
			t.Fatalf("diagnostics = %#v, want the changed file owned", result.Diagnostics)
		}
	}
	if len(result.Results) != 1 || !strings.Contains(result.Results[0].Diff, "+  value: two") {
		t.Fatalf("results = %#v, want the demo ConfigMap value change", result.Results)
	}
}

// TestWithSelectionOnlyPathsJsonnetCodeReadsRevision pins code that names
// an ARGOCD_APP_REVISION variable. Selection does not know the revision of
// the render. When the variable is in an import path, the imports are not
// known, so the source is selected on any change. When it is only in a
// string, the imports are known.
func TestWithSelectionOnlyPathsJsonnetCodeReadsRevision(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		code        string
		wantUnknown bool
	}{
		{name: "import path", code: "import '../shared/${ARGOCD_APP_REVISION_SHORT}.libsonnet'", wantUnknown: true},
		{name: "string", code: "{ revision: '$ARGOCD_APP_REVISION', cfg: import '../shared/cfg.libsonnet' }"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			application := writeJsonnetCodeImportRepo(t, root, "one")
			application.Spec.Source.Directory.Jsonnet.ExtVars[0].Value = testCase.code
			side := withSelectionOnlyPaths(context.Background(), root, nil, selfRepoRefs{}, sourceTypeGates{}, []ApplicationSelectionInput{{Application: application}})
			if got := len(side.unscanned) == 1; got != testCase.wantUnknown {
				t.Fatalf("unscanned = %v, want unknown %t", side.unscanned, testCase.wantUnknown)
			}
			if !testCase.wantUnknown {
				if selected, _ := SelectChangedApplicationInputs(side.inputs, []string{"shared/cfg.libsonnet"}); len(selected) != 1 {
					t.Fatalf("change to shared/cfg.libsonnet selected %d Applications, want the Jsonnet source", len(selected))
				}
			}
		})
	}
}

// TestDirectoryDigestPathsWithoutJsonnetCodeKeepTheirBytes pins that the
// ext code scan adds nothing to a source whose extVars and TLAs import no
// file: plain string values and code without imports give the same digest
// paths as no values at all, so existing cache keys do not change.
func TestDirectoryDigestPathsWithoutJsonnetCodeKeepTheirBytes(t *testing.T) {
	root := t.TempDir()
	application := writeJsonnetCodeImportRepo(t, root, "one")
	provider := renderCoverageFixtureProvider(root)
	digestPaths := func(sourceJsonnet argoappv1.ApplicationSourceJsonnet) string {
		t.Helper()
		changed := application.DeepCopy()
		changed.Spec.Source.Directory.Jsonnet = sourceJsonnet
		plan := mustPlan(t, *changed)
		paths, _, err := localInputDigestPathsForSource(context.Background(), plan, plan.Sources[0], provider)
		if err != nil {
			t.Fatalf("localInputDigestPathsForSource() error = %v", err)
		}
		data, err := sigsyaml.Marshal(paths)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	none := digestPaths(argoappv1.ApplicationSourceJsonnet{})
	withoutImports := digestPaths(argoappv1.ApplicationSourceJsonnet{
		ExtVars: []argoappv1.JsonnetVar{
			{Name: "plain", Value: "import '../shared/plain.libsonnet'"},
			{Name: "code", Value: "{ a: 1 }", Code: true},
		},
		TLAs: []argoappv1.JsonnetVar{{Name: "broken", Value: "import '../shared/cfg.libsonnet' {", Code: true}},
	})
	if withoutImports != none {
		t.Fatalf("digest paths with values that import nothing = %s, want the paths without values %s", withoutImports, none)
	}
}

// TestDirectoryDigestPathsSubstituteJsonnetCodeLikeTheRender pins that the
// digest scan substitutes extVar code with the build environment of the
// render: ARGOCD_APP_NAME in the controller namespace of the provider, and
// the revision that PrepareSource resolved, not the spec revision.
func TestDirectoryDigestPathsSubstituteJsonnetCodeLikeTheRender(t *testing.T) {
	for _, testCase := range []struct {
		name                string
		code                string
		controllerNamespace string
		appRevision         string
		want                string
	}{
		{name: "app name", code: "import '../shared/${ARGOCD_APP_NAME}.libsonnet'", controllerNamespace: "ops", want: "shared/argocd_demo.libsonnet"},
		{name: "revision", code: "import '../shared/${ARGOCD_APP_REVISION_SHORT}.libsonnet'", appRevision: "abcdef0123456789abcdef0123456789abcdef01", want: "shared/abcdef0.libsonnet"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			application := writeJsonnetCodeImportRepo(t, root, "one")
			application.Spec.Source.Directory.Jsonnet.ExtVars = []argoappv1.JsonnetVar{{Name: "cfg", Value: testCase.code, Code: true}}
			provider := renderCoverageFixtureProvider(root)
			provider.controllerNamespace = testCase.controllerNamespace
			plan := mustPlan(t, application)
			plan.Sources[0].AppRevision = testCase.appRevision
			paths, _, err := localInputDigestPathsForSource(context.Background(), plan, plan.Sources[0], provider)
			if err != nil {
				t.Fatalf("localInputDigestPathsForSource() error = %v", err)
			}
			found := false
			for _, item := range paths {
				found = found || (item.Path == testCase.want && item.Optional)
			}
			if !found {
				t.Fatalf("digest paths = %#v, want optional %s", paths, testCase.want)
			}
		})
	}
}
