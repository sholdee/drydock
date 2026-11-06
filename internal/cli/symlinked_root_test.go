package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRejectBroadDiagPathFollowsSymlinks pins that a --path symlink to the
// filesystem root or to the home directory is refused like the directory
// itself, because the scan reads the tree it points to.
func TestRejectBroadDiagPathFollowsSymlinks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	links := t.TempDir()
	for target, want := range map[string]string{
		"/":  "refusing to recursively scan filesystem root",
		home: "refusing to recursively scan home directory",
	} {
		link := filepath.Join(links, strings.ReplaceAll(strings.Trim(target, "/"), "/", "_")+"link")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if err := rejectBroadDiagPath(link); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("rejectBroadDiagPath(link to %s) error = %v, want %q", target, err, want)
		}
	}
	repo := filepath.Join(home, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := rejectBroadDiagPath(repo); err != nil {
		t.Fatalf("rejectBroadDiagPath(repo) error = %v", err)
	}
}

// TestResultRootResolvesASymlinkedRoot pins that plugin onboarding analyzes
// the tree that a --path symlink points to, as the build does.
func TestResultRootResolvesASymlinkedRoot(t *testing.T) {
	parent := t.TempDir()
	if err := os.MkdirAll(filepath.Join(parent, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "link")
	if err := os.Symlink("real", link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	want, err := filepath.EvalSymlinks(link)
	if err != nil {
		t.Fatal(err)
	}
	if got := resultRoot(link); got != want {
		t.Fatalf("resultRoot(%q) = %q, want %q", link, got, want)
	}
	if got := resultRoot(""); got != "." {
		t.Fatalf("resultRoot(\"\") = %q, want .", got)
	}
}
