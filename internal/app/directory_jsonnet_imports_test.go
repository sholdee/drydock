package app

import (
	"context"
	"path/filepath"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
)

// writeJsonnetImportFixture writes a directory source whose Jsonnet file
// imports files outside its path and its libraries: a relative import, an
// importstr, a nested import, and an import through a symlink.
func writeJsonnetImportFixture(t *testing.T, root string) argoappv1.Application {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "app", "main.jsonnet"), `local shared = import '../shared/lib.libsonnet';
local text = importstr '../shared/text.txt';
local linked = import 'linked.libsonnet';
{
  apiVersion: 'v1',
  kind: 'ConfigMap',
  metadata: { name: 'demo' },
  data: { name: shared.name, nested: shared.nested, text: text, linked: linked.name },
}
`)
	writeTestFile(t, filepath.Join(root, "shared", "lib.libsonnet"), "{ name: 'shared', nested: (import 'nested.libsonnet').value }\n")
	writeTestFile(t, filepath.Join(root, "shared", "nested.libsonnet"), "{ value: 'nested' }\n")
	writeTestFile(t, filepath.Join(root, "shared", "text.txt"), "text")
	writeTestFile(t, filepath.Join(root, "other", "target.libsonnet"), "{ name: 'linked' }\n")
	symlinkBoundsLink(t, "../other/target.libsonnet", filepath.Join(root, "app", "linked.libsonnet"))
	writeTestFile(t, filepath.Join(root, "unrelated", "README.md"), "not a render input\n")
	return argoappv1.Application{
		Name:      "demo",
		Namespace: "argocd",
		Spec: argoappv1.ApplicationSpec{
			Source: &argoappv1.ApplicationSource{
				RepoURL: "https://git.example.test/org/repo.git", Path: "app", TargetRevision: "main",
				Directory: &argoappv1.ApplicationSourceDirectory{},
			},
			Destination: argoappv1.ApplicationDestination{Namespace: "default"},
		},
	}
}

// TestRenderInputCoverageDirectoryJsonnetImports pins that the persistent
// render cache digest covers the files that a Jsonnet file imports from
// outside the source path and its libraries.
func TestRenderInputCoverageDirectoryJsonnetImports(t *testing.T) {
	root := t.TempDir()
	application := writeJsonnetImportFixture(t, root)
	assertRenderInputCoverage(t, root, application)
}

// TestWithSelectionOnlyPathsOwnsJsonnetImports pins that changed-only
// selection owns the files that a Jsonnet file imports from outside the
// source path, and the candidates of an import that a new file would win.
func TestWithSelectionOnlyPathsOwnsJsonnetImports(t *testing.T) {
	root := t.TempDir()
	application := writeJsonnetImportFixture(t, root)
	got := withSelectionOnlyPaths(context.Background(), root, nil, selfRepoRefs{}, []ApplicationSelectionInput{{Application: application}}).inputs
	for _, changed := range []string{"shared/lib.libsonnet", "shared/nested.libsonnet", "shared/text.txt", "other/target.libsonnet"} {
		if selected, _ := SelectChangedApplicationInputs(got, []string{changed}); len(selected) != 1 {
			t.Errorf("change to %s selected %d Applications, want the Jsonnet source (paths %v)", changed, len(selected), got[0].Paths)
		}
	}
	if selected, _ := SelectChangedApplicationInputs(got, []string{"unrelated/README.md"}); len(selected) != 0 {
		t.Errorf("an unrelated change selected the Jsonnet source")
	}
}
