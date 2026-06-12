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
mkdir -p "${DRYDOCK_ACTION_WORK_DIR}"
printf '%s\n' "$*" >> "${DRYDOCK_ACTION_WORK_DIR}/drydock-args.txt"

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
  '(.removed // [])[]')
    printf 'example/app:v1\n'
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
		"DRYDOCK_INPUT_CHANGED_ONLY_INCLUDE=apps/**",
		"DRYDOCK_INPUT_CHANGED_ONLY_IGNORE=.github/**",
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

	args := readFile(t, filepath.Join(workDir, "drydock-args.txt"))
	for _, want := range []string{"-o name", "-o json", "--changed-only-include apps/**", "--changed-only-ignore .github/**"} {
		if !strings.Contains(args, want) {
			t.Fatalf("drydock args = %q, want %q", args, want)
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
	for _, want := range []string{"~~~diff"} {
		if !strings.Contains(diffComment, want) {
			t.Fatalf("diff comment = %q, want %q", diffComment, want)
		}
	}
	if strings.Contains(diffComment, "Full diff output") || strings.Contains(diffComment, "actions/runs/12345") {
		t.Fatalf("diff comment contains raw artifact footer from run.sh:\n%s", diffComment)
	}
	if strings.Contains(diffComment, "Full output is available from the workflow artifacts") {
		t.Fatalf("diff comment still has vague artifact text:\n%s", diffComment)
	}

	imagesComment := readFile(t, filepath.Join(workDir, "images-comment.md"))
	for _, want := range []string{"## drydock image diff", "| added | `example/app:v2` |", "| removed | `example/app:v1` |"} {
		if !strings.Contains(imagesComment, want) {
			t.Fatalf("images comment = %q, want %q", imagesComment, want)
		}
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
		"| added | `example/app:v2` |",
		"| removed | `example/app:v1` |",
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
	if !strings.Contains(imagesComment, "No rendered image differences detected.") {
		t.Fatalf("images comment = %q, want empty images text", imagesComment)
	}
	for _, comment := range []string{diffComment, imagesComment} {
		if strings.Contains(comment, "artifact](") || strings.Contains(comment, "actions/runs/12345") {
			t.Fatalf("empty comment contains artifact link:\n%s", comment)
		}
	}
}

func TestRunCommentEmptyDoesNotRunImageDiffWhenImageDiffDisabled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell action tests require bash")
	}

	workDir := runCommentScenario(t, commentScenario{
		commentMode:   "images",
		commentEmpty:  true,
		skipImageDiff: true,
	})

	args := readFile(t, filepath.Join(workDir, "drydock-args.txt"))
	if strings.Contains(args, "diff images") {
		t.Fatalf("drydock args = %q, did not expect diff images call", args)
	}
	imagesComment := readFile(t, filepath.Join(workDir, "images-comment.md"))
	if !strings.Contains(imagesComment, "No rendered image differences detected.") {
		t.Fatalf("images comment = %q, want empty images text", imagesComment)
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
	for _, want := range []string{"-o json", "--raw-output-file /tmp/ignored", "-o markdown", "--markdown-max-bytes 59488", "--raw-output-file " + filepath.Join(workDir, "diff.txt"), "--html-output-file " + filepath.Join(workDir, "diff.html"), "--exit-code=false"} {
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
	if !strings.Contains(diffComment, "## drydock diff") {
		t.Fatalf("diff comment = %q, want markdown report", diffComment)
	}
	if strings.Contains(diffComment, "Full diff output") || strings.Contains(diffComment, "actions/runs/12345") {
		t.Fatalf("diff comment contains raw artifact footer from run.sh:\n%s", diffComment)
	}
}

func TestRunOutputsHTMLDiffArtifactPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell action tests require bash")
	}

	workDir := runCommentScenario(t, commentScenario{
		diffOutput:                      true,
		omitDiffHTMLArtifactNameFromEnv: true,
	})

	htmlPath := filepath.Join(workDir, "drydock-diff-run-1.html")
	html := readFile(t, htmlPath)
	if !strings.Contains(html, "<html><body>drydock diff</body></html>") {
		t.Fatalf("diff.html = %q, want HTML diff artifact", html)
	}

	output := readFile(t, filepath.Join(filepath.Dir(workDir), "github-output"))
	for _, want := range []string{
		"diff-html-path=" + htmlPath,
		"diff-html-artifact-name=drydock-diff-run-1.html",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("GITHUB_OUTPUT = %q, want %q", output, want)
		}
	}
}

func TestRunPassesChangedOnlyPathFiltersOnlyToDiffCommands(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell action tests require bash")
	}

	workDir := runCommentScenario(t, commentScenario{
		commentMode:        "both",
		diffOutput:         true,
		imageOutput:        true,
		runTest:            true,
		changedOnlyInclude: "apps/**\nclusters/**",
		changedOnlyIgnore:  ".github/**\nmise.lock",
	})

	args := readFile(t, filepath.Join(workDir, "drydock-args.txt"))
	lines := strings.Split(strings.TrimSpace(args), "\n")
	if len(lines) != 4 {
		t.Fatalf("drydock invocations = %q, want test, diff apps, diff images name, diff images markdown", args)
	}
	for _, line := range lines {
		hasFilters := strings.Contains(line, "--changed-only-include apps/**") &&
			strings.Contains(line, "--changed-only-include clusters/**") &&
			strings.Contains(line, "--changed-only-ignore .github/**") &&
			strings.Contains(line, "--changed-only-ignore mise.lock")
		switch {
		case strings.Contains(line, "test apps"):
			if hasFilters {
				t.Fatalf("test apps received diff-only changed path filters:\n%s", line)
			}
		case strings.Contains(line, "diff apps") || strings.Contains(line, "diff images"):
			if !hasFilters {
				t.Fatalf("diff command missing changed path filters:\n%s", line)
			}
		default:
			t.Fatalf("unexpected drydock invocation:\n%s", line)
		}
	}
}

func TestRunPassesCacheDirsToEveryInvocationWhenCachePathSet(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell action tests require bash")
	}

	tmp := t.TempDir()
	cachePath := filepath.Join(tmp, "cache")
	workDir := runCommentScenario(t, commentScenario{
		commentMode: "both",
		diffOutput:  true,
		imageOutput: true,
		runTest:     true,
		cachePath:   cachePath,
	})

	args := readFile(t, filepath.Join(workDir, "drydock-args.txt"))
	lines := strings.Split(strings.TrimSpace(args), "\n")
	if len(lines) != 4 {
		t.Fatalf("drydock invocations = %q, want test, diff apps, diff images name, diff images markdown", args)
	}
	wantPlugin := "--plugin-cache-dir " + filepath.Join(cachePath, "plugin")
	wantRender := "--render-cache-dir " + filepath.Join(cachePath, "renders")
	for _, line := range lines {
		if !strings.Contains(line, wantPlugin) {
			t.Fatalf("drydock invocation missing plugin cache dir %q:\n%s", wantPlugin, line)
		}
		if !strings.Contains(line, wantRender) {
			t.Fatalf("drydock invocation missing render cache dir %q:\n%s", wantRender, line)
		}
	}
	for _, name := range []string{"git", "charts", "remotes", "plugin", "renders"} {
		path := filepath.Join(cachePath, name)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if !info.IsDir() {
			t.Fatalf("%s is not a directory", path)
		}
	}
}

func TestRunOmitsPluginCacheDirWhenCachePathEmpty(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell action tests require bash")
	}

	workDir := runCommentScenario(t, commentScenario{
		commentMode: "both",
		diffOutput:  true,
		imageOutput: true,
		runTest:     true,
	})

	args := readFile(t, filepath.Join(workDir, "drydock-args.txt"))
	lines := strings.Split(strings.TrimSpace(args), "\n")
	if len(lines) != 4 {
		t.Fatalf("drydock invocations = %q, want test, diff apps, diff images name, diff images markdown", args)
	}
	for _, line := range lines {
		if strings.Contains(line, "--plugin-cache-dir") {
			t.Fatalf("drydock invocation included plugin cache dir with empty DRYDOCK_CACHE_PATH:\n%s", line)
		}
		if strings.Contains(line, "--render-cache-dir") {
			t.Fatalf("drydock invocation included render cache dir with empty DRYDOCK_CACHE_PATH:\n%s", line)
		}
	}
}

func TestRunImageCommentsUseMarkdownAfterExtraImageArgs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell action tests require bash")
	}

	workDir := runCommentScenario(t, commentScenario{
		uploadArtifacts: true,
		githubEnv:       true,
		commentMode:     "images",
		imageOutput:     true,
		extraImageArgs:  "-o json\n--exit-code=true",
	})

	args := readFile(t, filepath.Join(workDir, "drydock-args.txt"))
	for _, want := range []string{"-o json", "--exit-code=true", "-o name", "-o markdown", "--markdown-max-bytes 59488", "--exit-code=false"} {
		if !strings.Contains(args, want) {
			t.Fatalf("drydock args = %q, want %q", args, want)
		}
	}
	if strings.Index(args, "-o json") > strings.Index(args, "-o name") {
		t.Fatalf("forced name output did not come after extra image args:\n%s", args)
	}
	if strings.LastIndex(args, "-o json") > strings.LastIndex(args, "-o markdown") {
		t.Fatalf("forced markdown output did not come after extra image args:\n%s", args)
	}
	imagesComment := readFile(t, filepath.Join(workDir, "images-comment.md"))
	for _, want := range []string{"## drydock image diff", "| added | `example/app:v2` |", "| removed | `example/app:v1` |"} {
		if !strings.Contains(imagesComment, want) {
			t.Fatalf("images comment = %q, want %q", imagesComment, want)
		}
	}
}

func TestRunRemovedOnlyImageDiffCommentsWithoutAddedImagesArtifact(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell action tests require bash")
	}

	workDir := runCommentScenario(t, commentScenario{
		uploadArtifacts: true,
		githubEnv:       true,
		commentMode:     "images",
		imageRemoved:    true,
	})

	output := readFile(t, filepath.Join(filepath.Dir(workDir), "github-output"))
	for _, want := range []string{"has-images=false", "has-image-diff=true", "images-comment=true"} {
		if !strings.Contains(output, want) {
			t.Fatalf("GITHUB_OUTPUT = %q, want %q", output, want)
		}
	}
	addedImages := readFile(t, filepath.Join(workDir, "added-images.txt"))
	if addedImages != "" {
		t.Fatalf("added-images.txt = %q, want empty for removed-only diff", addedImages)
	}
	imagesComment := readFile(t, filepath.Join(workDir, "images-comment.md"))
	if !strings.Contains(imagesComment, "| removed | `example/app:v1` |") {
		t.Fatalf("images comment = %q, want removed image markdown", imagesComment)
	}
	if strings.Contains(imagesComment, "Full added image output") {
		t.Fatalf("removed-only comment linked added image artifact:\n%s", imagesComment)
	}
}

func TestRunFallsBackToLegacyImageCommentWhenMarkdownUnsupported(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell action tests require bash")
	}

	workDir := runCommentScenario(t, commentScenario{
		uploadArtifacts:       true,
		githubEnv:             true,
		commentMode:           "images",
		imageOutput:           true,
		imageMarkdownDisabled: true,
	})

	imagesComment := readFile(t, filepath.Join(workDir, "images-comment.md"))
	for _, want := range []string{"## drydock image diff", "**Summary:** 1 added, 1 removed.", "| added | `example/app:v2` |", "| removed | `example/app:v1` |"} {
		if !strings.Contains(imagesComment, want) {
			t.Fatalf("images comment = %q, want %q", imagesComment, want)
		}
	}
}

func TestRunFallsBackToLegacyImageCommentForRemovedOnlyImageDiff(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell action tests require bash")
	}

	workDir := runCommentScenario(t, commentScenario{
		uploadArtifacts:       true,
		githubEnv:             true,
		commentMode:           "images",
		imageRemoved:          true,
		imageMarkdownDisabled: true,
	})

	imagesComment := readFile(t, filepath.Join(workDir, "images-comment.md"))
	for _, want := range []string{"## drydock image diff", "**Summary:** 0 added, 1 removed.", "| removed | `example/app:v1` |"} {
		if !strings.Contains(imagesComment, want) {
			t.Fatalf("images comment = %q, want %q", imagesComment, want)
		}
	}
	for _, notWant := range []string{"No rendered image differences detected.", "Full added image output"} {
		if strings.Contains(imagesComment, notWant) {
			t.Fatalf("images comment = %q, did not want %q", imagesComment, notWant)
		}
	}
}

func TestRunFallsBackToGenericImageCommentWhenLegacyJSONFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell action tests require bash")
	}

	workDir := runCommentScenario(t, commentScenario{
		uploadArtifacts:       true,
		githubEnv:             true,
		commentMode:           "images",
		imageRemoved:          true,
		imageMarkdownDisabled: true,
		imageJSONDisabled:     true,
	})

	imagesComment := readFile(t, filepath.Join(workDir, "images-comment.md"))
	for _, want := range []string{
		"## drydock image diff",
		"**Summary:** image differences detected.",
		"Image differences detected, but detailed image rows are unavailable because this drydock binary does not support markdown image output.",
	} {
		if !strings.Contains(imagesComment, want) {
			t.Fatalf("images comment = %q, want %q", imagesComment, want)
		}
	}
	for _, notWant := range []string{"**Summary:** 0 added, 0 removed.", "No rendered image differences detected.", "| removed |"} {
		if strings.Contains(imagesComment, notWant) {
			t.Fatalf("images comment = %q, did not want %q", imagesComment, notWant)
		}
	}
}

type commentScenario struct {
	uploadArtifacts                 bool
	githubEnv                       bool
	commentEmpty                    bool
	commentMode                     string
	diffOutput                      bool
	imageOutput                     bool
	imageRemoved                    bool
	imageMarkdownDisabled           bool
	imageJSONDisabled               bool
	skipImageDiff                   bool
	runTest                         bool
	extraDiffArgs                   string
	extraImageArgs                  string
	changedOnlyInclude              string
	changedOnlyIgnore               string
	omitDiffHTMLArtifactNameFromEnv bool
	cachePath                       string
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
		"DRYDOCK_INPUT_EXTRA_IMAGE_DIFF_ARGS="+scenario.extraImageArgs,
		"DRYDOCK_INPUT_CHANGED_ONLY_INCLUDE="+scenario.changedOnlyInclude,
		"DRYDOCK_INPUT_CHANGED_ONLY_IGNORE="+scenario.changedOnlyIgnore,
		"DRYDOCK_INPUT_RUN_DIFF=true",
		"DRYDOCK_INPUT_RUN_IMAGE_DIFF="+boolString(!scenario.skipImageDiff),
		"DRYDOCK_INPUT_RUN_TEST="+boolString(scenario.runTest),
		"DRYDOCK_INPUT_UPLOAD_ARTIFACTS="+boolString(scenario.uploadArtifacts),
		"DRYDOCK_INPUT_COMMENT_EMPTY="+boolString(scenario.commentEmpty),
	)
	if scenario.githubEnv {
		env = setEnv(env, "GITHUB_SERVER_URL", "https://github.example.test")
		env = setEnv(env, "GITHUB_REPOSITORY", "example/repo")
		env = setEnv(env, "GITHUB_RUN_ID", "12345")
	}
	if scenario.omitDiffHTMLArtifactNameFromEnv {
		env = withoutEnv(env, "DRYDOCK_DIFF_HTML_ARTIFACT_NAME")
	}
	if scenario.cachePath != "" {
		env = append(withoutEnv(env, "DRYDOCK_CACHE_PATH"), "DRYDOCK_CACHE_PATH="+scenario.cachePath)
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
	imageAdded := ""
	if scenario.imageOutput {
		imageAdded = "example/app:v2"
	}
	imageRemoved := ""
	if scenario.imageOutput || scenario.imageRemoved {
		imageRemoved = "example/app:v1"
	}
	imageMarkdownSupport := "true"
	if scenario.imageMarkdownDisabled {
		imageMarkdownSupport = "false"
	}
	imageJSONSupport := "true"
	if scenario.imageJSONDisabled {
		imageJSONSupport = "false"
	}

	writeExecutable(t, path, `#!/usr/bin/env bash
set -euo pipefail
mkdir -p "${DRYDOCK_ACTION_WORK_DIR}"
printf '%s\n' "$*" >> "${DRYDOCK_ACTION_WORK_DIR}/drydock-args.txt"

if [[ "$*" == *"test apps"* ]]; then
  exit 0
fi

if [[ "$*" == *"diff apps"* ]]; then
  `+diffOutput+`
  raw_output_file=""
  html_output_file=""
  output="diff"
  while [[ "$#" -gt 0 ]]; do
    case "$1" in
      --raw-output-file)
        raw_output_file="$2"
        shift 2
        ;;
      --html-output-file)
        html_output_file="$2"
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
  if [[ -n "${html_output_file}" ]]; then
    printf '<html><body>drydock diff</body></html>\n' > "${html_output_file}"
  fi
  if [[ "${output}" == "markdown" ]]; then
    if [[ -n "${diff_body:-}" ]]; then
      printf '## drydock diff\n\n~~~diff\n%s~~~\n' "${diff_body}"
    else
      printf '## drydock diff\n\n**Summary:** 0 apps, 0 resources, +0/-0.\n\nNo rendered manifest differences detected.\n'
    fi
  else
    printf '%s' "${diff_body:-}"
  fi
  exit 0
fi

if [[ "$*" == *"diff images"* ]]; then
  output="diff"
  while [[ "$#" -gt 0 ]]; do
    case "$1" in
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
  image_added="`+imageAdded+`"
  image_removed="`+imageRemoved+`"
  image_markdown_support="`+imageMarkdownSupport+`"
  image_json_support="`+imageJSONSupport+`"
  case "${output}" in
    name)
      if [[ -n "${image_added}" ]]; then
        printf '%s\n' "${image_added}"
      fi
      if [[ -n "${image_added}" || -n "${image_removed}" ]]; then
        exit 1
      fi
      exit 0
      ;;
    markdown)
      if [[ "${image_markdown_support}" != "true" ]]; then
        echo 'markdown output is not supported for diff images' >&2
        exit 2
      fi
      printf '## drydock image diff\n\n'
      if [[ -n "${image_added}" || -n "${image_removed}" ]]; then
        printf '**Summary:**'
        if [[ -n "${image_added}" ]]; then
          printf ' 1 added'
        else
          printf ' 0 added'
        fi
        if [[ -n "${image_removed}" ]]; then
          printf ', 1 removed'
        else
          printf ', 0 removed'
        fi
        printf '.\n\n| Change | Image |\n| --- | --- |\n'
        if [[ -n "${image_added}" ]]; then
          printf '| added | \x60%s\x60 |\n' "${image_added}"
        fi
        if [[ -n "${image_removed}" ]]; then
          printf '| removed | \x60%s\x60 |\n' "${image_removed}"
        fi
      else
        printf '**Summary:** 0 added, 0 removed.\n\nNo rendered image differences detected.\n'
      fi
      exit 0
      ;;
    json)
      if [[ "${image_json_support}" != "true" ]]; then
        echo 'json output failed' >&2
        exit 2
      fi
      printf '{"added":['
      if [[ -n "${image_added}" ]]; then
        printf '"%s"' "${image_added}"
      fi
      printf '],"removed":['
      if [[ -n "${image_removed}" ]]; then
        printf '"%s"' "${image_removed}"
      fi
      printf '],"unchanged":[]}\n'
      exit 0
      ;;
    *)
      echo "unexpected image output: ${output}" >&2
      exit 2
      ;;
  esac
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

func withoutEnv(env []string, key string) []string {
	prefix := key + "="
	filtered := env[:0]
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func setEnv(env []string, key, value string) []string {
	return append(withoutEnv(env, key), key+"="+value)
}

func TestDefaultRunEnvFiltersAmbientActionVariables(t *testing.T) {
	t.Setenv("GITHUB_RUN_ID", "ambient-run")
	t.Setenv("GITHUB_RUN_ATTEMPT", "99")
	t.Setenv("GITHUB_SERVER_URL", "https://ambient.example.test")
	t.Setenv("GITHUB_REPOSITORY", "ambient/repo")
	t.Setenv("GITHUB_OUTPUT", "/ambient/output")
	t.Setenv("GITHUB_STEP_SUMMARY", "/ambient/summary")
	t.Setenv("DRYDOCK_DIFF_HTML_ARTIFACT_NAME", "ambient.html")
	t.Setenv("DRYDOCK_INPUT_RUN_DIFF", "ambient")

	env := defaultRunEnv(t.TempDir(), "/test/work", "/test/output")

	for key, want := range map[string]string{
		"DRYDOCK_ACTION_WORK_DIR":             "/test/work",
		"DRYDOCK_DIFF_HTML_ARTIFACT_NAME":     "diff.html",
		"DRYDOCK_INPUT_DISABLE_PLUGIN_POLICY": "false",
		"DRYDOCK_INPUT_ENABLE_AVP_COMPAT":     "false",
		"DRYDOCK_INPUT_RUN_DIFF":              "false",
		"DRYDOCK_INPUT_UPLOAD_ARTIFACTS":      "true",
		"GITHUB_OUTPUT":                       "/test/output",
		"GITHUB_REPOSITORY":                   "",
		"GITHUB_RUN_ATTEMPT":                  "",
		"GITHUB_RUN_ID":                       "",
		"GITHUB_SERVER_URL":                   "",
		"GITHUB_STEP_SUMMARY":                 "",
	} {
		values := envValues(env, key)
		if len(values) != 1 {
			t.Fatalf("%s values = %#v, want exactly one", key, values)
		}
		if values[0] != want {
			t.Fatalf("%s = %q, want %q", key, values[0], want)
		}
	}
}

func envValues(env []string, key string) []string {
	prefix := key + "="
	var values []string
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, prefix); ok {
			values = append(values, value)
		}
	}
	return values
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
	env := withoutEnvKeys(os.Environ(),
		"DRYDOCK_ACTION_WORK_DIR",
		"DRYDOCK_BASE_COMPARE_REF",
		"DRYDOCK_BIN",
		"DRYDOCK_CACHE_PATH",
		"DRYDOCK_DIFF_ARTIFACT_NAME",
		"DRYDOCK_DIFF_HTML_ARTIFACT_NAME",
		"DRYDOCK_IMAGE_ARTIFACT_NAME",
		"GITHUB_OUTPUT",
		"GITHUB_REPOSITORY",
		"GITHUB_RUN_ATTEMPT",
		"GITHUB_RUN_ID",
		"GITHUB_SERVER_URL",
		"GITHUB_STEP_SUMMARY",
		"PATH",
	)
	for _, entry := range []string{
		"PATH=" + tmp + string(os.PathListSeparator) + os.Getenv("PATH"),
		"GITHUB_OUTPUT=" + outputPath,
		"GITHUB_REPOSITORY=",
		"GITHUB_RUN_ATTEMPT=",
		"GITHUB_RUN_ID=",
		"GITHUB_SERVER_URL=",
		"GITHUB_STEP_SUMMARY=",
		"DRYDOCK_ACTION_WORK_DIR=" + workDir,
		"DRYDOCK_BASE_COMPARE_REF=origin/main",
		"DRYDOCK_BIN=drydock",
		"DRYDOCK_CACHE_PATH=",
		"DRYDOCK_DIFF_ARTIFACT_NAME=diff",
		"DRYDOCK_DIFF_HTML_ARTIFACT_NAME=diff.html",
		"DRYDOCK_IMAGE_ARTIFACT_NAME=images",
		"DRYDOCK_INPUT_CHANGED_ONLY=",
		"DRYDOCK_INPUT_CHANGED_ONLY_INCLUDE=",
		"DRYDOCK_INPUT_CHANGED_ONLY_IGNORE=",
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
	} {
		key, _, _ := strings.Cut(entry, "=")
		env = setEnv(env, key, strings.TrimPrefix(entry, key+"="))
	}
	return env
}

func withoutEnvKeys(env []string, keys ...string) []string {
	filtered := env
	for _, key := range keys {
		filtered = withoutEnv(filtered, key)
	}
	return filtered
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
