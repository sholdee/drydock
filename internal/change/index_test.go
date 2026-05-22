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
