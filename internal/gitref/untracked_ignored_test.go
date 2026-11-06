package gitref

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
)

// filterModes runs a test with the git CLI and with the go-git fallback,
// which an empty PATH selects.
func filterModes(t *testing.T, run func(t *testing.T, cli bool)) {
	t.Helper()
	t.Run("git CLI", func(t *testing.T) {
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git not installed")
		}
		run(t, true)
	})
	t.Run("go-git fallback", func(t *testing.T) {
		t.Setenv("PATH", "")
		run(t, false)
	})
}

// isolateGitUserConfig points Git and go-git at an empty user home, so the
// tests do not read the user's own ignore rules.
func isolateGitUserConfig(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	return home
}

// TestUntrackedIgnoredFilterSkipsOnlyIgnoredUntrackedPaths pins which
// worktree paths the out-of-bounds symlink check leaves out: a path that Git
// ignores and does not track. A tracked path is kept even when an ignore
// rule matches it, and so is a directory that holds one.
func TestUntrackedIgnoredFilterSkipsOnlyIgnoredUntrackedPaths(t *testing.T) {
	filterModes(t, func(t *testing.T, cli bool) {
		isolateGitUserConfig(t)
		root, _, wt := newSnapshotRepo(t)
		writeChangeSetFile(t, filepath.Join(root, ".gitignore"), "ignored/\n*.tmp\nforced/\n")
		writeChangeSetFile(t, filepath.Join(root, "apps", "cm.yaml"), "a: 1\n")
		writeChangeSetFile(t, filepath.Join(root, "forced", "kept.tmp"), "a: 1\n")
		commitAllSnapshotFiles(t, wt, "fixture")
		if _, err := wt.Add("forced/kept.tmp"); err != nil {
			t.Fatalf("Add() error = %v", err)
		}
		writeChangeSetFile(t, filepath.Join(root, "apps", "new.yaml"), "a: 1\n")
		writeChangeSetFile(t, filepath.Join(root, "ignored", "x"), "a: 1\n")
		writeChangeSetFile(t, filepath.Join(root, "apps", "scratch.tmp"), "a: 1\n")
		writeChangeSetFile(t, filepath.Join(root, "forced", "other.tmp"), "a: 1\n")

		filter, err := OpenUntrackedIgnoredFilter(t.Context(), root)
		if err != nil || filter == nil {
			t.Fatalf("OpenUntrackedIgnoredFilter() = %v, %v", filter, err)
		}
		for _, tc := range []struct {
			rel   string
			isDir bool
			want  bool
		}{
			{rel: "apps", isDir: true},
			{rel: "apps/cm.yaml"},
			{rel: "apps/new.yaml"},
			// Only Git skips a whole directory; the fallback decides file
			// by file.
			{rel: "ignored", isDir: true, want: cli},
			{rel: "ignored/x", want: true},
			{rel: "apps/scratch.tmp", want: true},
			{rel: "forced", isDir: true},
			{rel: "forced/kept.tmp"},
			{rel: "forced/other.tmp", want: true},
		} {
			if got := filter.Skip(tc.rel, tc.isDir); got != tc.want {
				t.Errorf("Skip(%q, %t) = %t, want %t", tc.rel, tc.isDir, got, tc.want)
			}
		}
	})
}

func TestUntrackedIgnoredFilterSkipsNothingOutsideAWorktree(t *testing.T) {
	root := t.TempDir()
	writeChangeSetFile(t, filepath.Join(root, ".gitignore"), "*\n")
	writeChangeSetFile(t, filepath.Join(root, "anything"), "a: 1\n")
	filter, err := OpenUntrackedIgnoredFilter(t.Context(), root)
	if err != nil {
		t.Fatalf("OpenUntrackedIgnoredFilter() error = %v", err)
	}
	if filter.Skip("anything", false) || filter.Skip("dir", true) {
		t.Fatal("the filter of a directory that is not a worktree skipped a path")
	}
}

// TestUntrackedIgnoredFilterMapsASubdirectoryRoot pins a root in a
// subdirectory of a worktree: the ignore rules of the worktree apply to it,
// relative to the worktree top level. A root that Git ignores itself is
// checked whole.
func TestUntrackedIgnoredFilterMapsASubdirectoryRoot(t *testing.T) {
	filterModes(t, func(t *testing.T, _ bool) {
		isolateGitUserConfig(t)
		top, _, wt := newSnapshotRepo(t)
		writeChangeSetFile(t, filepath.Join(top, ".gitignore"), ".venv/\ngenerated/\n")
		writeChangeSetFile(t, filepath.Join(top, "gitops", "app", "cm.yaml"), "a: 1\n")
		commitAllSnapshotFiles(t, wt, "fixture")
		writeChangeSetFile(t, filepath.Join(top, "gitops", ".venv", "bin", "python"), "x\n")
		writeChangeSetFile(t, filepath.Join(top, "generated", "app", "cm.yaml"), "a: 1\n")

		filter, err := OpenUntrackedIgnoredFilter(t.Context(), filepath.Join(top, "gitops"))
		if err != nil {
			t.Fatal(err)
		}
		if !filter.Skip(".venv/bin/python", false) {
			t.Fatal("Skip(.venv) = false, want the worktree ignore rules applied to the subdirectory root")
		}
		if filter.Skip("app/cm.yaml", false) {
			t.Fatal("Skip(app/cm.yaml) = true, want a tracked file kept")
		}
		ignoredRoot, err := OpenUntrackedIgnoredFilter(t.Context(), filepath.Join(top, "generated"))
		if err != nil {
			t.Fatal(err)
		}
		if ignoredRoot.Skip("app/cm.yaml", false) || ignoredRoot.Skip("app", true) {
			t.Fatal("a root that Git ignores skipped a path, want it checked whole")
		}
	})
}

// TestUntrackedIgnoredFilterNeverSkipsSubmodulePaths pins that paths at or
// under a gitlink are kept. The parent index does not list the files of a
// submodule, but Argo CD checks out submodules, so a tracked submodule file
// that a parent ignore rule matches is in every checkout.
func TestUntrackedIgnoredFilterNeverSkipsSubmodulePaths(t *testing.T) {
	filterModes(t, func(t *testing.T, _ bool) {
		isolateGitUserConfig(t)
		root, repo, wt := newSnapshotRepo(t)
		writeChangeSetFile(t, filepath.Join(root, ".gitignore"), "result\n")
		commitAllSnapshotFiles(t, wt, "fixture")
		commitSubmoduleEntryForTest(t, repo, wt, "sub")
		writeChangeSetFile(t, filepath.Join(root, "sub", "result"), "x\n")
		writeChangeSetFile(t, filepath.Join(root, "sub", "deep", "result"), "x\n")
		writeChangeSetFile(t, filepath.Join(root, "result"), "x\n")
		filter, err := OpenUntrackedIgnoredFilter(t.Context(), root)
		if err != nil {
			t.Fatalf("OpenUntrackedIgnoredFilter() error = %v", err)
		}
		for _, tc := range []struct {
			rel   string
			isDir bool
		}{
			{rel: "sub", isDir: true},
			{rel: "sub/result"},
			{rel: "sub/deep/result"},
		} {
			if filter.Skip(tc.rel, tc.isDir) {
				t.Errorf("Skip(%q, %t) = true, want a submodule path kept", tc.rel, tc.isDir)
			}
		}
		if !filter.Skip("result", false) {
			t.Error("Skip(result) = false, want the ignored parent path skipped")
		}
	})
}

// TestUntrackedIgnoredFilterUserIgnoreFile pins the user ignore rules. The
// git CLI applies them as Git does, so the default user ignore file
// applies when core.excludesFile is not set. The go-git fallback leaves
// them out, so it only ever skips fewer paths than Git.
func TestUntrackedIgnoredFilterUserIgnoreFile(t *testing.T) {
	filterModes(t, func(t *testing.T, cli bool) {
		home := isolateGitUserConfig(t)
		writeChangeSetFile(t, filepath.Join(home, ".config", "git", "ignore"), "result\n")
		root, _, wt := newSnapshotRepo(t)
		writeChangeSetFile(t, filepath.Join(root, "apps", "cm.yaml"), "a: 1\n")
		commitAllSnapshotFiles(t, wt, "fixture")
		writeChangeSetFile(t, filepath.Join(root, "result"), "x\n")
		filter, err := OpenUntrackedIgnoredFilter(t.Context(), root)
		if err != nil {
			t.Fatal(err)
		}
		if got := filter.Skip("result", false); got != cli {
			t.Fatalf("Skip(result) = %t, want %t", got, cli)
		}
	})
}

// TestUntrackedIgnoredFilterReadsInfoExcludeOfALinkedWorktree pins that a
// linked worktree, whose .git is a file, reads info/exclude of the common
// Git directory.
func TestUntrackedIgnoredFilterReadsInfoExcludeOfALinkedWorktree(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	isolateGitUserConfig(t)
	main, _, wt := newSnapshotRepo(t)
	writeChangeSetFile(t, filepath.Join(main, "apps", "cm.yaml"), "a: 1\n")
	commitAllSnapshotFiles(t, wt, "fixture")
	writeChangeSetFile(t, filepath.Join(main, ".git", "info", "exclude"), "local-only/\n")
	linked := filepath.Join(t.TempDir(), "linked")
	if out, err := exec.Command(gitBin, "-C", main, "worktree", "add", "-q", linked).CombinedOutput(); err != nil {
		t.Skipf("git worktree add: %v: %s", err, out)
	}
	writeChangeSetFile(t, filepath.Join(linked, "local-only", "x"), "x\n")
	filterModes(t, func(t *testing.T, _ bool) {
		filter, err := OpenUntrackedIgnoredFilter(t.Context(), linked)
		if err != nil {
			t.Fatalf("OpenUntrackedIgnoredFilter() error = %v", err)
		}
		if !filter.Skip("local-only/x", false) {
			t.Fatal("Skip(local-only/x) = false, want info/exclude of the common Git directory read")
		}
	})
}

// TestUntrackedIgnoredFilterIgnoresInheritedGitEnvironment pins that the git
// CLI call reads the worktree it is given, whatever GIT_DIR or
// GIT_INDEX_FILE the process inherited, as in a Git hook. A force-added,
// tracked file under an ignored directory is kept.
func TestUntrackedIgnoredFilterIgnoresInheritedGitEnvironment(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	isolateGitUserConfig(t)
	root, _, wt := newSnapshotRepo(t)
	writeChangeSetFile(t, filepath.Join(root, ".gitignore"), "vendor/\n")
	writeChangeSetFile(t, filepath.Join(root, "apps", "cm.yaml"), "a: 1\n")
	commitAllSnapshotFiles(t, wt, "fixture")
	writeChangeSetFile(t, filepath.Join(root, "vendor", "kept.yaml"), "a: 1\n")
	if _, err := wt.Add("vendor/kept.yaml"); err != nil {
		t.Fatal(err)
	}
	other, _, otherWt := newSnapshotRepo(t)
	writeChangeSetFile(t, filepath.Join(other, "x"), "x\n")
	commitAllSnapshotFiles(t, otherWt, "other")
	for name, value := range map[string]string{
		"GIT_INDEX_FILE": filepath.Join(t.TempDir(), "empty-index"),
		"GIT_DIR":        filepath.Join(other, ".git"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, value)
			filter, err := OpenUntrackedIgnoredFilter(t.Context(), root)
			if err != nil {
				t.Fatal(err)
			}
			if filter.git == nil {
				t.Fatal("the filter did not use the git CLI")
			}
			if filter.Skip("vendor/kept.yaml", false) || filter.Skip("vendor", true) {
				t.Fatalf("the filter skipped a tracked file because of the inherited %s", name)
			}
		})
	}
}

// TestUntrackedIgnoredFilterFallsBackWhenGitBlocks pins that a git CLI call
// that blocks, here on a FIFO named by core.excludesFile, falls back to the
// go-git rules after gitCLITimeout.
func TestUntrackedIgnoredFilterFallsBackWhenGitBlocks(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	isolateGitUserConfig(t)
	root, repo, wt := newSnapshotRepo(t)
	writeChangeSetFile(t, filepath.Join(root, ".gitignore"), "ignored/\n")
	writeChangeSetFile(t, filepath.Join(root, "apps", "cm.yaml"), "a: 1\n")
	commitAllSnapshotFiles(t, wt, "fixture")
	writeChangeSetFile(t, filepath.Join(root, "ignored", "x"), "x\n")
	fifo := filepath.Join(t.TempDir(), "excludes")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	cfg, err := repo.Config()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Raw.Section("core").SetOption("excludesFile", fifo)
	if err := repo.Storer.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	previous := gitCLITimeout
	gitCLITimeout = 500 * time.Millisecond
	t.Cleanup(func() { gitCLITimeout = previous })

	start := time.Now()
	filter, err := OpenUntrackedIgnoredFilter(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("OpenUntrackedIgnoredFilter() took %s, want it bounded", elapsed)
	}
	if filter.git != nil {
		t.Fatal("the filter used the blocked git CLI, want the go-git fallback")
	}
	if !filter.Skip("ignored/x", false) {
		t.Fatal("Skip(ignored/x) = false, want the go-git rules applied")
	}
}

// TestUntrackedIgnoredFilterSkipsNothingInAnUntrackedSubdirectory pins that a
// root inside a worktree that does not track it, such as a temporary
// directory under a repository, gets no ignore rules from that worktree.
func TestUntrackedIgnoredFilterSkipsNothingInAnUntrackedSubdirectory(t *testing.T) {
	filterModes(t, func(t *testing.T, _ bool) {
		isolateGitUserConfig(t)
		top, _, wt := newSnapshotRepo(t)
		writeChangeSetFile(t, filepath.Join(top, ".gitignore"), "*.yaml\n")
		writeChangeSetFile(t, filepath.Join(top, "README"), "x\n")
		commitAllSnapshotFiles(t, wt, "fixture")
		writeChangeSetFile(t, filepath.Join(top, "tmp", "snapshot", "escape.yaml"), "x\n")
		filter, err := OpenUntrackedIgnoredFilter(t.Context(), filepath.Join(top, "tmp", "snapshot"))
		if err != nil {
			t.Fatal(err)
		}
		if filter.Skip("escape.yaml", false) {
			t.Fatal("the filter of an untracked subdirectory skipped a path")
		}
	})
}

// TestUntrackedIgnoredFilterReadsIgnoreFilesLikeGit pins two Git rules that
// the go-git fallback must keep, so it never skips more than Git: Git does
// not read a .gitignore that is a symlink, and it does not read the
// .gitignore files of a nested repository.
func TestUntrackedIgnoredFilterReadsIgnoreFilesLikeGit(t *testing.T) {
	filterModes(t, func(t *testing.T, _ bool) {
		isolateGitUserConfig(t)
		root, _, wt := newSnapshotRepo(t)
		writeChangeSetFile(t, filepath.Join(root, "apps", "cm.yaml"), "a: 1\n")
		writeChangeSetFile(t, filepath.Join(root, "rules"), "linked-rule\n")
		commitAllSnapshotFiles(t, wt, "fixture")
		if err := os.Symlink("../rules", filepath.Join(root, "apps", ".gitignore")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		writeChangeSetFile(t, filepath.Join(root, "apps", "linked-rule"), "x\n")
		nested, _, nestedWt := newSnapshotRepoAt(t, filepath.Join(root, "nested"))
		writeChangeSetFile(t, filepath.Join(nested, ".gitignore"), "nested-rule\n")
		commitAllSnapshotFiles(t, nestedWt, "nested")
		writeChangeSetFile(t, filepath.Join(nested, "nested-rule"), "x\n")

		filter, err := OpenUntrackedIgnoredFilter(t.Context(), root)
		if err != nil {
			t.Fatal(err)
		}
		if filter.Skip("apps/linked-rule", false) {
			t.Error("Skip(apps/linked-rule) = true, want a symlinked .gitignore not read")
		}
		if filter.Skip("nested/nested-rule", false) {
			t.Error("Skip(nested/nested-rule) = true, want the .gitignore of a nested repository not read")
		}
	})
}

func newSnapshotRepoAt(t *testing.T, root string) (string, *git.Repository, *git.Worktree) {
	t.Helper()
	repo, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatalf("PlainInit() error = %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree() error = %v", err)
	}
	return root, repo, wt
}

// TestUntrackedIgnoredFilterOpensRepositoriesThatGoGitRefuses pins that the
// git CLI path also serves a repository that go-git v5 cannot open: one
// with reftable refs, and one with extensions.worktreeConfig, which git
// sparse-checkout sets. An ignored, untracked virtual environment is left
// out there too.
func TestUntrackedIgnoredFilterOpensRepositoriesThatGoGitRefuses(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	isolateGitUserConfig(t)
	run := func(t *testing.T, dir string, args ...string) {
		t.Helper()
		full := append([]string{"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-C", dir}, args...)
		if out, err := exec.Command(gitBin, full...).CombinedOutput(); err != nil {
			t.Skipf("git %v: %v: %s", args, err, out)
		}
	}
	for name, setup := range map[string]func(t *testing.T, dir string){
		"reftable": func(t *testing.T, dir string) {
			run(t, dir, "init", "-q", "--ref-format=reftable")
		},
		"worktreeConfig": func(t *testing.T, dir string) {
			run(t, dir, "init", "-q")
			run(t, dir, "config", "core.repositoryformatversion", "1")
			run(t, dir, "config", "extensions.worktreeConfig", "true")
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			setup(t, root)
			if _, err := git.PlainOpen(root); err == nil {
				t.Log("go-git opened the repository; the test still checks the git CLI path")
			}
			writeChangeSetFile(t, filepath.Join(root, ".gitignore"), ".venv/\n")
			writeChangeSetFile(t, filepath.Join(root, "gitops", "app", "cm.yaml"), "a: 1\n")
			run(t, root, "add", ".")
			run(t, root, "commit", "-q", "-m", "fixture")
			writeChangeSetFile(t, filepath.Join(root, "gitops", ".venv", "bin", "python"), "x\n")
			for _, base := range []string{root, filepath.Join(root, "gitops")} {
				filter, err := OpenUntrackedIgnoredFilter(t.Context(), base)
				if err != nil {
					t.Fatalf("OpenUntrackedIgnoredFilter(%s) error = %v", base, err)
				}
				rel := ".venv"
				if base == root {
					rel = "gitops/.venv"
				}
				if !filter.Skip(rel, true) || !filter.Skip(rel+"/bin/python", false) {
					t.Fatalf("Skip(%s) from %s = false, want the ignored virtual environment left out", rel, base)
				}
			}
		})
	}
}

// TestUntrackedIgnoredFilterNeverSkipsMoreThanGit pins two go-git gaps that
// the fallback closes. A "!" pattern can bring back a file below a
// directory that an earlier pattern matches, so the fallback decides file by
// file. Git negates a bracket expression with "[!", which go-git reads as a
// plain "!", so the fallback rewrites it.
func TestUntrackedIgnoredFilterNeverSkipsMoreThanGit(t *testing.T) {
	filterModes(t, func(t *testing.T, _ bool) {
		isolateGitUserConfig(t)
		root, _, wt := newSnapshotRepo(t)
		writeChangeSetFile(t, filepath.Join(root, ".gitignore"), "foo/**\n!foo/keep.yaml\nbar[!a]\nbaz/**\nqux/**/\nsub/**\n")
		writeChangeSetFile(t, filepath.Join(root, "sub", ".gitignore"), "!keep.yaml\n")
		writeChangeSetFile(t, filepath.Join(root, "apps", "cm.yaml"), "a: 1\n")
		commitAllSnapshotFiles(t, wt, "fixture")
		writeChangeSetFile(t, filepath.Join(root, "foo", "keep.yaml"), "x\n")
		writeChangeSetFile(t, filepath.Join(root, "foo", "drop.yaml"), "x\n")
		writeChangeSetFile(t, filepath.Join(root, "bara"), "x\n")
		writeChangeSetFile(t, filepath.Join(root, "barb"), "x\n")
		// "baz/**" needs a part after baz, so a file named baz is kept.
		writeChangeSetFile(t, filepath.Join(root, "baz"), "x\n")
		// "qux/**/" matches directories below qux, not a file in it.
		writeChangeSetFile(t, filepath.Join(root, "qux", "direct"), "x\n")
		writeChangeSetFile(t, filepath.Join(root, "qux", "deeper", "x"), "x\n")
		// "sub/**" does not match sub, so Git reads sub/.gitignore and
		// brings keep.yaml back.
		writeChangeSetFile(t, filepath.Join(root, "sub", "keep.yaml"), "x\n")
		writeChangeSetFile(t, filepath.Join(root, "sub", "drop.yaml"), "x\n")
		filter, err := OpenUntrackedIgnoredFilter(t.Context(), root)
		if err != nil {
			t.Fatal(err)
		}
		for rel, want := range map[string]bool{
			"foo/keep.yaml": false,
			"foo/drop.yaml": true,
			"bara":          false,
			"barb":          true,
			"baz":           false,
			"qux/direct":    false,
			"qux/deeper/x":  true,
			"sub/keep.yaml": false,
			"sub/drop.yaml": true,
		} {
			if got := filter.Skip(rel, false); got != want {
				t.Errorf("Skip(%s) = %t, want %t", rel, got, want)
			}
		}
		if filter.Skip("foo", true) {
			t.Error("Skip(foo) = true, want a directory with a kept file walked")
		}
	})
}
