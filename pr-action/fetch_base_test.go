package praction

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFetchBaseReusesExistingGitAuthHeader(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell action tests require bash")
	}

	result := runFetchBase(t, true)
	if strings.Contains(result, "extraheader") {
		t.Fatalf("fetch command = %q, want no injected auth header when git already has one", result)
	}
}

func TestFetchBaseAddsAuthHeaderWhenGitHasNoCredential(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell action tests require bash")
	}

	result := runFetchBase(t, false)
	if !strings.Contains(result, "-c") || !strings.Contains(result, "http.https://github.com/.extraheader=AUTHORIZATION: basic") {
		t.Fatalf("fetch command = %q, want injected auth header", result)
	}
}

func runFetchBase(t *testing.T, existingAuthHeader bool) string {
	t.Helper()

	tmp := t.TempDir()
	gitPath := filepath.Join(tmp, "git")
	resultPath := filepath.Join(tmp, "fetch-args")

	existingValue := "false"
	if existingAuthHeader {
		existingValue = "true"
	}

	fakeGit := `#!/usr/bin/env bash
set -euo pipefail

if [[ "$1" == "check-ref-format" ]]; then
  exit 0
fi

if [[ "$1" == "config" ]]; then
  if [[ "${DRYDOCK_TEST_EXISTING_AUTH_HEADER}" == "true" ]]; then
    echo "AUTHORIZATION: basic existing"
    exit 0
  fi
  exit 1
fi

if [[ "$1" == "fetch" || "$1" == "-c" ]]; then
  printf '%s\n' "$*" > "${DRYDOCK_TEST_FETCH_ARGS}"
  exit 0
fi

echo "unexpected git invocation: $*" >&2
exit 2
`
	if err := os.WriteFile(gitPath, []byte(fakeGit), 0o700); err != nil {
		t.Fatalf("write fake git: %v", err)
	}

	outputPath := filepath.Join(tmp, "github-output")
	cmd := exec.Command("bash", "fetch-base.sh")
	cmd.Dir = "."
	cmd.Env = append(os.Environ(),
		"PATH="+tmp+string(os.PathListSeparator)+os.Getenv("PATH"),
		"DRYDOCK_GITHUB_TOKEN=test-token",
		"DRYDOCK_PR_BASE_REF=master",
		"DRYDOCK_TEST_EXISTING_AUTH_HEADER="+existingValue,
		"DRYDOCK_TEST_FETCH_ARGS="+resultPath,
		"GITHUB_OUTPUT="+outputPath,
		"GITHUB_SERVER_URL=https://github.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fetch-base.sh failed: %v\n%s", err, out)
	}

	result, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("read fetch args: %v", err)
	}
	return string(result)
}
