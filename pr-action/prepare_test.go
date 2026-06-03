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
