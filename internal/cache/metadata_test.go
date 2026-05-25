package cache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteMetadataGoldenRedactsTarget(t *testing.T) {
	entry := filepath.Join(t.TempDir(), "entry")
	if err := WriteMetadata(entry, Metadata{
		Source: SourceGit,
		Kind:   "git",
		Key:    "abc123",
		Target: "https://user:secret@example.test/repo.git?token=abc#fragment",
	}); err != nil {
		t.Fatalf("WriteMetadata() error = %v", err)
	}

	data, err := os.ReadFile(MetadataPath(entry))
	if err != nil {
		t.Fatalf("ReadFile(metadata) error = %v", err)
	}
	assertMetadataDoesNotLeak(t, data, "user", "secret", "token=", "fragment")

	var got Metadata
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal(metadata) error = %v", err)
	}
	assertMetadataGoldenShape(t, got)

	read, err := ReadMetadata(entry, SourceGit, "git", "abc123")
	if err != nil {
		t.Fatalf("ReadMetadata() error = %v", err)
	}
	if read == nil {
		t.Fatal("ReadMetadata() = nil, want metadata")
	}
	if read.Target != "https://example.test/repo.git" {
		t.Fatalf("ReadMetadata().Target = %q, want redacted target", read.Target)
	}
}

func assertMetadataDoesNotLeak(t *testing.T, data []byte, forbidden ...string) {
	t.Helper()
	text := string(data)
	for _, leaked := range forbidden {
		if strings.Contains(text, leaked) {
			t.Fatalf("metadata leaked %q:\n%s", leaked, text)
		}
	}
}

func assertMetadataGoldenShape(t *testing.T, got Metadata) {
	t.Helper()
	if got.SchemaVersion != 1 {
		t.Fatalf("SchemaVersion = %d, want 1", got.SchemaVersion)
	}
	if got.Source != SourceGit {
		t.Fatalf("Source = %q, want %q", got.Source, SourceGit)
	}
	if got.Kind != "git" {
		t.Fatalf("Kind = %q, want git", got.Kind)
	}
	if got.Key != "abc123" {
		t.Fatalf("Key = %q, want abc123", got.Key)
	}
	if got.Target != "https://example.test/repo.git" {
		t.Fatalf("Target = %q, want redacted target", got.Target)
	}
	if got.CreatedAt.IsZero() {
		t.Fatal("CreatedAt is zero, want timestamp")
	}
	if got.UpdatedAt.IsZero() {
		t.Fatal("UpdatedAt is zero, want timestamp")
	}
}
