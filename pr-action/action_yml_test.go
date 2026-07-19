package praction

import (
	"os"
	"strings"
	"testing"
)

// loadActionYAML reads action.yml once and returns it as a string.
func loadActionYAML(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("action.yml")
	if err != nil {
		t.Fatalf("read action.yml: %v", err)
	}
	return string(b)
}

func TestActionUploadsHTMLDiffBeforeCommentAndAppendsURL(t *testing.T) {
	actionYAML := loadActionYAML(t)

	rawUpload := strings.Index(actionYAML, "- name: Upload rendered manifest diff\n")
	htmlUpload := strings.Index(actionYAML, "- name: Upload rendered manifest diff HTML\n")
	htmlLink := strings.Index(actionYAML, "- name: Add rendered manifest diff HTML link\n")
	comment := strings.Index(actionYAML, "- name: Comment rendered manifest diff\n")
	for name, index := range map[string]int{
		"raw diff upload":  rawUpload,
		"HTML diff upload": htmlUpload,
		"HTML link":        htmlLink,
		"diff comment":     comment,
	} {
		if index == -1 {
			t.Fatalf("action.yml missing %s step", name)
		}
	}
	if rawUpload >= htmlUpload || htmlUpload >= htmlLink || htmlLink >= comment {
		t.Fatalf("action.yml step order raw upload=%d html upload=%d html link=%d comment=%d", rawUpload, htmlUpload, htmlLink, comment)
	}

	htmlUploadBlock := actionYAML[htmlUpload:htmlLink]
	for _, want := range []string{
		"id: upload-diff-html",
		"name: ${{ steps.run.outputs.diff-html-artifact-name }}",
		"path: ${{ steps.run.outputs.diff-html-path }}",
		"archive: false",
	} {
		if !strings.Contains(htmlUploadBlock, want) {
			t.Fatalf("HTML upload block missing %q:\n%s", want, htmlUploadBlock)
		}
	}

	linkBlock := actionYAML[htmlLink:comment]
	for _, want := range []string{
		"DRYDOCK_DIFF_COMMENT_PATH: ${{ steps.run.outputs.diff-comment-path }}",
		"DRYDOCK_DIFF_HTML_ARTIFACT_URL: ${{ steps.upload-diff-html.outputs.artifact-url }}",
		"[Full Rendered Diff View](${DRYDOCK_DIFF_HTML_ARTIFACT_URL})",
		"artifact-url",
	} {
		if !strings.Contains(linkBlock, want) {
			t.Fatalf("HTML link block missing %q:\n%s", want, linkBlock)
		}
	}
	if strings.Contains(linkBlock, "[${DRYDOCK_DIFF_HTML_ARTIFACT_NAME}]") {
		t.Fatalf("HTML link block should not use artifact filename as link text:\n%s", linkBlock)
	}
	if strings.Contains(linkBlock, "[Full Rendered Diff View](${DRYDOCK_DIFF_HTML_ARTIFACT_URL}).") {
		t.Fatalf("HTML link block should not append punctuation to link:\n%s", linkBlock)
	}
	if strings.Contains(linkBlock, "Full Diff Report") {
		t.Fatalf("HTML link block should use Full Rendered Diff View label:\n%s", linkBlock)
	}
}

func TestActionCachePruneMaxSizeInputDeclaredWithDefault(t *testing.T) {
	actionYAML := loadActionYAML(t)

	// New input must be declared.
	if !strings.Contains(actionYAML, "cache-prune-max-size:") {
		t.Fatalf("action.yml missing cache-prune-max-size input declaration")
	}
	// Default must be 4Gi.
	inputIdx := strings.Index(actionYAML, "cache-prune-max-size:")
	// Find the default: line within the next 800 chars.
	snippet := actionYAML[inputIdx:min(inputIdx+800, len(actionYAML))]
	if !strings.Contains(snippet, `default: "4Gi"`) {
		t.Fatalf("cache-prune-max-size input block = %q, want default: \"4Gi\"", snippet)
	}
}

func TestActionPrepareStepEnvContainsNewVars(t *testing.T) {
	actionYAML := loadActionYAML(t)

	// Locate the prepare step.
	prepareIdx := strings.Index(actionYAML, "- name: Prepare drydock action\n")
	if prepareIdx == -1 {
		t.Fatalf("action.yml missing 'Prepare drydock action' step")
	}
	// Find the boundary of the next step to scope the search.
	nextStep := strings.Index(actionYAML[prepareIdx+1:], "\n    - name: ")
	var prepareBlock string
	if nextStep == -1 {
		prepareBlock = actionYAML[prepareIdx:]
	} else {
		prepareBlock = actionYAML[prepareIdx : prepareIdx+1+nextStep]
	}

	for _, want := range []string{
		"DRYDOCK_INPUT_OFFLINE: ${{ inputs.offline }}",
		"DRYDOCK_INPUT_CACHE_PRUNE_MAX_SIZE: ${{ inputs.cache-prune-max-size }}",
	} {
		if !strings.Contains(prepareBlock, want) {
			t.Fatalf("prepare step env block missing %q:\n%s", want, prepareBlock)
		}
	}
}

func TestActionPruneStepExistsAfterSaveStep(t *testing.T) {
	actionYAML := loadActionYAML(t)

	saveIdx := strings.Index(actionYAML, "- name: Save drydock render cache\n")
	pruneIdx := strings.Index(actionYAML, "- name: Prune drydock local cache\n")
	uploadIdx := strings.Index(actionYAML, "- name: Upload rendered manifest diff\n")

	if saveIdx == -1 {
		t.Fatalf("action.yml missing 'Save drydock render cache' step")
	}
	if pruneIdx == -1 {
		t.Fatalf("action.yml missing 'Prune drydock local cache' step")
	}
	if uploadIdx == -1 {
		t.Fatalf("action.yml missing 'Upload rendered manifest diff' step")
	}
	if saveIdx >= pruneIdx {
		t.Fatalf("prune step (pos %d) must come after save step (pos %d)", pruneIdx, saveIdx)
	}
	if pruneIdx >= uploadIdx {
		t.Fatalf("prune step (pos %d) must come before upload step (pos %d)", pruneIdx, uploadIdx)
	}
}

func TestActionPruneStepIfGuardAndEnvWiring(t *testing.T) {
	actionYAML := loadActionYAML(t)

	pruneIdx := strings.Index(actionYAML, "- name: Prune drydock local cache\n")
	if pruneIdx == -1 {
		t.Fatalf("action.yml missing 'Prune drydock local cache' step")
	}
	// Scope to the prune step block (until the next step).
	nextStep := strings.Index(actionYAML[pruneIdx+1:], "\n    - name: ")
	var pruneBlock string
	if nextStep == -1 {
		pruneBlock = actionYAML[pruneIdx:]
	} else {
		pruneBlock = actionYAML[pruneIdx : pruneIdx+1+nextStep]
	}

	// if: guard must reference cache-prune output from prepare.
	if !strings.Contains(pruneBlock, "steps.prepare.outputs.cache-prune == 'true'") {
		t.Fatalf("prune step missing cache-prune output guard:\n%s", pruneBlock)
	}
	if !strings.Contains(pruneBlock, "always()") {
		t.Fatalf("prune step if: must include always():\n%s", pruneBlock)
	}

	// Env: DRYDOCK_INPUT_CACHE_PRUNE_MAX_SIZE must come from prepare OUTPUT
	// (not the raw input), to keep prepare the single source of truth.
	if !strings.Contains(pruneBlock, "DRYDOCK_INPUT_CACHE_PRUNE_MAX_SIZE: ${{ steps.prepare.outputs.cache-prune-max-size }}") {
		t.Fatalf("prune step must wire DRYDOCK_INPUT_CACHE_PRUNE_MAX_SIZE from prepare output:\n%s", pruneBlock)
	}
	// DRYDOCK_BIN must use the same format expression as the Run step.
	if !strings.Contains(pruneBlock, "format('{0}/drydock', steps.setup.outputs.install-dir)") {
		t.Fatalf("prune step DRYDOCK_BIN missing install-dir format expression:\n%s", pruneBlock)
	}
	if !strings.Contains(pruneBlock, "DRYDOCK_CACHE_PATH: ${{ steps.prepare.outputs.cache-path }}") {
		t.Fatalf("prune step missing DRYDOCK_CACHE_PATH wiring:\n%s", pruneBlock)
	}
}

func TestActionKSOPSCompatInputDeclaredAndWiredToRunStep(t *testing.T) {
	actionYAML := loadActionYAML(t)

	// Input must be declared.
	if !strings.Contains(actionYAML, "enable-ksops-compat:") {
		t.Fatalf("action.yml missing enable-ksops-compat input declaration")
	}
	// Default must be "false".
	inputIdx := strings.Index(actionYAML, "enable-ksops-compat:")
	snippet := actionYAML[inputIdx:min(inputIdx+300, len(actionYAML))]
	if !strings.Contains(snippet, `default: "false"`) {
		t.Fatalf("enable-ksops-compat input block = %q, want default: \"false\"", snippet)
	}

	// Run step must wire the env var.
	runIdx := strings.Index(actionYAML, "- name: Run drydock\n")
	if runIdx == -1 {
		t.Fatalf("action.yml missing 'Run drydock' step")
	}
	nextStep := strings.Index(actionYAML[runIdx+1:], "\n    - name: ")
	var runBlock string
	if nextStep == -1 {
		runBlock = actionYAML[runIdx:]
	} else {
		runBlock = actionYAML[runIdx : runIdx+1+nextStep]
	}
	if !strings.Contains(runBlock, "DRYDOCK_INPUT_ENABLE_KSOPS_COMPAT: ${{ inputs.enable-ksops-compat }}") {
		t.Fatalf("Run step env block missing DRYDOCK_INPUT_ENABLE_KSOPS_COMPAT wiring:\n%s", runBlock)
	}
}

func TestActionDiscoverIgnoreInputDeclaredAndWiredToRunStep(t *testing.T) {
	actionYAML := loadActionYAML(t)

	// Input must be declared.
	if !strings.Contains(actionYAML, "discover-ignore:") {
		t.Fatalf("action.yml missing discover-ignore input declaration")
	}

	// Run step must wire the env var.
	runIdx := strings.Index(actionYAML, "- name: Run drydock\n")
	if runIdx == -1 {
		t.Fatalf("action.yml missing 'Run drydock' step")
	}
	nextStep := strings.Index(actionYAML[runIdx+1:], "\n    - name: ")
	var runBlock string
	if nextStep == -1 {
		runBlock = actionYAML[runIdx:]
	} else {
		runBlock = actionYAML[runIdx : runIdx+1+nextStep]
	}
	if !strings.Contains(runBlock, "DRYDOCK_INPUT_DISCOVER_IGNORE: ${{ inputs.discover-ignore }}") {
		t.Fatalf("Run step env block missing DRYDOCK_INPUT_DISCOVER_IGNORE wiring:\n%s", runBlock)
	}
}

// The Run step needs the base ref even when fetch-base is skipped
// (render-test-only configs): run.sh writes the origin/HEAD symref from these
// env vars so drydock's self-repo resolution works in ALL modes.
func TestActionRunStepWiresBaseRefEnvForSymrefWrite(t *testing.T) {
	actionYAML := loadActionYAML(t)

	runIdx := strings.Index(actionYAML, "- name: Run drydock\n")
	if runIdx == -1 {
		t.Fatalf("action.yml missing 'Run drydock' step")
	}
	nextStep := strings.Index(actionYAML[runIdx+1:], "\n    - name: ")
	var runBlock string
	if nextStep == -1 {
		runBlock = actionYAML[runIdx:]
	} else {
		runBlock = actionYAML[runIdx : runIdx+1+nextStep]
	}
	for _, want := range []string{
		"DRYDOCK_INPUT_BASE_REF: ${{ inputs.base-ref }}",
		"DRYDOCK_PR_BASE_REF: ${{ github.event.pull_request.base.ref }}",
	} {
		if !strings.Contains(runBlock, want) {
			t.Fatalf("Run step env block missing %q:\n%s", want, runBlock)
		}
	}
}

func TestActionNoPinnedActionRefsInPruneStep(t *testing.T) {
	actionYAML := loadActionYAML(t)

	pruneIdx := strings.Index(actionYAML, "- name: Prune drydock local cache\n")
	if pruneIdx == -1 {
		t.Fatalf("action.yml missing 'Prune drydock local cache' step")
	}
	nextStep := strings.Index(actionYAML[pruneIdx+1:], "\n    - name: ")
	var pruneBlock string
	if nextStep == -1 {
		pruneBlock = actionYAML[pruneIdx:]
	} else {
		pruneBlock = actionYAML[pruneIdx : pruneIdx+1+nextStep]
	}

	// The prune step must not introduce any new external action references.
	if strings.Contains(pruneBlock, "\n      uses:") {
		t.Fatalf("prune step must not use any external actions (nothing to SHA-pin):\n%s", pruneBlock)
	}
}
