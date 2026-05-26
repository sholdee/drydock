package change

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIndexKeepsAllOverlappingApplications(t *testing.T) {
	index := NewIndex()
	index.Add("app-a", []string{"apps/shared"})
	index.Add("app-b", []string{"apps/shared/config"})

	got := index.Match([]string{"apps/shared/config/cm.yaml"})

	wantApps := []string{"app-a", "app-b"}
	if !equal(got.Applications, wantApps) {
		t.Fatalf("Applications = %v, want %v", got.Applications, wantApps)
	}
	if got.RenderAll {
		t.Fatal("RenderAll = true, want false")
	}
}

func TestIndexUnownedFallsBackToRenderAll(t *testing.T) {
	index := NewIndex()
	index.Add("app-a", []string{"apps/a"})

	got := index.Match([]string{"docs/readme.md"})

	if !got.RenderAll {
		t.Fatal("RenderAll = false, want true")
	}
	wantUnowned := []string{"docs/readme.md"}
	if !equal(got.Unowned, wantUnowned) {
		t.Fatalf("Unowned = %v, want %v", got.Unowned, wantUnowned)
	}
}

func TestIndexDeduplicatesUnownedChangedPaths(t *testing.T) {
	index := NewIndex()
	index.Add("app-a", []string{"apps/a"})

	got := index.Match([]string{"docs/readme.md", "docs/readme.md"})

	if !got.RenderAll {
		t.Fatal("RenderAll = false, want true")
	}
	wantUnowned := []string{"docs/readme.md"}
	if !equal(got.Unowned, wantUnowned) {
		t.Fatalf("Unowned = %v, want %v", got.Unowned, wantUnowned)
	}
}

func TestIndexPathPrefixesMatchWholeSegments(t *testing.T) {
	index := NewIndex()
	index.Add("app-a", []string{"apps/a"})

	got := index.Match([]string{"apps/ab/cm.yaml"})

	if len(got.Applications) != 0 {
		t.Fatalf("Applications = %v, want none", got.Applications)
	}
	if !got.RenderAll {
		t.Fatal("RenderAll = false, want true")
	}
	wantUnowned := []string{"apps/ab/cm.yaml"}
	if !equal(got.Unowned, wantUnowned) {
		t.Fatalf("Unowned = %v, want %v", got.Unowned, wantUnowned)
	}
}

func TestIndexRootInputOwnsAllChangedPaths(t *testing.T) {
	index := NewIndex()
	index.Add("app-root", []string{"/"})

	got := index.Match([]string{"apps/a/cm.yaml"})

	wantApps := []string{"app-root"}
	if !equal(got.Applications, wantApps) {
		t.Fatalf("Applications = %v, want %v", got.Applications, wantApps)
	}
	if got.RenderAll {
		t.Fatal("RenderAll = true, want false")
	}
	if len(got.Unowned) != 0 {
		t.Fatalf("Unowned = %v, want none", got.Unowned)
	}
}

func TestDetectChangedPaths(t *testing.T) {
	base := t.TempDir()
	current := t.TempDir()
	writeFile(t, base, "apps/a/cm.yaml", "old")
	writeFile(t, current, "apps/a/cm.yaml", "new")
	writeFile(t, current, "apps/b/cm.yaml", "added")

	got, err := Detect(base, current)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"apps/a/cm.yaml", "apps/b/cm.yaml"}
	if !equal(got, want) {
		t.Fatalf("Detect() = %v, want %v", got, want)
	}
}

func TestDetectReportsDeletedPaths(t *testing.T) {
	base := t.TempDir()
	current := t.TempDir()
	writeFile(t, base, "apps/a/cm.yaml", "old")

	got, err := Detect(base, current)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"apps/a/cm.yaml"}
	if !equal(got, want) {
		t.Fatalf("Detect() = %v, want %v", got, want)
	}
}

func TestDetectSkipsGitDirectories(t *testing.T) {
	base := t.TempDir()
	current := t.TempDir()
	writeFile(t, base, ".git/config", "old")
	writeFile(t, current, ".git/config", "new")

	got, err := Detect(base, current)
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 0 {
		t.Fatalf("Detect() = %v, want none", got)
	}
}

func TestDetectSkipsGitWorktreeFiles(t *testing.T) {
	base := t.TempDir()
	current := t.TempDir()
	writeFile(t, base, ".git", "gitdir: /tmp/base/.git/worktrees/base\n")
	writeFile(t, current, ".git", "gitdir: /tmp/current/.git/worktrees/current\n")
	writeFile(t, base, "apps/a/cm.yaml", "old")
	writeFile(t, current, "apps/a/cm.yaml", "new")

	got, err := Detect(base, current)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"apps/a/cm.yaml"}
	if !equal(got, want) {
		t.Fatalf("Detect() = %v, want %v", got, want)
	}
}

func TestDetectReportsSymlinkReplacementWithoutFollowingIt(t *testing.T) {
	base := t.TempDir()
	current := t.TempDir()
	outside := t.TempDir()
	writeFile(t, base, "apps/a.yaml", "same as outside")
	writeFile(t, outside, "a.yaml", "same as outside")
	if err := os.MkdirAll(filepath.Join(current, "apps"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "a.yaml"), filepath.Join(current, "apps", "a.yaml")); err != nil {
		t.Fatal(err)
	}

	got, err := Detect(base, current)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"apps/a.yaml"}
	if !equal(got, want) {
		t.Fatalf("Detect() = %v, want %v", got, want)
	}
}

func TestDetectReportsSymlinkOnlyChanges(t *testing.T) {
	base := t.TempDir()
	current := t.TempDir()
	outside := t.TempDir()
	writeFile(t, outside, "old.yaml", "old")
	writeFile(t, outside, "new.yaml", "new")
	if err := os.MkdirAll(filepath.Join(base, "apps"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(current, "apps"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "old.yaml"), filepath.Join(base, "apps", "a.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "new.yaml"), filepath.Join(current, "apps", "a.yaml")); err != nil {
		t.Fatal(err)
	}

	got, err := Detect(base, current)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"apps/a.yaml"}
	if !equal(got, want) {
		t.Fatalf("Detect() = %v, want %v", got, want)
	}
}

func TestDetectIgnoresUnchangedSymlinkTargets(t *testing.T) {
	base := t.TempDir()
	current := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "apps"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(current, "apps"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../shared/a.yaml", filepath.Join(base, "apps", "a.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../shared/a.yaml", filepath.Join(current, "apps", "a.yaml")); err != nil {
		t.Fatal(err)
	}

	got, err := Detect(base, current)
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 0 {
		t.Fatalf("Detect() = %v, want none", got)
	}
}

func TestDetectReportsSymlinkOnlyAddition(t *testing.T) {
	base := t.TempDir()
	current := t.TempDir()
	outside := t.TempDir()
	writeFile(t, outside, "a.yaml", "added")
	if err := os.MkdirAll(filepath.Join(current, "apps"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "a.yaml"), filepath.Join(current, "apps", "a.yaml")); err != nil {
		t.Fatal(err)
	}

	got, err := Detect(base, current)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"apps/a.yaml"}
	if !equal(got, want) {
		t.Fatalf("Detect() = %v, want %v", got, want)
	}
}

func TestDetectReportsFileToDirectoryTypeChange(t *testing.T) {
	base := t.TempDir()
	current := t.TempDir()
	writeFile(t, base, "apps/a.yaml", "old")
	if err := os.MkdirAll(filepath.Join(current, "apps", "a.yaml"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := Detect(base, current)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"apps/a.yaml"}
	if !equal(got, want) {
		t.Fatalf("Detect() = %v, want %v", got, want)
	}
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
