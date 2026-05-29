package praction

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRunFallsBackToJSONWhenImageNameOutputIsUnsupported(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell action tests require bash")
	}

	tmp := t.TempDir()
	writeExecutable(t, filepath.Join(tmp, "drydock"), `#!/usr/bin/env bash
set -euo pipefail

if [[ "$*" == *"diff images"* && "$*" == *"-o name"* ]]; then
  echo "name output is not supported for diff images" >&2
  exit 2
fi

if [[ "$*" == *"diff images"* && "$*" == *"-o json"* ]]; then
  printf '{"added":["example/app:v2"],"removed":["example/app:v1"],"unchanged":["example/sidecar:v1"]}\n'
  exit 0
fi

echo "unexpected drydock invocation: $*" >&2
exit 2
`)
	writeExecutable(t, filepath.Join(tmp, "jq"), `#!/usr/bin/env bash
set -euo pipefail

if [[ "$1" == "-r" ]]; then
  shift
fi

case "$1" in
  '(.added // [])[]')
    printf 'example/app:v2\n'
    ;;
  '((.added // []) | length) + ((.removed // []) | length)')
    printf '2\n'
    ;;
  *)
    echo "unexpected jq expression: $1" >&2
    exit 2
    ;;
esac
`)

	workDir := filepath.Join(tmp, "work")
	outputPath := filepath.Join(tmp, "github-output")
	cmd := exec.Command("bash", "run.sh")
	cmd.Dir = "."
	cmd.Env = append(defaultRunEnv(tmp, workDir, outputPath),
		"DRYDOCK_INPUT_RUN_IMAGE_DIFF=true",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run.sh failed: %v\n%s%s", err, out, runDebugOutput(t, workDir))
	}
	if strings.Contains(string(out), `"unchanged"`) {
		t.Fatalf("run.sh output leaked JSON fallback payload:\n%s", out)
	}
	if strings.Contains(string(out), "name output is not supported for diff images") {
		t.Fatalf("run.sh output leaked fallback probe error:\n%s", out)
	}

	images, err := os.ReadFile(filepath.Join(workDir, "added-images.txt"))
	if err != nil {
		t.Fatalf("read added images: %v", err)
	}
	if got, want := string(images), "example/app:v2\n"; got != want {
		t.Fatalf("added images = %q, want %q", got, want)
	}

	output, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read GITHUB_OUTPUT: %v", err)
	}
	for _, want := range []string{"has-images=true", "has-image-diff=true"} {
		if !strings.Contains(string(output), want) {
			t.Fatalf("GITHUB_OUTPUT = %q, want %q", output, want)
		}
	}
}

func runDebugOutput(t *testing.T, workDir string) string {
	t.Helper()

	var out strings.Builder
	for _, name := range []string{"images.err", "images.json", "images.count", "added-images.txt"} {
		path := filepath.Join(workDir, name)
		body, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		out.WriteString("\n")
		out.WriteString(name)
		out.WriteString(":\n")
		out.Write(body)
	}
	return out.String()
}

func defaultRunEnv(tmp, workDir, outputPath string) []string {
	env := append(os.Environ(),
		"PATH="+tmp+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GITHUB_OUTPUT="+outputPath,
		"DRYDOCK_ACTION_WORK_DIR="+workDir,
		"DRYDOCK_BASE_COMPARE_REF=origin/main",
		"DRYDOCK_BIN=drydock",
		"DRYDOCK_CACHE_PATH=",
		"DRYDOCK_DIFF_ARTIFACT_NAME=diff",
		"DRYDOCK_IMAGE_ARTIFACT_NAME=images",
		"DRYDOCK_INPUT_CHANGED_ONLY=",
		"DRYDOCK_INPUT_COMMENT_EMPTY=false",
		"DRYDOCK_INPUT_COMMENT_MODE=none",
		"DRYDOCK_INPUT_DIFF_MAX_BYTES=61000",
		"DRYDOCK_INPUT_DISABLE_PLUGIN_POLICY=false",
		"DRYDOCK_INPUT_DISCOVER_KUSTOMIZE=",
		"DRYDOCK_INPUT_ENABLE_AVP_COMPAT=false",
		"DRYDOCK_INPUT_ENABLE_PLUGINS=false",
		"DRYDOCK_INPUT_EXTRA_DIFF_ARGS=",
		"DRYDOCK_INPUT_EXTRA_IMAGE_DIFF_ARGS=",
		"DRYDOCK_INPUT_EXTRA_TEST_ARGS=",
		"DRYDOCK_INPUT_FAIL_ON_DIFF=false",
		"DRYDOCK_INPUT_FAIL_ON_IMAGE_DIFF=false",
		"DRYDOCK_INPUT_FAIL_ON_RENDER_ERROR=true",
		"DRYDOCK_INPUT_HEAD_REF=",
		"DRYDOCK_INPUT_MAX_DISCOVERY_DEPTH=",
		"DRYDOCK_INPUT_OFFLINE=false",
		"DRYDOCK_INPUT_PARALLELISM=",
		"DRYDOCK_INPUT_PATH=.",
		"DRYDOCK_INPUT_PLUGIN_POLICY_PATH=",
		"DRYDOCK_INPUT_PLUGIN_POLICY_REF=",
		"DRYDOCK_INPUT_PLUGIN_POLICY_REPO=",
		"DRYDOCK_INPUT_REPO=.",
		"DRYDOCK_INPUT_REPO_MAP=",
		"DRYDOCK_INPUT_RUN_DIFF=false",
		"DRYDOCK_INPUT_RUN_IMAGE_DIFF=false",
		"DRYDOCK_INPUT_RUN_TEST=false",
		"DRYDOCK_INPUT_SHOW_IGNORED_FIELDS=false",
		"DRYDOCK_INPUT_SKIP_SECRETS=true",
		"DRYDOCK_INPUT_STRICT=false",
		"DRYDOCK_INPUT_STRICT_CHANGED_ONLY=false",
	)
	return env
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
