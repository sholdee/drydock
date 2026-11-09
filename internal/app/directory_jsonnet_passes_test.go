package app

import (
	"context"
	"path/filepath"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
)

// writeJsonnetPassRepo writes a directory source with two Jsonnet files.
// The repo-server substitutes the extVar code once more for each Jsonnet
// file that it evaluates, so a.jsonnet imports
// shared/${ARGOCD_APP_NAME}.libsonnet and b.jsonnet imports
// shared/demo.libsonnet. code is the value of the extVar cfg, and names
// are the Jsonnet files, a and b when there are none.
func writeJsonnetPassRepo(t *testing.T, root, code string, names ...string) argoappv1.Application {
	t.Helper()
	if len(names) == 0 {
		names = []string{"a", "b"}
	}
	for _, name := range names {
		writeTestFile(t, filepath.Join(root, "app", name+".jsonnet"), `{
  apiVersion: 'v1',
  kind: 'ConfigMap',
  metadata: { name: '`+name+`' },
  data: { value: std.extVar('cfg').value },
}
`)
	}
	writeTestFile(t, filepath.Join(root, "shared", "${ARGOCD_APP_NAME}.libsonnet"), "{ value: 'first pass' }\n")
	writeTestFile(t, filepath.Join(root, "shared", "demo.libsonnet"), "{ value: 'second pass' }\n")
	writeTestFile(t, filepath.Join(root, "unrelated", "README.md"), "not a render input\n")
	return argoappv1.Application{
		Name:      "demo",
		Namespace: "argocd",
		Spec: argoappv1.ApplicationSpec{
			Source: &argoappv1.ApplicationSource{
				RepoURL: "https://git.example.test/org/repo.git", Path: "app", TargetRevision: "main",
				Directory: &argoappv1.ApplicationSourceDirectory{Jsonnet: argoappv1.ApplicationSourceJsonnet{
					ExtVars: []argoappv1.JsonnetVar{{Name: "cfg", Value: code, Code: true}},
				}},
			},
			Destination: argoappv1.ApplicationDestination{Namespace: "default"},
		},
	}
}

const jsonnetPassCode = "import '../shared/$${ARGOCD_APP_NAME}.libsonnet'"

// TestRenderInputCoverageDirectoryJsonnetCodePasses pins that the cache
// digest covers the imports of every env substitution pass: the file that
// the first Jsonnet file imports and the file that the second imports.
func TestRenderInputCoverageDirectoryJsonnetCodePasses(t *testing.T) {
	root := t.TempDir()
	application := writeJsonnetPassRepo(t, root, jsonnetPassCode)
	assertRenderInputCoverage(t, root, application)
}

// TestWithSelectionOnlyPathsOwnsJsonnetCodePasses pins that changed-only
// selection owns the imports of every env substitution pass.
func TestWithSelectionOnlyPathsOwnsJsonnetCodePasses(t *testing.T) {
	root := t.TempDir()
	application := writeJsonnetPassRepo(t, root, jsonnetPassCode)
	side := withSelectionOnlyPaths(context.Background(), root, nil, selfRepoRefs{}, sourceTypeGates{}, []ApplicationSelectionInput{{Application: application}})
	if len(side.unscanned) != 0 {
		t.Fatalf("unscanned = %v, want the imports known", side.unscanned)
	}
	for _, changed := range []string{"shared/${ARGOCD_APP_NAME}.libsonnet", "shared/demo.libsonnet"} {
		if selected, _ := SelectChangedApplicationInputs(side.inputs, []string{changed}); len(selected) != 1 {
			t.Errorf("change to %s selected %d Applications, want the Jsonnet source (paths %v)", changed, len(selected), side.inputs[0].Paths)
		}
	}
}

// TestWithSelectionOnlyPathsJsonnetCodeReadsRevisionInALaterPass pins
// that the revision probe checks every pass. "$$ARGOCD_APP_REVISION_SHORT"
// is a literal in the first pass and the revision in the second, so with
// two Jsonnet files the imports read the revision, and with one they do
// not.
func TestWithSelectionOnlyPathsJsonnetCodeReadsRevisionInALaterPass(t *testing.T) {
	root := t.TempDir()
	application := writeJsonnetPassRepo(t, root, "import '../shared/$${ARGOCD_APP_REVISION_SHORT}.libsonnet'")
	side := withSelectionOnlyPaths(context.Background(), root, nil, selfRepoRefs{}, sourceTypeGates{}, []ApplicationSelectionInput{{Application: application}})
	if len(side.unscanned) != 1 {
		t.Fatalf("unscanned = %v, want the source with two Jsonnet files unknown", side.unscanned)
	}

	single := t.TempDir()
	application = writeJsonnetPassRepo(t, single, "import '../shared/$${ARGOCD_APP_REVISION_SHORT}.libsonnet'", "a")
	side = withSelectionOnlyPaths(context.Background(), single, nil, selfRepoRefs{}, sourceTypeGates{}, []ApplicationSelectionInput{{Application: application}})
	if len(side.unscanned) != 0 {
		t.Fatalf("unscanned = %v, want the source with one Jsonnet file known", side.unscanned)
	}
}
