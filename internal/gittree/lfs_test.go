package gittree

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func lfsPointerText(content string) (string, string) {
	sum := sha256.Sum256([]byte(content))
	oid := hex.EncodeToString(sum[:])
	return fmt.Sprintf("version https://git-lfs.github.com/spec/v1\noid sha256:%s\nsize %d\n", oid, len(content)), oid
}

// TestParseLFSPointerFollowsTheSpec pins the pointer files that Git LFS
// reads: a version line, then sorted keys with an oid and a size.
func TestParseLFSPointerFollowsTheSpec(t *testing.T) {
	valid, oid := lfsPointerText("payload\n")
	pointer, ok := ParseLFSPointer([]byte(valid))
	if !ok || pointer.OID != oid || pointer.Size != 8 || pointer.Extensions {
		t.Fatalf("ParseLFSPointer(valid) = %+v, %v", pointer, ok)
	}
	for name, data := range map[string]string{
		"no newline":    valid[:len(valid)-1],
		"bad version":   "version https://example.invalid/spec\noid sha256:" + oid + "\nsize 8\n",
		"unsorted keys": "version https://git-lfs.github.com/spec/v1\nsize 8\noid sha256:" + oid + "\n",
		"no size":       "version https://git-lfs.github.com/spec/v1\noid sha256:" + oid + "\n",
		"upper-case":    "version https://git-lfs.github.com/spec/v1\noid sha256:" + fmt.Sprintf("%X", oid) + "\nsize 8\n",
		"yaml":          "apiVersion: v1\nkind: ConfigMap\n",
	} {
		if _, ok := ParseLFSPointer([]byte(data)); ok {
			t.Fatalf("ParseLFSPointer(%s) = ok, want it refused", name)
		}
	}
}

// TestReplaceLFSPointerReadsAVerifiedObject pins that a pointer gets the
// content of the store's object only when its size and SHA-256 match, as
// `git lfs checkout` does, and that a missing object reports
// ErrLFSObjectMissing and leaves the pointer.
func TestReplaceLFSPointerReadsAVerifiedObject(t *testing.T) {
	content := "kind: ConfigMap\n"
	text, oid := lfsPointerText(content)
	pointer, _ := ParseLFSPointer([]byte(text))
	pointer.Path = "dir/file.yaml"
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dir", "file.yaml"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}

	empty := t.TempDir()
	if err := ReplaceLFSPointer(root, pointer, []string{empty}); !errors.Is(err, ErrLFSObjectMissing) {
		t.Fatalf("ReplaceLFSPointer(empty store) error = %v, want ErrLFSObjectMissing", err)
	}
	corrupt := t.TempDir()
	writeLFSObject(t, corrupt, oid, "kind: Secret!!!\n")
	if err := ReplaceLFSPointer(root, pointer, []string{corrupt}); !errors.Is(err, ErrLFSObjectMissing) {
		t.Fatalf("ReplaceLFSPointer(corrupt store) error = %v, want ErrLFSObjectMissing", err)
	}
	store := t.TempDir()
	writeLFSObject(t, store, oid, content)
	if err := ReplaceLFSPointer(root, pointer, []string{corrupt, store}); err != nil {
		t.Fatalf("ReplaceLFSPointer() error = %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "dir", "file.yaml")); string(data) != content {
		t.Fatalf("file = %q, want the object content", data)
	}
}

func writeLFSObject(t *testing.T, lfsDir, oid, content string) {
	t.Helper()
	path := LFSObjectPath(lfsDir, oid)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
