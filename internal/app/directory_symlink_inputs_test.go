package app

import (
	"context"
	"path/filepath"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
)

// TestRenderInputCoverageDirectorySourceSymlinks pins that the persistent
// render cache digest covers every file outside the source path that a
// symlink in it renders, directly or through a chain. The probe rewrites
// regular files only; TestDirectorySymlinkInputPaths covers a symlinked
// directory on the way.
func TestRenderInputCoverageDirectorySourceSymlinks(t *testing.T) {
	repoRoot := t.TempDir()
	writeTestFile(t, filepath.Join(repoRoot, "manifests", "demo", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\n")
	writeTestFile(t, filepath.Join(repoRoot, "targets", "linked.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: linked\n")
	writeTestFile(t, filepath.Join(repoRoot, "targets", "end.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: chain\n")
	writeTestFile(t, filepath.Join(repoRoot, "unrelated", "README.md"), "not a render input\n")
	symlinkBoundsLink(t, "../../targets/linked.yaml", filepath.Join(repoRoot, "manifests", "demo", "linked.yaml"))
	symlinkBoundsLink(t, "end.yaml", filepath.Join(repoRoot, "targets", "mid.yaml"))
	symlinkBoundsLink(t, "../../targets/mid.yaml", filepath.Join(repoRoot, "manifests", "demo", "chain.yaml"))
	application := argoappv1.Application{
		Name: "demo",
		Spec: argoappv1.ApplicationSpec{
			Source: &argoappv1.ApplicationSource{
				RepoURL: "https://git.example.test/org/repo.git", Path: "manifests/demo", TargetRevision: "main",
				Directory: &argoappv1.ApplicationSourceDirectory{},
			},
			Destination: argoappv1.ApplicationDestination{Namespace: "default"},
		},
	}
	assertRenderInputCoverage(t, repoRoot, application)
}

// TestWithSelectionOnlyPathsOwnsSymlinkTargets pins that changed-only
// selection owns, for a source that renders from the tree, the paths its
// symlinks reach outside its path: a change to a target, to a symlink on
// the way, or the creation of a missing target selects it.
func TestWithSelectionOnlyPathsOwnsSymlinkTargets(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "cm.yaml"), symlinkBoundsConfigMap)
	writeTestFile(t, filepath.Join(root, "targets", "end.yaml"), symlinkBoundsConfigMap)
	writeTestFile(t, filepath.Join(root, "libs", "main.libsonnet"), "{}\n")
	// The libraries count only for a source with a Jsonnet file.
	writeTestFile(t, filepath.Join(root, "apps", "main.jsonnet"), "{ apiVersion: 'v1', kind: 'ConfigMap', metadata: { name: 'x' } }\n")
	symlinkBoundsLink(t, "end.yaml", filepath.Join(root, "targets", "mid.yaml"))
	symlinkBoundsLink(t, "../targets/mid.yaml", filepath.Join(root, "apps", "chain.yaml"))
	symlinkBoundsLink(t, "../targets/missing.yaml", filepath.Join(root, "apps", "dangling.yaml"))
	symlinkBoundsLink(t, "../shared/lib.libsonnet", filepath.Join(root, "libs", "lib.libsonnet"))
	application := argoappv1.Application{
		Name: "demo",
		Spec: argoappv1.ApplicationSpec{Source: &argoappv1.ApplicationSource{
			RepoURL: "https://git.example.test/org/repo.git", TargetRevision: "main", Path: "apps",
			Directory: &argoappv1.ApplicationSourceDirectory{Jsonnet: argoappv1.ApplicationSourceJsonnet{Libs: []string{"libs"}}},
		}},
	}
	got := withSelectionOnlyPaths(context.Background(), root, nil, selfRepoRefs{}, sourceTypeGates{}, []ApplicationSelectionInput{{Application: application}}).inputs
	for _, changed := range []string{"targets/end.yaml", "targets/mid.yaml", "targets/missing.yaml", "shared/lib.libsonnet"} {
		selected, _ := SelectChangedApplicationInputs(got, []string{changed})
		if len(selected) != 1 {
			t.Errorf("change to %s selected %d Applications, want the directory source (paths %v)", changed, len(selected), got[0].Paths)
		}
	}
}
