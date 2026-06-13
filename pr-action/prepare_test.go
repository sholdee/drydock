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
	res := runPrepare(t, map[string]string{"GITHUB_SHA": "abc123def456"})

	got := outputValue(t, res.output, "cache-key")
	want := "drydock-Linux-X64-example-repo-test-v1-abc123def456"
	if got != want {
		t.Fatalf("cache-key = %q, want commit-rotated %q", got, want)
	}
	// The rotated key must fall back through the version+suffix restore prefix
	// so other runs in scope restore the most recent cache.
	if !strings.HasPrefix(got, "drydock-Linux-X64-example-repo-test-v1-") {
		t.Fatalf("rotated key %q does not start with its restore prefix", got)
	}
	if !strings.Contains(res.output, "restore-keys<<EOF\ndrydock-Linux-X64-example-repo-test-v1-\n") {
		t.Fatalf("GITHUB_OUTPUT = %q, want version+suffix restore prefix first", res.output)
	}
}

func TestPrepareCacheKeyStaticWithoutCommitSha(t *testing.T) {
	res := runPrepare(t, map[string]string{"GITHUB_SHA": ""})

	got := outputValue(t, res.output, "cache-key")
	want := "drydock-Linux-X64-example-repo-test-v1"
	if got != want {
		t.Fatalf("cache-key = %q, want static %q when no commit sha is set", got, want)
	}
}

func TestPrepareRespectsExplicitCacheKey(t *testing.T) {
	res := runPrepare(t, map[string]string{
		"GITHUB_SHA":              "abc123def456",
		"DRYDOCK_INPUT_CACHE_KEY": "custom-key",
	})

	got := outputValue(t, res.output, "cache-key")
	if got != "custom-key" {
		t.Fatalf("cache-key = %q, want the explicit custom-key without rotation", got)
	}
}

func TestPrepareCacheModeAutoUsesActionsCache(t *testing.T) {
	res := runPrepare(t, map[string]string{"DRYDOCK_INPUT_CACHE_MODE": "auto"})

	if got := outputValue(t, res.output, "cache-restore"); got != "true" {
		t.Fatalf("cache-restore = %q, want true for auto on a trusted run", got)
	}
	if got := outputValue(t, res.output, "cache-save"); got != "true" {
		t.Fatalf("cache-save = %q, want true for auto on a trusted run", got)
	}
}

func TestPrepareCacheModeLocalSkipsActionsCache(t *testing.T) {
	toolCache := t.TempDir()
	res := runPrepare(t, map[string]string{
		"DRYDOCK_INPUT_CACHE_MODE": "local",
		"RUNNER_TOOL_CACHE":        toolCache,
	})

	if got := outputValue(t, res.output, "cache-restore"); got != "false" {
		t.Fatalf("cache-restore = %q, want false for local mode", got)
	}
	if got := outputValue(t, res.output, "cache-save"); got != "false" {
		t.Fatalf("cache-save = %q, want false for local mode", got)
	}
	// local defaults its cache-path to the persistent tool cache.
	if got := outputValue(t, res.output, "cache-path"); got != filepath.Join(toolCache, "drydock-cache") {
		t.Fatalf("cache-path = %q, want it under the tool cache %q", got, toolCache)
	}
}

func TestPrepareCacheModeOffDisablesActionsCache(t *testing.T) {
	res := runPrepare(t, map[string]string{"DRYDOCK_INPUT_CACHE_MODE": "off"})

	if got := outputValue(t, res.output, "cache-restore"); got != "false" {
		t.Fatalf("cache-restore = %q, want false for off mode", got)
	}
	if got := outputValue(t, res.output, "cache-save"); got != "false" {
		t.Fatalf("cache-save = %q, want false for off mode", got)
	}
}

func TestPrepareCacheFalseForcesOff(t *testing.T) {
	// Back-compat: cache: false disables persistence even though cache-mode
	// defaults to auto.
	res := runPrepare(t, map[string]string{"DRYDOCK_INPUT_CACHE": "false"})

	if got := outputValue(t, res.output, "cache-restore"); got != "false" {
		t.Fatalf("cache-restore = %q, want false when cache is false", got)
	}
	if got := outputValue(t, res.output, "cache-save"); got != "false" {
		t.Fatalf("cache-save = %q, want false when cache is false", got)
	}
}

func TestPrepareCacheModeInvalidFails(t *testing.T) {
	logs := runPrepareExpectFailure(t, map[string]string{"DRYDOCK_INPUT_CACHE_MODE": "bogus"})
	if !strings.Contains(logs, "cache-mode must be auto, github, local, or off") {
		t.Fatalf("stderr = %q, want a cache-mode validation error", logs)
	}
}

func TestPrepareSelfHostedAutoEmitsNotice(t *testing.T) {
	res := runPrepare(t, map[string]string{
		"DRYDOCK_INPUT_CACHE_MODE": "auto",
		"RUNNER_ENVIRONMENT":       "self-hosted",
	})

	if !strings.Contains(res.logs, "::notice::") || !strings.Contains(res.logs, "cache-mode: local") {
		t.Fatalf("logs = %q, want a self-hosted local-cache notice", res.logs)
	}
}

func TestPrepareCacheModeGithubSilencesSelfHostedNotice(t *testing.T) {
	res := runPrepare(t, map[string]string{
		"DRYDOCK_INPUT_CACHE_MODE": "github",
		"RUNNER_ENVIRONMENT":       "self-hosted",
	})

	if got := outputValue(t, res.output, "cache-restore"); got != "true" {
		t.Fatalf("cache-restore = %q, want true for github mode", got)
	}
	if strings.Contains(res.logs, "::notice::") {
		t.Fatalf("logs = %q, want no notice when cache-mode is explicitly github", res.logs)
	}
}

func TestPrepareLocalOnGitHubHostedWarns(t *testing.T) {
	res := runPrepare(t, map[string]string{
		"DRYDOCK_INPUT_CACHE_MODE": "local",
		"RUNNER_ENVIRONMENT":       "github-hosted",
		"RUNNER_TOOL_CACHE":        t.TempDir(),
	})

	if !strings.Contains(res.logs, "::warning::") || !strings.Contains(res.logs, "does not persist on a GitHub-hosted runner") {
		t.Fatalf("logs = %q, want a github-hosted local-cache warning", res.logs)
	}
}

type prepareResult struct {
	output string // GITHUB_OUTPUT file contents
	logs   string // prepare.sh stdout+stderr (::notice::/::warning:: commands)
}

// runPrepare runs prepare.sh with a deterministic default environment merged
// with overrides (overrides win) and expects success.
func runPrepare(t *testing.T, overrides map[string]string) prepareResult {
	t.Helper()
	outputPath, logs, err := execPrepare(t, overrides)
	if err != nil {
		t.Fatalf("prepare.sh failed: %v\n%s", err, logs)
	}
	return prepareResult{output: readFile(t, outputPath), logs: logs}
}

// runPrepareExpectFailure runs prepare.sh expecting a non-zero exit and returns
// its combined output.
func runPrepareExpectFailure(t *testing.T, overrides map[string]string) string {
	t.Helper()
	_, logs, err := execPrepare(t, overrides)
	if err == nil {
		t.Fatalf("prepare.sh succeeded, want failure\n%s", logs)
	}
	return logs
}

func execPrepare(t *testing.T, overrides map[string]string) (outputPath, logs string, err error) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell action tests require bash")
	}

	tmp := t.TempDir()
	outputPath = filepath.Join(tmp, "github-output")

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
		"RUNNER_ENVIRONMENT":                      "",
		"RUNNER_TOOL_CACHE":                       "",
		"DRYDOCK_INPUT_ARTIFACT_RETENTION_DAYS":   "",
		"DRYDOCK_INPUT_CACHE":                     "true",
		"DRYDOCK_INPUT_CACHE_MODE":                "",
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
	out, runErr := cmd.CombinedOutput()
	return outputPath, string(out), runErr
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
