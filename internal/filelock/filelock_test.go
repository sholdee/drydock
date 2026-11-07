//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package filelock

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestAcquireWaitsAndTimesOut pins that a second Acquire waits while the
// lock is held, fails when its context ends, and succeeds after Release.
func TestAcquireWaitsAndTimesOut(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".lock")
	held, err := Acquire(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := Acquire(ctx, path); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second Acquire() error = %v, want the context deadline", err)
	}
	if waited := time.Since(start); waited < 100*time.Millisecond {
		t.Fatalf("second Acquire() returned after %v, want it to wait for the lock", waited)
	}
	held.Release()
	again, err := Acquire(context.Background(), path)
	if err != nil {
		t.Fatalf("Acquire() after Release() error = %v", err)
	}
	again.Release()
}

// TestAcquireFileModes pins the mode of a new lock file: 0644 before the
// umask for Acquire, so a group-shared cache works, and the mode that
// AcquireWithMode sets.
func TestAcquireFileModes(t *testing.T) {
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)
	dir := t.TempDir()
	for name, acquire := range map[string]func(string) (*Lock, error){
		"shared":  func(path string) (*Lock, error) { return Acquire(context.Background(), path) },
		"private": func(path string) (*Lock, error) { return AcquireWithMode(context.Background(), path, 0o600) },
	} {
		path := filepath.Join(dir, name+".lock")
		lock, err := acquire(path)
		if err != nil {
			t.Fatal(err)
		}
		lock.Release()
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0o644)
		if name == "private" {
			want = 0o600
		}
		if got := info.Mode().Perm(); got != want {
			t.Fatalf("%s lock file mode = %v, want %v", name, got, want)
		}
	}
}

// TestAcquireReadOnly pins that a lock file in a read-only directory
// cannot be created, and that an existing lock file that only allows
// reading is locked through a read-only open.
func TestAcquireReadOnly(t *testing.T) {
	dir := t.TempDir()
	readOnlyDir := filepath.Join(dir, "ro")
	if err := os.Mkdir(readOnlyDir, 0o555); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(readOnlyDir, 0o755) }()
	if _, err := Acquire(context.Background(), filepath.Join(readOnlyDir, ".lock")); err == nil {
		t.Fatal("Acquire() in a read-only directory error = nil, want an open error")
	}
	path := filepath.Join(dir, "readonly.lock")
	if err := os.WriteFile(path, nil, 0o444); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		t.Skip("root opens a read-only file for writing")
	}
	lock, err := Acquire(context.Background(), path)
	if err != nil {
		t.Fatalf("Acquire() on a read-only lock file error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := Acquire(ctx, path); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second Acquire() error = %v, want the read-only lock to hold", err)
	}
	lock.Release()
}

// TestAcquireRefusesSymlinks pins that a lock file that is a symlink is
// refused and its target is not created.
func TestAcquireRefusesSymlinks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	path := filepath.Join(dir, ".lock")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(context.Background(), path); err == nil || !strings.Contains(err.Error(), "is a symlink") {
		t.Fatalf("Acquire() error = %v, want a symlink refusal", err)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("symlink target created: %v", err)
	}
}
