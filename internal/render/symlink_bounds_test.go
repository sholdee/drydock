package render

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	apppathutil "github.com/argoproj/argo-cd/v3/util/app/path"
	"sigs.k8s.io/yaml"
)

// TestFindOutOfBoundsSymlinkMatchesUpstream runs drydock's walk and the
// upstream CheckOutOfBoundsSymlinks (Argo CD v3.6.1 util/app/path/
// path.go:51-110) on the same trees and requires the same verdict and the
// same first file.
func TestFindOutOfBoundsSymlinkMatchesUpstream(t *testing.T) {
	for _, tc := range []struct {
		name  string
		links map[string]string
		files []string
		want  string
	}{
		{name: "no symlinks", files: []string{"a/b.yaml"}},
		{name: "in-bounds sibling", files: []string{"b/t.yaml"}, links: map[string]string{"a/l.yaml": "../b/t.yaml"}},
		{name: "in-bounds directory", files: []string{"b/t.yaml"}, links: map[string]string{"a/l": "../b"}},
		{name: "in-bounds missing target", links: map[string]string{"a/l.yaml": "../missing.yaml"}},
		{name: "absolute target", links: map[string]string{"a/l.yaml": "/etc/hosts"}, want: "a/l.yaml"},
		{name: "relative target outside", links: map[string]string{"l.yaml": "../outside.yaml"}, want: "l.yaml"},
		{name: "a step leaves the root and comes back", files: []string{"t.yaml"}, links: map[string]string{"a/l.yaml": "../../" + "ROOT" + "/t.yaml"}, want: "a/l.yaml"},
		{name: "a parent step inside the target stays in bounds", files: []string{"a/t.yaml"}, links: map[string]string{"a/l.yaml": "x/../t.yaml"}},
		{name: "first in walk order wins", links: map[string]string{"a/b/l.yaml": "/x", "a.b/l.yaml": "/y", "z.yaml": "/z"}, want: "a/b/l.yaml"},
		{name: "root .git directory is skipped", files: []string{".git/HEAD"}, links: map[string]string{".git/l": "/etc/hosts"}},
		{name: "nested .git directory is walked", links: map[string]string{"a/.git/l": "/etc/hosts"}, want: "a/.git/l"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent := t.TempDir()
			root := filepath.Join(parent, "repo")
			if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, file := range tc.files {
				writeFile(t, filepath.Join(root, file), "x: 1\n")
			}
			for link, target := range tc.links {
				symlink(t, strings.ReplaceAll(target, "ROOT", "repo"), filepath.Join(root, link))
			}
			upstream := ""
			if err := apppathutil.CheckOutOfBoundsSymlinks(root, ".git"); err != nil {
				var oob *apppathutil.OutOfBoundsSymlinkError
				if !errors.As(err, &oob) {
					t.Fatalf("upstream error = %v", err)
				}
				upstream = filepath.ToSlash(oob.File)
			}
			got, err := FindOutOfBoundsSymlink(t.Context(), root, OutOfBoundsSymlinkOptions{SkipRootGit: true})
			if err != nil {
				t.Fatalf("FindOutOfBoundsSymlink() error = %v", err)
			}
			if upstream != tc.want || got != tc.want {
				t.Fatalf("upstream = %q, drydock = %q, want %q", upstream, got, tc.want)
			}
		})
	}
}

// TestCheckOutOfBoundsSymlinksErrorHoldsUpstreamText pins the repo-server
// message for each scope (reposerver/repository/repository.go:442, 483, and
// 512 at v3.6.1).
func TestCheckOutOfBoundsSymlinksErrorHoldsUpstreamText(t *testing.T) {
	root := t.TempDir()
	symlink(t, "/etc/hosts", filepath.Join(root, "dir", "escape.yaml"))
	for scope, want := range map[OutOfBoundsSymlinkScope]string{
		OutOfBoundsSymlinkScopeRepository: "repository contains out-of-bounds symlinks. file: dir/escape.yaml",
		OutOfBoundsSymlinkScopeOCI:        "oci image contains out-of-bounds symlinks. file: dir/escape.yaml",
		OutOfBoundsSymlinkScopeChart:      "chart contains out-of-bounds symlinks. file: dir/escape.yaml",
	} {
		err := CheckOutOfBoundsSymlinks(t.Context(), root, scope, OutOfBoundsSymlinkOptions{})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("CheckOutOfBoundsSymlinks(%s) error = %v, want %q", scope, err, want)
		}
	}
}

// TestFindOutOfBoundsSymlinkSkipsRootGitFile pins that a .git file at the
// root, as in a linked Git worktree, is skipped without skipping the rest
// of the root. filepath.Walk would skip the rest of the directory if the
// repo-server's SkipDir hit a file; an Argo CD checkout always has a .git
// directory there.
func TestFindOutOfBoundsSymlinkSkipsRootGitFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git"), "gitdir: /elsewhere\n")
	symlink(t, "/etc/hosts", filepath.Join(root, "z.yaml"))
	got, err := FindOutOfBoundsSymlink(t.Context(), root, OutOfBoundsSymlinkOptions{SkipRootGit: true})
	if err != nil || got != "z.yaml" {
		t.Fatalf("FindOutOfBoundsSymlink() = %q, %v, want z.yaml", got, err)
	}
	skipped, err := FindOutOfBoundsSymlink(t.Context(), root, OutOfBoundsSymlinkOptions{
		SkipRootGit: true,
		Skip:        func(rel string, _ bool) bool { return rel == "z.yaml" },
	})
	if err != nil || skipped != "" {
		t.Fatalf("FindOutOfBoundsSymlink() with Skip = %q, %v, want none", skipped, err)
	}
}

// TestOutOfBoundsSymlinkSmokeFixture builds the oob-symlink repository of
// the Argo CD render parity smoke as the smoke does, and requires the
// message that the smoke expects from drydock and from the upstream
// CheckOutOfBoundsSymlinks with the repo-server's wording
// (reposerver/repository/repository.go:512 at v3.6.1).
func TestOutOfBoundsSymlinkSmokeFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "argocd-parity", "directory-decode-expected.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var expected struct {
		Cases []parityDecodeCase `json:"cases"`
	}
	if err := yaml.Unmarshal(data, &expected); err != nil {
		t.Fatal(err)
	}
	var tc parityDecodeCase
	for _, candidate := range expected.Cases {
		if candidate.Repository == "oob-symlink" {
			tc = candidate
		}
	}
	if tc.Name == "" {
		t.Fatal("no expected case uses the oob-symlink repository")
	}
	root := t.TempDir()
	if err := os.CopyFS(root, os.DirFS(filepath.Join("..", "..", "testdata", "argocd-parity", "oob-symlink-repo"))); err != nil {
		t.Fatal(err)
	}
	// prepare_oob_symlink_repo in scripts/argocd-parity-smoke.sh adds it.
	symlink(t, "/etc/hosts", filepath.Join(root, "other", "escape.yaml"))

	upstreamErr := apppathutil.CheckOutOfBoundsSymlinks(root, ".git")
	var oob *apppathutil.OutOfBoundsSymlinkError
	if !errors.As(upstreamErr, &oob) {
		t.Fatalf("upstream error = %v, want an out-of-bounds symlink", upstreamErr)
	}
	if got := fmt.Sprintf("repository contains out-of-bounds symlinks. file: %s", oob.File); !strings.Contains(got, tc.ArgoCDMessage) {
		t.Fatalf("upstream message = %q, want %q", got, tc.ArgoCDMessage)
	}
	drydockErr := CheckOutOfBoundsSymlinks(t.Context(), root, OutOfBoundsSymlinkScopeRepository, OutOfBoundsSymlinkOptions{SkipRootGit: true})
	if drydockErr == nil || !strings.Contains(drydockErr.Error(), tc.DrydockMessage) {
		t.Fatalf("drydock error = %v, want %q", drydockErr, tc.DrydockMessage)
	}
}

// TestFindOutOfBoundsSymlinkFollowsASymlinkedRoot pins that a root which is
// itself a symlink, such as a --repo-map path, is checked through: the walk
// would otherwise stop at the root and find nothing.
func TestFindOutOfBoundsSymlinkFollowsASymlinkedRoot(t *testing.T) {
	parent := t.TempDir()
	symlink(t, "/etc/hosts", filepath.Join(parent, "real", "app", "bad.yaml"))
	symlink(t, "real", filepath.Join(parent, "link"))
	for _, root := range []string{filepath.Join(parent, "link"), filepath.Join(parent, "link") + string(filepath.Separator)} {
		got, err := FindOutOfBoundsSymlink(t.Context(), root, OutOfBoundsSymlinkOptions{SkipRootGit: true})
		if err != nil || got != "app/bad.yaml" {
			t.Fatalf("FindOutOfBoundsSymlink(%q) = %q, %v, want app/bad.yaml", root, got, err)
		}
	}
}
