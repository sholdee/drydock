package profile

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestStartRejectsInvalidMode(t *testing.T) {
	if _, err := Start(Options{Mode: "sometimes", OutDir: t.TempDir()}); err == nil {
		t.Fatal("Start() error = nil, want invalid mode error")
	} else if !strings.Contains(err.Error(), `profile must be cpu, mem, block, mutex, or trace, got "sometimes"`) {
		t.Fatalf("Start() error = %v, want invalid mode message", err)
	}
}

func TestMemProfileWritesRestrictiveFileAndStatus(t *testing.T) {
	outDir := t.TempDir()
	var stderr bytes.Buffer
	session, err := Start(Options{
		Mode:        "mem",
		OutDir:      outDir,
		CommandPath: "drydock test apps",
		Stderr:      &stderr,
		Now: func() time.Time {
			return time.Date(2026, 5, 29, 15, 30, 12, 123456789, time.UTC)
		},
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := session.Stop(); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	path := filepath.Join(outDir, "drydock-test-apps-20260529T153012.123456789Z.mem.pprof")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat profile file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("profile file mode = %v, want 0600", info.Mode().Perm())
	}
	if !strings.Contains(stderr.String(), "profile mem: wrote "+path) {
		t.Fatalf("stderr = %q, want profile write message", stderr.String())
	}
}

func TestTraceProfileUsesTraceExtension(t *testing.T) {
	if got := filename("drydock diff apps", "trace", time.Date(2026, 5, 29, 15, 31, 55, 0, time.UTC)); got != "drydock-diff-apps-20260529T153155.000000000Z.trace.out" {
		t.Fatalf("filename() = %q, want trace .out filename", got)
	}
}

func TestMutexProfileRestoresPreviousFraction(t *testing.T) {
	previous := runtime.SetMutexProfileFraction(7)
	defer runtime.SetMutexProfileFraction(previous)

	session, err := Start(Options{Mode: "mutex", OutDir: t.TempDir(), CommandPath: "drydock test apps"})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := session.Stop(); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if got := runtime.SetMutexProfileFraction(previous); got != 7 {
		t.Fatalf("mutex profile fraction after Stop = %d, want restored 7", got)
	}
}

func TestSanitizeCommandPathUsesStableCommandWords(t *testing.T) {
	got := sanitizeCommandPath("drydock build app")
	if got != "drydock-build-app" {
		t.Fatalf("sanitizeCommandPath() = %q", got)
	}
}
