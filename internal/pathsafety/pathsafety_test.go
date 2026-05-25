package pathsafety

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRelEscapes(t *testing.T) {
	if !RelEscapes("..") || !RelEscapes(filepath.Join("..", "escape")) {
		t.Fatal("RelEscapes did not reject parent escape")
	}
	if RelEscapes(".") || RelEscapes("nested") {
		t.Fatal("RelEscapes rejected safe relative paths")
	}
	if !SlashRelEscapes("..") || !SlashRelEscapes("../escape") {
		t.Fatal("SlashRelEscapes did not reject slash parent escape")
	}
	if SlashRelEscapes(".") || SlashRelEscapes("nested") {
		t.Fatal("SlashRelEscapes rejected safe slash-relative paths")
	}
}

func TestCleanRelative(t *testing.T) {
	got, ok := CleanRelative("nested/../app")
	if !ok || got != "app" {
		t.Fatalf("CleanRelative() = %q, %v; want app, true", got, ok)
	}
	for _, raw := range []string{"", ".", "..", "../app", filepath.Join("..", "app")} {
		if got, ok := CleanRelative(raw); ok {
			t.Fatalf("CleanRelative(%q) = %q, true; want false", raw, got)
		}
	}
}

func TestIsInsideAnyResolvesExistingSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink privileges are not guaranteed on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("create symlink: %v", err)
	}
	inside, _, err := IsInsideAny(filepath.Join(link, "cache"), []string{root})
	if err != nil {
		t.Fatalf("IsInsideAny() error = %v", err)
	}
	if inside {
		t.Fatal("IsInsideAny() = true for symlink escape, want false")
	}
}
