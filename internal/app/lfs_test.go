package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/sholdee/drydock/internal/gittree"
	"github.com/sholdee/drydock/internal/gittree/submoduletest"
)

const lfsTestRepoURL = "https://git.example.test/org/lfs.git"

const lfsTestConfigMap = `apiVersion: v1
kind: ConfigMap
metadata:
  name: from-lfs
`

// lfsRepo builds a repository whose lfs/cm.yaml is a Git LFS pointer to
// lfsTestConfigMap, with the object in .git/lfs when stored, and whose
// repository Secret sets enableLfs to enableLFS. It returns the repository
// and the commit before the Application that renders lfs/.
func lfsRepo(t *testing.T, enableLFS, stored bool) (string, string) {
	t.Helper()
	root := t.TempDir()
	submoduletest.Run(t, root, "init", "-q")
	submoduletest.Run(t, root, "remote", "add", "origin", lfsTestRepoURL)
	sum := sha256.Sum256([]byte(lfsTestConfigMap))
	oid := hex.EncodeToString(sum[:])
	submoduletest.WriteFile(t, root, "lfs/cm.yaml", fmt.Sprintf("version https://git-lfs.github.com/spec/v1\noid sha256:%s\nsize %d\n", oid, len(lfsTestConfigMap)))
	submoduletest.WriteFile(t, root, "argocd/repo.yaml", fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: lfs-repo
  namespace: argocd
  labels:
    argocd.argoproj.io/secret-type: repository
stringData:
  url: %s
  enableLfs: "%t"
`, lfsTestRepoURL, enableLFS))
	submoduletest.Run(t, root, "add", "-A")
	submoduletest.Run(t, root, "commit", "-q", "-m", "base")
	base := submoduletest.Run(t, root, "rev-parse", "HEAD")
	submoduletest.WriteFile(t, root, "apps/lfs.yaml", `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: lfs
  namespace: argocd
spec:
  project: default
  source:
    repoURL: `+lfsTestRepoURL+`
    targetRevision: HEAD
    path: lfs
  destination:
    name: in-cluster
    namespace: default
`)
	submoduletest.Run(t, root, "add", "-A")
	submoduletest.Run(t, root, "commit", "-q", "-m", "add app")
	if stored {
		object := gittree.LFSObjectPath(filepath.Join(root, ".git", "lfs"), oid)
		submoduletest.WriteFile(t, filepath.Dir(object), filepath.Base(object), lfsTestConfigMap)
	}
	return root, base
}

func diffLFSRepo(t *testing.T, root, base string) DiffResult {
	t.Helper()
	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		RightPath: root,
		Repo:      root,
		Ref:       "HEAD",
		RefOrig:   base,
		Offline:   true,
	})
	if err != nil {
		t.Logf("DiffApps() error = %v", err)
	}
	return result
}

func hasConfigMap(result DiffResult, name string) bool {
	for _, item := range result.Results {
		if item.Resource.Kind == "ConfigMap" && item.Resource.Name == name {
			return true
		}
	}
	return false
}

// TestDiffRefSnapshotChecksOutLFSContent pins that a --ref snapshot of a
// repository whose Secret sets enableLfs renders the content of a Git LFS
// file from the repository's .git/lfs store, as `git lfs checkout` does in
// the Argo CD repo-server.
func TestDiffRefSnapshotChecksOutLFSContent(t *testing.T) {
	root, base := lfsRepo(t, true, true)
	result := diffLFSRepo(t, root, base)
	if !hasConfigMap(result, "from-lfs") || hasDiagnosticCode(result.Diagnostics, lfsPointerCode) {
		t.Fatalf("Results = %#v, Diagnostics = %#v, want the ConfigMap from the LFS object", result.Results, result.Diagnostics)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "lfs", "cm.yaml")); string(data) == lfsTestConfigMap {
		t.Fatal("the working tree file holds the object, want drydock to never write the repository")
	}
}

// TestDiffRefSnapshotWarnsForMissingLFSObject pins that a pointer whose
// object is not in .git/lfs stays a pointer with a warning, never
// silently.
func TestDiffRefSnapshotWarnsForMissingLFSObject(t *testing.T) {
	root, base := lfsRepo(t, true, false)
	result := diffLFSRepo(t, root, base)
	if hasConfigMap(result, "from-lfs") || !hasDiagnosticCode(result.Diagnostics, lfsPointerCode) {
		t.Fatalf("Results = %#v, Diagnostics = %#v, want the pointer and a warning", result.Results, result.Diagnostics)
	}
}

// TestDiffRefSnapshotKeepsLFSPointerWithoutEnableLFS pins that without
// enableLfs the snapshot keeps the pointer file, as the repo-server, which
// runs git with GIT_LFS_SKIP_SMUDGE=1, does.
func TestDiffRefSnapshotKeepsLFSPointerWithoutEnableLFS(t *testing.T) {
	root, base := lfsRepo(t, false, true)
	result := diffLFSRepo(t, root, base)
	if hasConfigMap(result, "from-lfs") || hasDiagnosticCode(result.Diagnostics, lfsPointerCode) {
		t.Fatalf("Results = %#v, Diagnostics = %#v, want the pointer without a warning", result.Results, result.Diagnostics)
	}
}

// TestBuildWarnsForLFSPointerInWorkingTree pins that a working tree file
// that still holds the Git LFS pointer of HEAD renders as it is, since
// drydock never writes the working tree, with a warning when the
// repository sets enableLfs.
func TestBuildWarnsForLFSPointerInWorkingTree(t *testing.T) {
	root, _ := lfsRepo(t, true, true)
	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root, Offline: true})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if !hasDiagnosticCode(result.Diagnostics, lfsPointerCode) {
		t.Fatalf("Diagnostics = %#v, want %s", result.Diagnostics, lfsPointerCode)
	}
}
