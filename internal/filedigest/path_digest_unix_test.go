//go:build darwin || linux

package filedigest

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestPathDigestRejectsSpecialFileModes(t *testing.T) {
	root := t.TempDir()
	fifo := filepath.Join(root, "apps", "demo", "fifo")
	if err := os.MkdirAll(filepath.Dir(fifo), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", filepath.Dir(fifo), err)
	}
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}

	_, err := PathDigest(t.Context(), PathDigestInput{
		RepoRoot: root,
		Paths:    []PathDigestPath{{Path: "apps/demo/fifo"}},
	})
	if err == nil {
		t.Fatalf("PathDigest() special file error = nil, want rejection")
	}
	if !strings.Contains(err.Error(), "unsupported filesystem mode") {
		t.Fatalf("PathDigest() special file error = %q, want unsupported mode rejection", err)
	}
}
