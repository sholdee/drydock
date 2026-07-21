package cache

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func seedOCITestEntry(t *testing.T, root, key string) string {
	t.Helper()
	entry := OCIEntryPath(root, key)
	if err := os.MkdirAll(entry, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(entry, "image.tar"), []byte("tar"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteMetadata(entry, Metadata{
		Source:   SourceOCI,
		Kind:     "image",
		Key:      key,
		Target:   "oci://registry.example/org/app",
		Revision: "sha256:" + strings.Repeat("ab", 32),
	}); err != nil {
		t.Fatal(err)
	}
	return entry
}

// TestListIncludesOCIEntriesByDefault pins SourceOCI membership in the
// default enabled-source set and the entry shape (source oci, kind image).
func TestListIncludesOCIEntriesByDefault(t *testing.T) {
	root := t.TempDir()
	key := strings.Repeat("a", 64)
	seedOCITestEntry(t, root, key)
	// Non-entry names (offline tag records) are ignored by the lister.
	if err := os.MkdirAll(filepath.Join(root, "tags"), 0o755); err != nil {
		t.Fatal(err)
	}

	entries, err := List(Options{OCICacheDir: root})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1: %#v", len(entries), entries)
	}
	entry := entries[0]
	if entry.Source != SourceOCI || entry.Kind != "image" || entry.Key != key {
		t.Fatalf("entry = %+v, want oci/image/%s", entry, key)
	}
	if entry.Legacy || entry.Metadata == nil {
		t.Fatalf("entry = %+v, want metadata-backed", entry)
	}
}
