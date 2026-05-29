package setupaction

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestResolveLatestBuildsChecksumScopedCacheKey(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell action tests require bash")
	}

	tmp := t.TempDir()
	hash := strings.Repeat("a", 64)
	writeExecutable(t, filepath.Join(tmp, "curl"), `#!/usr/bin/env bash
set -euo pipefail

args="$*"
if [[ "${args}" == *"api.github.com/repos/sholdee/drydock/releases/latest"* ]]; then
  printf '{"tag_name":"v1.2.3"}\n'
  exit 0
fi
if [[ "${args}" == *"checksums.txt"* ]]; then
  out=""
  while [[ "$#" -gt 0 ]]; do
    if [[ "$1" == "--output" ]]; then
      out="$2"
      shift 2
      continue
    fi
    shift
  done
  printf '`+hash+`  drydock_linux-amd64.tar.gz\n' > "${out}"
  exit 0
fi
echo "unexpected curl invocation: ${args}" >&2
exit 2
`)

	outputPath := filepath.Join(tmp, "github-output")
	cmd := exec.Command("bash", "resolve.sh")
	cmd.Dir = "."
	cmd.Env = append(os.Environ(),
		"PATH="+tmp+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GITHUB_OUTPUT="+outputPath,
		"RUNNER_OS=Linux",
		"RUNNER_ARCH=X64",
		"RUNNER_TEMP="+tmp,
		"DRYDOCK_ALLOW_UNVERIFIED=false",
		"DRYDOCK_CACHE_BINARY=true",
		"DRYDOCK_CACHE_BINARY_KEY_SUFFIX=v1",
		"DRYDOCK_GITHUB_TOKEN=test-token",
		"DRYDOCK_INSTALL_DIR=/usr/local/bin",
		"DRYDOCK_RELEASE_REPOSITORY=sholdee/drydock",
		"DRYDOCK_VERSION=latest",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("resolve.sh failed: %v\n%s", err, out)
	}

	outputs := readGitHubOutput(t, outputPath)
	if got, want := outputs["version"], "v1.2.3"; got != want {
		t.Fatalf("version = %q, want %q", got, want)
	}
	if got, want := outputs["cache-enabled"], "true"; got != want {
		t.Fatalf("cache-enabled = %q, want %q", got, want)
	}
	if got := outputs["expected-hash"]; got != hash {
		t.Fatalf("expected-hash = %q, want %q", got, hash)
	}
	if got := outputs["cache-key"]; !strings.Contains(got, "v1.2.3") || strings.Contains(got, "latest") {
		t.Fatalf("cache-key = %q, want resolved version and no latest", got)
	}
	if got := outputs["cache-key"]; !strings.Contains(got, hash) {
		t.Fatalf("cache-key = %q, want checksum in key", got)
	}
}

func TestResolveSkipsCacheForUnverifiedInstall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell action tests require bash")
	}

	tmp := t.TempDir()
	writeExecutable(t, filepath.Join(tmp, "curl"), `#!/usr/bin/env bash
echo "curl should not be called for pinned unverified cache-disabled resolution" >&2
exit 2
`)

	outputPath := filepath.Join(tmp, "github-output")
	cmd := exec.Command("bash", "resolve.sh")
	cmd.Dir = "."
	cmd.Env = append(os.Environ(),
		"PATH="+tmp+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GITHUB_OUTPUT="+outputPath,
		"RUNNER_OS=Linux",
		"RUNNER_ARCH=X64",
		"RUNNER_TEMP="+tmp,
		"DRYDOCK_ALLOW_UNVERIFIED=true",
		"DRYDOCK_CACHE_BINARY=true",
		"DRYDOCK_CACHE_BINARY_KEY_SUFFIX=v1",
		"DRYDOCK_INSTALL_DIR=/usr/local/bin",
		"DRYDOCK_RELEASE_REPOSITORY=sholdee/drydock",
		"DRYDOCK_VERSION=v1.2.3",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("resolve.sh failed: %v\n%s", err, out)
	}

	outputs := readGitHubOutput(t, outputPath)
	if got, want := outputs["version"], "v1.2.3"; got != want {
		t.Fatalf("version = %q, want %q", got, want)
	}
	if got, want := outputs["cache-enabled"], "false"; got != want {
		t.Fatalf("cache-enabled = %q, want %q", got, want)
	}
	if got := outputs["expected-hash"]; got != "" {
		t.Fatalf("expected-hash = %q, want empty", got)
	}
}

func readGitHubOutput(t *testing.T, path string) map[string]string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read GITHUB_OUTPUT: %v", err)
	}
	outputs := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		outputs[key] = value
	}
	return outputs
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
