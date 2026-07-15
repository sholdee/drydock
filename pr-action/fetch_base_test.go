package praction

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeGitScript is a stand-in git used by fetch-base tests. Behavior is driven
// by DRYDOCK_TEST_* environment variables so individual tests can model the
// merge-base, shallow, and fetch interactions without a real repository.
const fakeGitScript = `#!/usr/bin/env bash
set -euo pipefail

case "$1" in
  check-ref-format)
    exit 0
    ;;
  config)
    if [[ "${DRYDOCK_TEST_EXISTING_AUTH_HEADER:-false}" == "true" ]]; then
      echo "AUTHORIZATION: basic existing"
      exit 0
    fi
    exit 1
    ;;
  rev-parse)
    if [[ "${2:-}" == "--git-dir" ]]; then
      echo "${DRYDOCK_TEST_GIT_DIR:-.git}"
    else
      echo "${DRYDOCK_TEST_HEAD_SHA:-headsha}"
    fi
    exit 0
    ;;
  merge-base)
    case "${DRYDOCK_TEST_MERGE_BASE_MODE:-immediate}" in
      immediate)
        echo "${DRYDOCK_TEST_MERGE_BASE_SHA:-mergebasesha}"
        ;;
      after-deepen)
        if [[ -n "${DRYDOCK_TEST_DEEPEN_MARKER:-}" && -f "${DRYDOCK_TEST_DEEPEN_MARKER}" ]]; then
          echo "${DRYDOCK_TEST_MERGE_BASE_SHA:-mergebasesha}"
        fi
        ;;
      never)
        :
        ;;
    esac
    exit 0
    ;;
  -c | fetch)
    if [[ -n "${DRYDOCK_TEST_FETCH_ARGS:-}" ]]; then
      printf '%s\n' "$*" > "${DRYDOCK_TEST_FETCH_ARGS}"
    fi
    if [[ -n "${DRYDOCK_TEST_FETCH_LOG:-}" ]]; then
      printf '%s\n' "$*" >> "${DRYDOCK_TEST_FETCH_LOG}"
    fi
    if [[ "$*" == *"--deepen"* && -n "${DRYDOCK_TEST_DEEPEN_MARKER:-}" ]]; then
      touch "${DRYDOCK_TEST_DEEPEN_MARKER}"
    fi
    exit 0
    ;;
esac

echo "unexpected git invocation: $*" >&2
exit 2
`

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

func TestFetchBaseUsesMergeBaseAsCompareRef(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell action tests require bash")
	}

	res := runFetchBaseScenario(t, map[string]string{
		"DRYDOCK_TEST_MERGE_BASE_MODE": "immediate",
		"DRYDOCK_TEST_MERGE_BASE_SHA":  "abc123mergebase",
	})

	if got := res.output["compare-ref"]; got != "abc123mergebase" {
		t.Fatalf("compare-ref = %q, want the merge-base sha abc123mergebase", got)
	}
	if got := res.output["base-ref"]; got != "master" {
		t.Fatalf("base-ref = %q, want master", got)
	}
	if strings.Contains(res.fetchLog, "--deepen") {
		t.Fatalf("fetch log = %q, want no deepen when merge base resolves immediately", res.fetchLog)
	}
}

func TestFetchBaseDeepensBaseOnlyWhenHeadShaUnavailable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell action tests require bash")
	}

	gitDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(gitDir, "shallow"), []byte("shallow\n"), 0o600); err != nil {
		t.Fatalf("write shallow marker: %v", err)
	}

	res := runFetchBaseScenario(t, map[string]string{
		"DRYDOCK_TEST_MERGE_BASE_MODE": "after-deepen",
		"DRYDOCK_TEST_MERGE_BASE_SHA":  "deep123mergebase",
		"DRYDOCK_TEST_GIT_DIR":         gitDir,
		// Empty head sha models git rev-parse failing; only the base is deepened.
		"DRYDOCK_TEST_HEAD_SHA":      "",
		"DRYDOCK_TEST_DEEPEN_MARKER": filepath.Join(t.TempDir(), "deepened"),
	})

	if got := res.output["compare-ref"]; got != "deep123mergebase" {
		t.Fatalf("compare-ref = %q, want the merge-base sha deep123mergebase", got)
	}
	if !strings.Contains(res.fetchLog, "--deepen") || !strings.Contains(res.fetchLog, "refs/remotes/origin/master") {
		t.Fatalf("fetch log = %q, want a deepen fetch of the base ref", res.fetchLog)
	}
}

// TestFetchBaseResolvesMergeBaseForDivergedShallowClone exercises fetch-base.sh
// against a real git repository: a depth-1 checkout of a head that is behind
// its base. This is the case that the fake-git tests cannot model, because the
// defect it guards against (deepening only the head, not the base) depends on
// real git fetch semantics, not on the command arguments.
func TestFetchBaseResolvesMergeBaseForDivergedShallowClone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell action tests require bash")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	root := t.TempDir()
	git := func(dir string, args ...string) string {
		t.Helper()
		full := append([]string{
			"-c", "user.email=t@example.invalid",
			"-c", "user.name=drydock test",
			"-c", "init.defaultBranch=main",
			"-c", "protocol.file.allow=always",
			// Disable background maintenance: a detached auto-gc can still be
			// writing .git/objects when t.TempDir cleanup runs (flaky
			// "directory not empty" failures on CI).
			"-c", "gc.auto=0",
			"-c", "gc.autoDetach=false",
			"-c", "maintenance.auto=false",
		}, args...)
		cmd := exec.Command("git", full...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	upstream := filepath.Join(root, "up.git")
	work := filepath.Join(root, "work")
	git(root, "init", "--bare", upstream)
	git(root, "init", work)

	commit := func(msg, file, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(work, file), []byte(content+"\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", file, err)
		}
		git(work, "add", file)
		git(work, "commit", "-m", msg)
	}

	for _, c := range []string{"c1", "c2", "c3"} {
		commit(c, "f", c)
	}
	git(work, "branch", "feat")
	for _, m := range []string{"m4", "m5", "m6", "m7", "m8"} {
		commit(m, "f", m)
	}
	git(work, "checkout", "feat")
	commit("feat1", "g", "feat1")
	git(work, "remote", "add", "origin", upstream)
	git(work, "push", "origin", "main", "feat")

	wantMergeBase := git(work, "merge-base", "main", "feat")

	// Depth-1 clone of the feat head, simulating actions/checkout fetch-depth: 1.
	shallow := filepath.Join(root, "shallow")
	git(root, "clone", "--depth=1", "--branch", "feat", "file://"+upstream, shallow)
	if shallowFile := filepath.Join(shallow, ".git", "shallow"); !fileExists(shallowFile) {
		t.Fatalf("expected a shallow clone at %s", shallowFile)
	}
	// A single-branch shallow clone does not set origin/HEAD (matching
	// actions/checkout), so the symref assertion below pins fetch-base's
	// git remote set-head call.

	scriptPath, err := filepath.Abs("fetch-base.sh")
	if err != nil {
		t.Fatalf("resolve script path: %v", err)
	}
	outputPath := filepath.Join(root, "github-output")

	cmd := exec.Command("bash", scriptPath)
	cmd.Dir = shallow
	cmd.Env = append(os.Environ(),
		"DRYDOCK_PR_BASE_REF=main",
		"GITHUB_OUTPUT="+outputPath,
		"GITHUB_SERVER_URL=https://github.com",
		// gc.auto/autoDetach/maintenance.auto: keep git from detaching
		// background maintenance that races t.TempDir cleanup.
		"GIT_CONFIG_COUNT=4",
		"GIT_CONFIG_KEY_0=protocol.file.allow",
		"GIT_CONFIG_VALUE_0=always",
		"GIT_CONFIG_KEY_1=gc.auto",
		"GIT_CONFIG_VALUE_1=0",
		"GIT_CONFIG_KEY_2=gc.autoDetach",
		"GIT_CONFIG_VALUE_2=false",
		"GIT_CONFIG_KEY_3=maintenance.auto",
		"GIT_CONFIG_VALUE_3=false",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fetch-base.sh failed: %v\n%s", err, out)
	}

	if compareRef := compareRefFromOutput(t, outputPath); compareRef != wantMergeBase {
		t.Fatalf("compare-ref = %q, want the true merge base %q", compareRef, wantMergeBase)
	}

	// fetch-base records the base branch as origin's HEAD symref so drydock's
	// diff self-repo gate can map sources pinned to the base branch NAME
	// (targetRevision: main) to the per-side trees.
	if symref := git(shallow, "symbolic-ref", "refs/remotes/origin/HEAD"); symref != "refs/remotes/origin/main" {
		t.Fatalf("origin/HEAD symref = %q, want refs/remotes/origin/main", symref)
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// compareRefFromOutput returns the compare-ref value fetch-base.sh wrote to a
// GITHUB_OUTPUT file, or "" if absent.
func compareRefFromOutput(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read github output: %v", err)
	}
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		if key, value, found := strings.Cut(line, "="); found && key == "compare-ref" {
			return value
		}
	}
	return ""
}

func TestFetchBaseFallsBackToBaseTipWithoutMergeBase(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell action tests require bash")
	}

	// Not shallow (no shallow marker) and merge base never resolves: keep the
	// pre-merge-base behavior of diffing against the base tip, with a warning.
	res := runFetchBaseScenario(t, map[string]string{
		"DRYDOCK_TEST_MERGE_BASE_MODE": "never",
		"DRYDOCK_TEST_GIT_DIR":         t.TempDir(),
	})

	if got := res.output["compare-ref"]; got != "origin/master" {
		t.Fatalf("compare-ref = %q, want fallback origin/master", got)
	}
	if !strings.Contains(res.stderr, "could not determine the merge base") {
		t.Fatalf("stderr = %q, want a merge-base warning", res.stderr)
	}
	if strings.Contains(res.fetchLog, "--deepen") {
		t.Fatalf("fetch log = %q, want no deepen when the repository is not shallow", res.fetchLog)
	}
}

func runFetchBase(t *testing.T, existingAuthHeader bool) string {
	t.Helper()

	tmp := t.TempDir()
	resultPath := filepath.Join(tmp, "fetch-args")

	existingValue := "false"
	if existingAuthHeader {
		existingValue = "true"
	}

	res := runFetchBaseScenario(t, map[string]string{
		"DRYDOCK_TEST_EXISTING_AUTH_HEADER": existingValue,
		"DRYDOCK_TEST_FETCH_ARGS":           resultPath,
	})
	return res.fetchArgs
}

type fetchBaseResult struct {
	fetchArgs string
	fetchLog  string
	output    map[string]string
	stderr    string
}

func runFetchBaseScenario(t *testing.T, extraEnv map[string]string) fetchBaseResult {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell action tests require bash")
	}

	tmp := t.TempDir()
	gitPath := filepath.Join(tmp, "git")
	if err := os.WriteFile(gitPath, []byte(fakeGitScript), 0o700); err != nil {
		t.Fatalf("write fake git: %v", err)
	}

	outputPath := filepath.Join(tmp, "github-output")
	fetchLogPath := filepath.Join(tmp, "fetch-log")

	env := append(os.Environ(),
		"PATH="+tmp+string(os.PathListSeparator)+os.Getenv("PATH"),
		"DRYDOCK_GITHUB_TOKEN=test-token",
		"DRYDOCK_PR_BASE_REF=master",
		"GITHUB_OUTPUT="+outputPath,
		"GITHUB_SERVER_URL=https://github.com",
		"DRYDOCK_TEST_FETCH_LOG="+fetchLogPath,
	)
	for key, value := range extraEnv {
		env = append(env, key+"="+value)
	}

	cmd := exec.Command("bash", "fetch-base.sh")
	cmd.Dir = "."
	cmd.Env = env

	var stderr strings.Builder
	cmd.Stderr = &stderr
	if out, err := cmd.Output(); err != nil {
		t.Fatalf("fetch-base.sh failed: %v\nstdout: %s\nstderr: %s", err, out, stderr.String())
	}

	result := fetchBaseResult{
		output: map[string]string{},
		stderr: stderr.String(),
	}
	if data, err := os.ReadFile(outputPath); err == nil {
		for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
			key, value, found := strings.Cut(line, "=")
			if found {
				result.output[key] = value
			}
		}
	}
	if data, err := os.ReadFile(fetchLogPath); err == nil {
		result.fetchLog = string(data)
	}
	if fetchArgs, ok := extraEnv["DRYDOCK_TEST_FETCH_ARGS"]; ok {
		if data, err := os.ReadFile(fetchArgs); err == nil {
			result.fetchArgs = string(data)
		}
	}
	return result
}

// TestFetchBaseForceUpdatesStaleRemoteTrackingRef reproduces the persistent
// self-hosted runner failure. A prior run leaves refs/remotes/origin/<base>
// pinned to an old, shallow tip; the next run's depth-1 fetch downloads only
// the new tip, so git cannot prove the update is a fast-forward. A refspec
// without a leading '+' is then rejected ("! [rejected] ... non-fast-forward")
// and the action exits 1. The remote-tracking ref must be force-updated, just
// as git's default clone refspec (+refs/heads/*:refs/remotes/origin/*) does.
//
// Hosted runners never hit this because their workspace is fresh each run, so
// the ref does not pre-exist; only the persistent runner exposes it. The fake
// git cannot model it — the defect lives in real fetch fast-forward semantics,
// not in the command arguments.
func TestFetchBaseForceUpdatesStaleRemoteTrackingRef(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell action tests require bash")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	root := t.TempDir()
	git := func(dir string, args ...string) string {
		t.Helper()
		full := append([]string{
			"-c", "user.email=t@example.invalid",
			"-c", "user.name=drydock test",
			"-c", "init.defaultBranch=main",
			"-c", "protocol.file.allow=always",
			// Disable background maintenance: a detached auto-gc can still be
			// writing .git/objects when t.TempDir cleanup runs (flaky
			// "directory not empty" failures on CI).
			"-c", "gc.auto=0",
			"-c", "gc.autoDetach=false",
			"-c", "maintenance.auto=false",
		}, args...)
		cmd := exec.Command("git", full...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	upstream := filepath.Join(root, "up.git")
	work := filepath.Join(root, "work")
	git(root, "init", "--bare", upstream)
	git(root, "init", work)

	commit := func(msg, file, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(work, file), []byte(content+"\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", file, err)
		}
		git(work, "add", file)
		git(work, "commit", "-m", msg)
	}

	// Base history, then a PR branch off the base tip.
	for _, c := range []string{"c1", "c2", "c3"} {
		commit(c, "f", c)
	}
	git(work, "branch", "feat")
	git(work, "checkout", "feat")
	commit("feat1", "g", "feat1")
	git(work, "checkout", "main")
	git(work, "remote", "add", "origin", upstream)
	git(work, "push", "origin", "main", "feat")

	staleMainSHA := git(work, "rev-parse", "main")

	// Persistent runner workspace: a depth-1 checkout of the PR head plus a
	// stale depth-1 remote-tracking ref for the base branch, exactly as a
	// prior run on the same runner would have left it.
	workspace := filepath.Join(root, "workspace")
	git(root, "clone", "--depth=1", "--branch", "feat", "file://"+upstream, workspace)
	git(workspace, "fetch", "--no-tags", "--depth=1", "origin", "main:refs/remotes/origin/main")
	if got := git(workspace, "rev-parse", "refs/remotes/origin/main"); got != staleMainSHA {
		t.Fatalf("setup: origin/main = %q, want stale tip %q", got, staleMainSHA)
	}

	// The base branch advances after the workspace captured the stale ref.
	for _, m := range []string{"m4", "m5", "m6"} {
		commit(m, "f", m)
	}
	git(work, "push", "origin", "main")
	newMainSHA := git(work, "rev-parse", "main")
	wantMergeBase := git(work, "merge-base", "main", "feat")

	scriptPath, err := filepath.Abs("fetch-base.sh")
	if err != nil {
		t.Fatalf("resolve script path: %v", err)
	}
	outputPath := filepath.Join(root, "github-output")

	cmd := exec.Command("bash", scriptPath)
	cmd.Dir = workspace
	cmd.Env = append(os.Environ(),
		"DRYDOCK_PR_BASE_REF=main",
		"GITHUB_OUTPUT="+outputPath,
		"GITHUB_SERVER_URL=https://github.com",
		// gc.auto/autoDetach/maintenance.auto: keep git from detaching
		// background maintenance that races t.TempDir cleanup.
		"GIT_CONFIG_COUNT=4",
		"GIT_CONFIG_KEY_0=protocol.file.allow",
		"GIT_CONFIG_VALUE_0=always",
		"GIT_CONFIG_KEY_1=gc.auto",
		"GIT_CONFIG_VALUE_1=0",
		"GIT_CONFIG_KEY_2=gc.autoDetach",
		"GIT_CONFIG_VALUE_2=false",
		"GIT_CONFIG_KEY_3=maintenance.auto",
		"GIT_CONFIG_VALUE_3=false",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fetch-base.sh failed against a stale shallow remote-tracking ref: %v\n%s", err, out)
	}

	// The stale remote-tracking ref must have been force-updated to the new tip.
	if got := git(workspace, "rev-parse", "refs/remotes/origin/main"); got != newMainSHA {
		t.Fatalf("origin/main = %q, want force-update to the new base tip %q", got, newMainSHA)
	}

	// And the compare ref should still resolve to the true merge base.
	if compareRef := compareRefFromOutput(t, outputPath); compareRef != wantMergeBase {
		t.Fatalf("compare-ref = %q, want the true merge base %q", compareRef, wantMergeBase)
	}
}
