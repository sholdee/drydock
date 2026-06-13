package praction

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPrepareOutputsDefaultHTMLDiffArtifactName(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell action tests require bash")
	}

	tmp := t.TempDir()
	outputPath := filepath.Join(tmp, "github-output")
	cmd := exec.Command("bash", "prepare.sh")
	cmd.Dir = "."
	cmd.Env = append(os.Environ(),
		"GITHUB_OUTPUT="+outputPath,
		"GITHUB_RUN_ID=12345",
		"GITHUB_RUN_ATTEMPT=2",
		"GITHUB_EVENT_NAME=pull_request",
		"GITHUB_REPOSITORY=example/repo",
		"RUNNER_TEMP="+tmp,
		"RUNNER_OS=Linux",
		"RUNNER_ARCH=X64",
		"DRYDOCK_INPUT_ARTIFACT_RETENTION_DAYS=",
		"DRYDOCK_INPUT_CACHE=true",
		"DRYDOCK_INPUT_CACHE_KEY=",
		"DRYDOCK_INPUT_CACHE_KEY_PREFIX=drydock",
		"DRYDOCK_INPUT_CACHE_KEY_SUFFIX=v1",
		"DRYDOCK_INPUT_CACHE_PATH=",
		"DRYDOCK_INPUT_CACHE_RESTORE_KEYS=",
		"DRYDOCK_INPUT_CACHE_UNTRUSTED_RESTORE=false",
		"DRYDOCK_INPUT_COMMENT_CONTINUE_ON_ERROR=false",
		"DRYDOCK_INPUT_COMMENT_EMPTY=false",
		"DRYDOCK_INPUT_COMMENT_MODE=both",
		"DRYDOCK_INPUT_DIFF_ARTIFACT_NAME=",
		"DRYDOCK_INPUT_DIFF_MAX_BYTES=60000",
		"DRYDOCK_INPUT_IMAGE_ARTIFACT_NAME=",
		"DRYDOCK_INPUT_SAVE_CACHE=true",
		"DRYDOCK_INPUT_UPLOAD_ARTIFACTS=true",
		"DRYDOCK_PR_HEAD_REPO=example/repo",
		"DRYDOCK_RESOLVED_VERSION=test",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("prepare.sh failed: %v\n%s", err, out)
	}

	output := readFile(t, outputPath)
	if !strings.Contains(output, "diff-html-artifact-name=drydock-diff-12345-2.html") {
		t.Fatalf("GITHUB_OUTPUT = %q, want default HTML diff artifact name", output)
	}
}

func TestPrepareRotatesCacheKeyByCommitSha(t *testing.T) {
	out := runPrepare(t, map[string]string{"GITHUB_SHA": "abc123def456"})

	got := outputValue(t, out, "cache-key")
	want := "drydock-Linux-X64-example-repo-test-v1-abc123def456"
	if got != want {
		t.Fatalf("cache-key = %q, want commit-rotated %q", got, want)
	}
	// The rotated key must fall back through the version+suffix restore prefix
	// so other runs in scope restore the most recent cache.
	if !strings.HasPrefix(got, "drydock-Linux-X64-example-repo-test-v1-") {
		t.Fatalf("rotated key %q does not start with its restore prefix", got)
	}
	if !strings.Contains(out, "restore-keys<<EOF\ndrydock-Linux-X64-example-repo-test-v1-\n") {
		t.Fatalf("GITHUB_OUTPUT = %q, want version+suffix restore prefix first", out)
	}
}

func TestPrepareCacheKeyStaticWithoutCommitSha(t *testing.T) {
	out := runPrepare(t, map[string]string{"GITHUB_SHA": ""})

	got := outputValue(t, out, "cache-key")
	want := "drydock-Linux-X64-example-repo-test-v1"
	if got != want {
		t.Fatalf("cache-key = %q, want static %q when no commit sha is set", got, want)
	}
}

func TestPrepareRespectsExplicitCacheKey(t *testing.T) {
	out := runPrepare(t, map[string]string{
		"GITHUB_SHA":              "abc123def456",
		"DRYDOCK_INPUT_CACHE_KEY": "custom-key",
	})

	got := outputValue(t, out, "cache-key")
	if got != "custom-key" {
		t.Fatalf("cache-key = %q, want the explicit custom-key without rotation", got)
	}
}

// runPrepare runs prepare.sh with a deterministic default environment merged
// with overrides (overrides win), and returns the GITHUB_OUTPUT contents.
func runPrepare(t *testing.T, overrides map[string]string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell action tests require bash")
	}

	tmp := t.TempDir()
	outputPath := filepath.Join(tmp, "github-output")

	defaults := map[string]string{
		"GITHUB_OUTPUT":                           outputPath,
		"GITHUB_RUN_ID":                           "12345",
		"GITHUB_RUN_ATTEMPT":                      "1",
		"GITHUB_EVENT_NAME":                       "pull_request",
		"GITHUB_REPOSITORY":                       "example/repo",
		"GITHUB_SHA":                              "",
		"RUNNER_TEMP":                             tmp,
		"RUNNER_OS":                               "Linux",
		"RUNNER_ARCH":                             "X64",
		"DRYDOCK_INPUT_ARTIFACT_RETENTION_DAYS":   "",
		"DRYDOCK_INPUT_CACHE":                     "true",
		"DRYDOCK_INPUT_CACHE_KEY":                 "",
		"DRYDOCK_INPUT_CACHE_KEY_PREFIX":          "drydock",
		"DRYDOCK_INPUT_CACHE_KEY_SUFFIX":          "v1",
		"DRYDOCK_INPUT_CACHE_PATH":                "",
		"DRYDOCK_INPUT_CACHE_RESTORE_KEYS":        "",
		"DRYDOCK_INPUT_CACHE_UNTRUSTED_RESTORE":   "false",
		"DRYDOCK_INPUT_COMMENT_CONTINUE_ON_ERROR": "false",
		"DRYDOCK_INPUT_COMMENT_EMPTY":             "false",
		"DRYDOCK_INPUT_COMMENT_MODE":              "both",
		"DRYDOCK_INPUT_DIFF_ARTIFACT_NAME":        "",
		"DRYDOCK_INPUT_DIFF_MAX_BYTES":            "60000",
		"DRYDOCK_INPUT_IMAGE_ARTIFACT_NAME":       "",
		"DRYDOCK_INPUT_SAVE_CACHE":                "true",
		"DRYDOCK_INPUT_UPLOAD_ARTIFACTS":          "true",
		"DRYDOCK_PR_HEAD_REPO":                    "example/repo",
		"DRYDOCK_RESOLVED_VERSION":                "test",
	}

	env := os.Environ()
	for key, value := range defaults {
		env = append(env, key+"="+value)
	}
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}

	cmd := exec.Command("bash", "prepare.sh")
	cmd.Dir = "."
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("prepare.sh failed: %v\n%s", err, out)
	}
	return readFile(t, outputPath)
}

func outputValue(t *testing.T, output, key string) string {
	t.Helper()
	for line := range strings.SplitSeq(output, "\n") {
		if k, v, ok := strings.Cut(line, "="); ok && k == key {
			return v
		}
	}
	t.Fatalf("GITHUB_OUTPUT %q has no %q line", output, key)
	return ""
}
