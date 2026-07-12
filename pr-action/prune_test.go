package praction

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// runPrune executes prune.sh with the given environment overlay and expects
// success (exit 0). It returns the combined stdout+stderr output.
func runPrune(t *testing.T, env []string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell action tests require bash")
	}
	cmd := exec.Command("bash", "prune.sh")
	cmd.Dir = "."
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("prune.sh failed: %v\n%s", err, out)
	}
	return string(out)
}

// defaultPruneEnv builds a minimal environment for prune.sh tests, with the
// drydock binary resolved from tmp (prepended to PATH).
func defaultPruneEnv(t *testing.T, tmp, cachePath string) []string {
	t.Helper()
	return append(
		withoutEnvKeys(os.Environ(), "PATH", "DRYDOCK_BIN", "DRYDOCK_CACHE_PATH", "DRYDOCK_INPUT_CACHE_PRUNE_MAX_SIZE", "DRYDOCK_INPUT_PATH"),
		"PATH="+tmp+string(os.PathListSeparator)+os.Getenv("PATH"),
		"DRYDOCK_BIN=drydock",
		"DRYDOCK_CACHE_PATH="+cachePath,
		"DRYDOCK_INPUT_CACHE_PRUNE_MAX_SIZE=4Gi",
		"DRYDOCK_INPUT_PATH=.",
	)
}

func TestPrunePassesExactFlagsAndCacheDirs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell action tests require bash")
	}

	tmp := t.TempDir()
	cachePath := filepath.Join(tmp, "cache")

	// Record the argv to a file; emit valid JSON output.
	writeExecutable(t, filepath.Join(tmp, "drydock"), `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "`+tmp+`/drydock-args.txt"
printf '{"removedCount":3,"sizeEvictedBytes":1048576,"totalSizeBytes":2097152}\n'
`)

	env := defaultPruneEnv(t, tmp, cachePath)
	runPrune(t, env)

	args := readFile(t, filepath.Join(tmp, "drydock-args.txt"))

	// Required flags and values
	for _, want := range []string{
		"cache prune",
		"--max-size 4Gi",
		"--yes",
		"--path .",
		"--git-cache-dir " + cachePath + "/git",
		"--chart-cache-dir " + cachePath + "/charts",
		"--remote-cache-dir " + cachePath + "/remotes",
		"--render-cache-dir " + cachePath + "/renders",
		"-o json",
	} {
		if !strings.Contains(args, want) {
			t.Fatalf("drydock args = %q, want %q", args, want)
		}
	}

	// Must NOT pass --render-cache-max-size (render sweep runs at its 512Mi default)
	if strings.Contains(args, "--render-cache-max-size") {
		t.Fatalf("drydock args = %q, must not include --render-cache-max-size", args)
	}

	// Must NOT reference the plugin cache dir
	if strings.Contains(args, "plugin") {
		t.Fatalf("drydock args = %q, must not touch plugin cache dir", args)
	}
}

func TestPruneUnknownFlagExitsZeroWithNotice(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell action tests require bash")
	}

	tmp := t.TempDir()
	cachePath := filepath.Join(tmp, "cache")

	// Simulate an older drydock that does not know --max-size.
	writeExecutable(t, filepath.Join(tmp, "drydock"), `#!/usr/bin/env bash
echo "unknown flag: --max-size" >&2
exit 2
`)

	env := defaultPruneEnv(t, tmp, cachePath)
	out := runPrune(t, env)

	if !strings.Contains(out, "::notice::") {
		t.Fatalf("output = %q, want ::notice:: for unknown-flag degradation", out)
	}
	if strings.Contains(out, "::warning::") {
		t.Fatalf("output = %q, must not emit ::warning:: for unknown-flag case", out)
	}
}

func TestPruneOtherFailureExitsZeroWithWarning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell action tests require bash")
	}

	tmp := t.TempDir()
	cachePath := filepath.Join(tmp, "cache")

	// Simulate a failure unrelated to the unknown-flag case.
	writeExecutable(t, filepath.Join(tmp, "drydock"), `#!/usr/bin/env bash
echo "cache prune: unexpected internal error" >&2
exit 1
`)

	env := defaultPruneEnv(t, tmp, cachePath)
	out := runPrune(t, env)

	if !strings.Contains(out, "::warning::") {
		t.Fatalf("output = %q, want ::warning:: for unexpected failure", out)
	}
}

func TestPruneMissingBinaryExitsZeroWithNotice(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell action tests require bash")
	}

	tmp := t.TempDir()
	cachePath := filepath.Join(tmp, "cache")

	// Do NOT write a drydock stub — binary is absent from tmp.
	// Use a PATH that does not contain any real drydock binary either.
	env := append(
		withoutEnvKeys(os.Environ(), "PATH", "DRYDOCK_BIN", "DRYDOCK_CACHE_PATH", "DRYDOCK_INPUT_CACHE_PRUNE_MAX_SIZE", "DRYDOCK_INPUT_PATH"),
		"PATH="+tmp, // only the empty tmp dir — no drydock here
		"DRYDOCK_BIN=drydock",
		"DRYDOCK_CACHE_PATH="+cachePath,
		"DRYDOCK_INPUT_CACHE_PRUNE_MAX_SIZE=4Gi",
		"DRYDOCK_INPUT_PATH=.",
	)

	out := runPrune(t, env)

	if !strings.Contains(out, "::notice::") {
		t.Fatalf("output = %q, want ::notice:: when binary is missing", out)
	}
	if strings.Contains(out, "::warning::") {
		t.Fatalf("output = %q, must not emit ::warning:: when binary is missing", out)
	}
}

func TestPruneSummaryLoggedOnSuccess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell action tests require bash")
	}

	tmp := t.TempDir()
	cachePath := filepath.Join(tmp, "cache")

	writeExecutable(t, filepath.Join(tmp, "drydock"), `#!/usr/bin/env bash
printf '{"removedCount":5,"sizeEvictedBytes":2097152,"totalSizeBytes":4194304}\n'
`)
	// Ensure jq is available in the PATH for this test.
	jqPath, err := exec.LookPath("jq")
	if err != nil {
		t.Skip("jq not available in PATH; skipping summary assertion")
	}
	_ = jqPath

	env := defaultPruneEnv(t, tmp, cachePath)
	out := runPrune(t, env)

	for _, want := range []string{"removed 5 entries", "freed 2097152 bytes", "4194304 bytes remain"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output = %q, want %q in summary line", out, want)
		}
	}
}
