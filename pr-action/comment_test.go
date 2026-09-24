package praction

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// commentTestToken is the fake credential handed to comment.sh. Tests assert
// it reaches the binary's environment and never its argv or the runner log.
const commentTestToken = "s3cret-token-value"

// runComment executes comment.sh with the given environment overlay and
// returns the combined stdout+stderr output together with the exit code.
func runComment(t *testing.T, env []string) (string, int) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell action tests require bash")
	}

	cmd := exec.Command("bash", "comment.sh")
	cmd.Dir = "."
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("comment.sh failed: %v\n%s", err, out)
	}
	return string(out), exitErr.ExitCode()
}

// defaultCommentEnv builds a minimal environment for comment.sh tests, with
// the drydock binary resolved from tmp (prepended to PATH).
func defaultCommentEnv(t *testing.T, tmp, commentPath string) []string {
	t.Helper()
	return append(
		withoutEnvKeys(os.Environ(),
			"PATH",
			"DRYDOCK_BIN",
			"DRYDOCK_COMMENT_MESSAGE_ID",
			"DRYDOCK_COMMENT_PATH",
			"DRYDOCK_PR_NUMBER",
			"DRYDOCK_GITHUB_TOKEN",
			"GITHUB_API_URL",
			"GITHUB_REPOSITORY",
		),
		"PATH="+tmp+string(os.PathListSeparator)+os.Getenv("PATH"),
		"DRYDOCK_BIN=drydock",
		"DRYDOCK_COMMENT_MESSAGE_ID=12/drydock-diff",
		"DRYDOCK_COMMENT_PATH="+commentPath,
		"DRYDOCK_PR_NUMBER=12",
		"DRYDOCK_GITHUB_TOKEN="+commentTestToken,
		"GITHUB_API_URL=https://forgejo.example/api/v1",
		"GITHUB_REPOSITORY=o/r",
	)
}

// writeCommentBody drops a comment body next to the stub binary and returns
// its path.
func writeCommentBody(t *testing.T, tmp string) string {
	t.Helper()

	path := filepath.Join(tmp, "comment.md")
	if err := os.WriteFile(path, []byte("rendered diff\n"), 0o600); err != nil {
		t.Fatalf("write comment body: %v", err)
	}
	return path
}

func TestCommentFailsWhenBinaryLacksPRCommentSubcommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell action tests require bash")
	}

	tmp := t.TempDir()
	commentPath := writeCommentBody(t, tmp)

	// A drydock release that predates `pr comment` rejects the probe.
	writeExecutable(t, filepath.Join(tmp, "drydock"), `#!/usr/bin/env bash
printf '%s\n' "$*" >> "`+tmp+`/drydock-args.txt"
echo 'unknown command "pr" for "drydock"' >&2
exit 2
`)

	out, code := runComment(t, defaultCommentEnv(t, tmp, commentPath))

	if code != 1 {
		t.Fatalf("comment.sh exit = %d, want 1\n%s", code, out)
	}
	for _, want := range []string{"pr comment", "version"} {
		if !strings.Contains(out, want) {
			t.Fatalf("comment.sh output = %q, want it to mention %q", out, want)
		}
	}

	args := readFile(t, filepath.Join(tmp, "drydock-args.txt"))
	if !strings.Contains(args, "pr comment --help") {
		t.Fatalf("drydock args = %q, want the capability probe", args)
	}
	if strings.Contains(args, "--body-file") {
		t.Fatalf("drydock args = %q, must not attempt a post after a failed probe", args)
	}
}

func TestCommentPassesFlagsAndKeepsTokenOffArgv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell action tests require bash")
	}

	tmp := t.TempDir()
	commentPath := writeCommentBody(t, tmp)

	writeExecutable(t, filepath.Join(tmp, "drydock"), `#!/usr/bin/env bash
set -euo pipefail
if [[ "$*" == *"--help"* ]]; then
  exit 0
fi
printf '%s\n' "$*" > "`+tmp+`/drydock-args.txt"
printf '%s\n' "${DRYDOCK_GITHUB_TOKEN:-<unset>}" > "`+tmp+`/drydock-token.txt"
echo "updated https://forgejo.example/o/r/pulls/12#issuecomment-4242"
`)

	out, code := runComment(t, defaultCommentEnv(t, tmp, commentPath))

	if code != 0 {
		t.Fatalf("comment.sh exit = %d, want 0\n%s", code, out)
	}

	args := readFile(t, filepath.Join(tmp, "drydock-args.txt"))
	for _, want := range []string{
		"pr comment",
		"--repository o/r",
		"--number 12",
		"--message-id 12/drydock-diff",
		"--body-file " + commentPath,
	} {
		if !strings.Contains(args, want) {
			t.Fatalf("drydock args = %q, want %q", args, want)
		}
	}
	if strings.Contains(args, commentTestToken) {
		t.Fatal("drydock args carry the token; it must travel in the environment only")
	}
	if strings.Contains(out, commentTestToken) {
		t.Fatal("comment.sh output carries the token")
	}

	token := strings.TrimSpace(readFile(t, filepath.Join(tmp, "drydock-token.txt")))
	if token != commentTestToken {
		t.Fatalf("DRYDOCK_GITHUB_TOKEN seen by drydock = %q, want the exported token", token)
	}
}

func TestCommentPropagatesPostFailureExitCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell action tests require bash")
	}

	tmp := t.TempDir()
	commentPath := writeCommentBody(t, tmp)

	writeExecutable(t, filepath.Join(tmp, "drydock"), `#!/usr/bin/env bash
set -euo pipefail
if [[ "$*" == *"--help"* ]]; then
  exit 0
fi
echo "POST /repos/o/r/issues/12/comments: HTTP 403" >&2
exit 2
`)

	out, code := runComment(t, defaultCommentEnv(t, tmp, commentPath))

	if code != 2 {
		t.Fatalf("comment.sh exit = %d, want the binary's 2\n%s", code, out)
	}
	if !strings.Contains(out, "HTTP 403") {
		t.Fatalf("comment.sh output = %q, want the binary's stderr", out)
	}
}
