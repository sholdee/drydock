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

func TestRunCommentsLinkWorkflowRunArtifactsWhenUploadEnabled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell action tests require bash")
	}

	workDir := runCommentScenario(t, commentScenario{
		uploadArtifacts: true,
		githubEnv:       true,
		diffOutput:      true,
		imageOutput:     true,
	})

	diffComment := readFile(t, filepath.Join(workDir, "diff-comment.md"))
	for _, want := range []string{
		"Full diff output: [diff artifact](https://github.example.test/example/repo/actions/runs/12345).",
		"~~~diff",
	} {
		if !strings.Contains(diffComment, want) {
			t.Fatalf("diff comment = %q, want %q", diffComment, want)
		}
	}
	if strings.Contains(diffComment, "Full output is available from the workflow artifacts") {
		t.Fatalf("diff comment still has vague artifact text:\n%s", diffComment)
	}

	imagesComment := readFile(t, filepath.Join(workDir, "images-comment.md"))
	if !strings.Contains(imagesComment, "`example/app:v2`") {
		t.Fatalf("images comment = %q, want added image", imagesComment)
	}
	if strings.Contains(imagesComment, "Full added image output") || strings.Contains(imagesComment, "actions/runs/12345") {
		t.Fatalf("images comment repeated artifact link while diff comment has one:\n%s", imagesComment)
	}
	if strings.Contains(imagesComment, "Full output is available from the workflow artifacts") {
		t.Fatalf("images comment has vague artifact text:\n%s", imagesComment)
	}
}

func TestRunImagesOnlyCommentLinksWorkflowRunArtifact(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell action tests require bash")
	}

	workDir := runCommentScenario(t, commentScenario{
		uploadArtifacts: true,
		githubEnv:       true,
		commentMode:     "images",
		diffOutput:      true,
		imageOutput:     true,
	})

	imagesComment := readFile(t, filepath.Join(workDir, "images-comment.md"))
	for _, want := range []string{
		"Full added image output: [images artifact](https://github.example.test/example/repo/actions/runs/12345).",
		"`example/app:v2`",
	} {
		if !strings.Contains(imagesComment, want) {
			t.Fatalf("images comment = %q, want %q", imagesComment, want)
		}
	}
}

func TestRunCommentsOmitArtifactLinksWhenUploadDisabled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell action tests require bash")
	}

	workDir := runCommentScenario(t, commentScenario{
		uploadArtifacts: false,
		githubEnv:       true,
		diffOutput:      true,
		imageOutput:     true,
	})

	for _, name := range []string{"diff-comment.md", "images-comment.md"} {
		comment := readFile(t, filepath.Join(workDir, name))
		if strings.Contains(comment, "actions/runs/12345") || strings.Contains(comment, "Full diff output") || strings.Contains(comment, "Full added image output") {
			t.Fatalf("%s contains artifact link when upload is disabled:\n%s", name, comment)
		}
		if strings.Contains(comment, "Full output is available from the workflow artifacts") {
			t.Fatalf("%s contains old vague artifact text:\n%s", name, comment)
		}
	}
}

func TestRunCommentsOmitArtifactLinksWhenGitHubRunURLIsUnavailable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell action tests require bash")
	}

	workDir := runCommentScenario(t, commentScenario{
		uploadArtifacts: true,
		githubEnv:       false,
		diffOutput:      true,
		imageOutput:     true,
	})

	for _, name := range []string{"diff-comment.md", "images-comment.md"} {
		comment := readFile(t, filepath.Join(workDir, name))
		if strings.Contains(comment, "Full diff output") || strings.Contains(comment, "Full added image output") || strings.Contains(comment, "actions/runs") {
			t.Fatalf("%s contains artifact link without GitHub run URL:\n%s", name, comment)
		}
		if strings.Contains(comment, "Full output is available from the workflow artifacts") {
			t.Fatalf("%s contains old vague artifact text:\n%s", name, comment)
		}
	}
}

func TestRunCommentEmptyDoesNotLinkArtifactsWithoutOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell action tests require bash")
	}

	workDir := runCommentScenario(t, commentScenario{
		uploadArtifacts: true,
		githubEnv:       true,
		commentEmpty:    true,
		diffOutput:      false,
		imageOutput:     false,
	})

	diffComment := readFile(t, filepath.Join(workDir, "diff-comment.md"))
	if !strings.Contains(diffComment, "No rendered manifest differences detected.") {
		t.Fatalf("diff comment = %q, want empty diff text", diffComment)
	}
	imagesComment := readFile(t, filepath.Join(workDir, "images-comment.md"))
	if !strings.Contains(imagesComment, "No added rendered images detected.") {
		t.Fatalf("images comment = %q, want empty images text", imagesComment)
	}
	for _, comment := range []string{diffComment, imagesComment} {
		if strings.Contains(comment, "artifact](") || strings.Contains(comment, "actions/runs/12345") {
			t.Fatalf("empty comment contains artifact link:\n%s", comment)
		}
	}
}

func TestRunDiffUsesMarkdownOutputAfterExtraDiffArgs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell action tests require bash")
	}

	workDir := runCommentScenario(t, commentScenario{
		uploadArtifacts: true,
		githubEnv:       true,
		diffOutput:      true,
		extraDiffArgs:   "-o json\n--raw-output-file /tmp/ignored\n--exit-code=true",
	})

	args := readFile(t, filepath.Join(workDir, "drydock-args.txt"))
	for _, want := range []string{"-o json", "--raw-output-file /tmp/ignored", "-o markdown", "--markdown-max-bytes 59488", "--raw-output-file " + filepath.Join(workDir, "diff.txt"), "--exit-code=false"} {
		if !strings.Contains(args, want) {
			t.Fatalf("drydock args = %q, want %q", args, want)
		}
	}
	if strings.Index(args, "-o json") > strings.Index(args, "-o markdown") {
		t.Fatalf("forced markdown output did not come after extra diff args:\n%s", args)
	}
	rawDiff := readFile(t, filepath.Join(workDir, "diff.txt"))
	if !strings.Contains(rawDiff, "diff --git a/apps/demo b/apps/demo") {
		t.Fatalf("raw diff artifact = %q, want raw diff", rawDiff)
	}
	diffComment := readFile(t, filepath.Join(workDir, "diff-comment.md"))
	if !strings.Contains(diffComment, "## drydock diff preview") {
		t.Fatalf("diff comment = %q, want markdown report", diffComment)
	}
}

type commentScenario struct {
	uploadArtifacts bool
	githubEnv       bool
	commentEmpty    bool
	commentMode     string
	diffOutput      bool
	imageOutput     bool
	extraDiffArgs   string
}

func runCommentScenario(t *testing.T, scenario commentScenario) string {
	t.Helper()

	tmp := t.TempDir()
	writeCommentScenarioDrydock(t, filepath.Join(tmp, "drydock"), scenario)

	workDir := filepath.Join(tmp, "work")
	outputPath := filepath.Join(tmp, "github-output")
	commentMode := scenario.commentMode
	if commentMode == "" {
		commentMode = "both"
	}
	env := append(defaultRunEnv(tmp, workDir, outputPath),
		"DRYDOCK_INPUT_COMMENT_MODE="+commentMode,
		"DRYDOCK_INPUT_EXTRA_DIFF_ARGS="+scenario.extraDiffArgs,
		"DRYDOCK_INPUT_RUN_DIFF=true",
		"DRYDOCK_INPUT_RUN_IMAGE_DIFF=true",
		"DRYDOCK_INPUT_UPLOAD_ARTIFACTS="+boolString(scenario.uploadArtifacts),
		"DRYDOCK_INPUT_COMMENT_EMPTY="+boolString(scenario.commentEmpty),
		"GITHUB_SERVER_URL=",
		"GITHUB_REPOSITORY=",
		"GITHUB_RUN_ID=",
	)
	if scenario.githubEnv {
		env = append(env,
			"GITHUB_SERVER_URL=https://github.example.test",
			"GITHUB_REPOSITORY=example/repo",
			"GITHUB_RUN_ID=12345",
		)
	}

	cmd := exec.Command("bash", "run.sh")
	cmd.Dir = "."
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run.sh failed: %v\n%s%s", err, out, runDebugOutput(t, workDir))
	}
	return workDir
}

func writeCommentScenarioDrydock(t *testing.T, path string, scenario commentScenario) {
	t.Helper()

	diffOutput := ""
	if scenario.diffOutput {
		diffOutput = "diff_body='diff --git a/apps/demo b/apps/demo\n+kind: ConfigMap\n'\n"
	}
	imageOutput := ""
	if scenario.imageOutput {
		imageOutput = "printf 'example/app:v2\\n'\n"
	}

	writeExecutable(t, path, `#!/usr/bin/env bash
set -euo pipefail
mkdir -p "${DRYDOCK_ACTION_WORK_DIR}"
printf '%s\n' "$*" >> "${DRYDOCK_ACTION_WORK_DIR}/drydock-args.txt"

if [[ "$*" == *"diff apps"* ]]; then
  `+diffOutput+`
  raw_output_file=""
  output="diff"
  while [[ "$#" -gt 0 ]]; do
    case "$1" in
      --raw-output-file)
        raw_output_file="$2"
        shift 2
        ;;
      -o | --output)
        output="$2"
        shift 2
        ;;
      --output=*)
        output="${1#--output=}"
        shift
        ;;
      *)
        shift
        ;;
    esac
  done
  if [[ -n "${raw_output_file}" ]]; then
    printf '%s' "${diff_body:-}" > "${raw_output_file}"
  fi
  if [[ "${output}" == "markdown" ]]; then
    if [[ -n "${diff_body:-}" ]]; then
      printf '## drydock diff preview\n\n~~~diff\n%s~~~\n' "${diff_body}"
    else
      printf '## drydock diff preview\n\nNo rendered manifest differences detected.\n'
    fi
  else
    printf '%s' "${diff_body:-}"
  fi
  exit 0
fi

if [[ "$*" == *"diff images"* ]]; then
  `+imageOutput+`  exit 0
fi

echo "unexpected drydock invocation: $*" >&2
exit 2
`)
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
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
		"DRYDOCK_INPUT_DIFF_MAX_BYTES=60000",
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
		"DRYDOCK_INPUT_UPLOAD_ARTIFACTS=true",
	)
	return env
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
