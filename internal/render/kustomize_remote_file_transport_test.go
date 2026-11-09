package render

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/kyaml/filesys"
)

// TestKustomizeRendererRefusesFileTransportRemoteBases checks that a
// Kustomize ref that names the file transport fails with a clear error in
// every field that takes a remote ref, before drydock acquires anything.
// kustomize clones such a ref from the local file system
// (TestUpstreamKustomizeClonesFileRemoteBase), so this is a recorded
// divergence (docs/compatibility.md).
func TestKustomizeRendererRefusesFileTransportRemoteBases(t *testing.T) {
	refs := []string{
		"file:///tmp/repo//base",
		"file:///tmp/repo.git//base?ref=main",
		"FILE:///tmp/repo.git//base",
		"git::file:///tmp/repo.git//base",
		"GIT::file:///tmp/repo.git//base",
		"file://localhost/tmp/repo.git//base",
		"file:/tmp/repo.git//base",
		"file::/tmp/repo.git//base",
	}
	fields := map[string]string{
		"resources":  "resources:\n  - %s\n",
		"components": "components:\n  - %s\n",
		"patches":    "patches:\n  - path: %s\n",
	}
	for _, ref := range refs {
		for field, body := range fields {
			t.Run(field+"/"+ref, func(t *testing.T) {
				root := t.TempDir()
				writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), strings.Replace(body, "%s", ref, 1))
				acquirer := &fakeRemoteAcquirer{path: t.TempDir()}
				_, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
					RepoRoot: root,
					Path:     "app",
				}, RenderOptions{
					RemoteResourceAcquirer: acquirer,
					RemoteResourceCacheDir: t.TempDir(),
				})
				if err == nil || !strings.Contains(err.Error(), "uses the local file transport") {
					t.Fatalf("Render() error = %v, want file transport refusal", err)
				}
				if len(acquirer.requests) != 0 {
					t.Fatalf("remote acquire calls = %d, want 0", len(acquirer.requests))
				}
			})
		}
	}
}

// TestUpstreamKustomizeClonesFileRemoteBase is the upstream side of the
// divergence above: kustomize v5.8.3, the version that Argo CD v3.6.1
// ships, clones a file:// remote base with the git program
// (sigs.k8s.io/kustomize/api internal/git/repospec.go extractHost and
// cloner.go ClonerUsingGitExec). Argo CD runs that kustomize in the
// repo-server, so it reads Git repositories on the repo-server's file
// system.
func TestUpstreamKustomizeClonesFileRemoteBase(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	origin := t.TempDir()
	writeFile(t, filepath.Join(origin, "base", "kustomization.yaml"), "resources:\n  - configmap.yaml\n")
	writeFile(t, filepath.Join(origin, "base", "configmap.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: from-local-repository\n")
	for _, args := range [][]string{
		{"init", "-q"},
		{"add", "."},
		{"-c", "user.email=drydock@example.invalid", "-c", "user.name=drydock", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "fixture"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = origin
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v error = %v: %s", args, err, out)
		}
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "kustomization.yaml"), "resources:\n  - file://"+filepath.ToSlash(origin)+"//base\n")
	resMap, err := krusty.MakeKustomizer(krusty.MakeDefaultOptions()).Run(filesys.MakeFsOnDisk(), dir)
	if err != nil {
		t.Fatalf("kustomize build error = %v", err)
	}
	built, err := resMap.AsYaml()
	if err != nil {
		t.Fatalf("AsYaml() error = %v", err)
	}
	if !strings.Contains(string(built), "name: from-local-repository") {
		t.Fatalf("kustomize build = %s, want the ConfigMap of the local repository", built)
	}
}
