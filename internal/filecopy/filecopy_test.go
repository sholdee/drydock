package filecopy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCopyRegularFileCopiesContentAndMode(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src.txt")
	dst := filepath.Join(root, "nested", "dst.txt")
	if err := os.WriteFile(src, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := CopyRegularFile(src, dst, 0); err != nil {
		t.Fatalf("CopyRegularFile() error = %v", err)
	}
	body, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "source" {
		t.Fatalf("dst content = %q, want source", body)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("dst mode = %v, want 0600", got)
	}
}

func TestCopyRegularFileReplacesHardlinkWithoutMutatingPeer(t *testing.T) {
	root := t.TempDir()
	peer := filepath.Join(root, "peer.txt")
	dst := filepath.Join(root, "dst.txt")
	src := filepath.Join(root, "src.txt")
	if err := os.WriteFile(peer, []byte("peer"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(peer, dst); err != nil {
		t.Skipf("hardlink unavailable: %v", err)
	}
	if err := os.WriteFile(src, []byte("replacement"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := CopyRegularFile(src, dst, 0o644); err != nil {
		t.Fatalf("CopyRegularFile() error = %v", err)
	}
	peerData, err := os.ReadFile(peer)
	if err != nil {
		t.Fatal(err)
	}
	if string(peerData) != "peer" {
		t.Fatalf("peer content = %q, want unchanged", peerData)
	}
	dstData, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(dstData) != "replacement" {
		t.Fatalf("dst content = %q, want replacement", dstData)
	}
}

func TestCopyRegularFileRejectsSymlinkSource(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.txt")
	link := filepath.Join(root, "link.txt")
	if err := os.WriteFile(target, []byte("target"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	err := CopyRegularFile(link, filepath.Join(root, "dst.txt"), 0o644)
	if err == nil || !strings.Contains(err.Error(), "is a symlink") {
		t.Fatalf("CopyRegularFile() error = %v, want symlink rejection", err)
	}
}

func TestLinkOrCopyRegularFileDoesNotChmodSource(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src.txt")
	dst := filepath.Join(root, "dst.txt")
	if err := os.WriteFile(src, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := LinkOrCopyRegularFile(src, dst, 0o644); err != nil {
		t.Fatalf("LinkOrCopyRegularFile() error = %v", err)
	}
	info, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("source mode = %v, want 0600", got)
	}
}

func TestLinkOrCopyRegularFileHardlinksWhenModeMatches(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src.txt")
	dst := filepath.Join(root, "dst.txt")
	if err := os.WriteFile(src, []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := LinkOrCopyRegularFile(src, dst, 0o644); err != nil {
		t.Fatalf("LinkOrCopyRegularFile() error = %v", err)
	}
	assertSameFile(t, src, dst)
}

func TestLinkOrCopyRegularFileCopiesWhenModeDiffers(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src.txt")
	dst := filepath.Join(root, "dst.txt")
	if err := os.WriteFile(src, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := LinkOrCopyRegularFile(src, dst, 0o644); err != nil {
		t.Fatalf("LinkOrCopyRegularFile() error = %v", err)
	}
	assertDifferentFile(t, src, dst)
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("dst mode = %v, want 0644", got)
	}
}

func assertSameFile(t *testing.T, left, right string) {
	t.Helper()
	leftInfo, err := os.Stat(left)
	if err != nil {
		t.Fatal(err)
	}
	rightInfo, err := os.Stat(right)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(leftInfo, rightInfo) {
		t.Fatalf("%s and %s are not the same file", left, right)
	}
}

func assertDifferentFile(t *testing.T, left, right string) {
	t.Helper()
	leftInfo, err := os.Stat(left)
	if err != nil {
		t.Fatal(err)
	}
	rightInfo, err := os.Stat(right)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(leftInfo, rightInfo) {
		t.Fatalf("%s and %s are the same file", left, right)
	}
}
