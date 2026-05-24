package cli

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHomeOpsRenovateSmokeScriptContract(t *testing.T) {
	scriptPath := filepath.Join("..", "..", "scripts", "home-ops-renovate-smoke.sh")
	info, err := os.Stat(scriptPath)
	if err != nil {
		t.Fatalf("stat smoke script: %v", err)
	}
	if info.Mode()&0111 == 0 {
		t.Fatalf("smoke script mode = %v, want executable", info.Mode())
	}

	contentBytes, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read smoke script: %v", err)
	}
	content := string(contentBytes)

	for _, want := range []string{
		"#!/usr/bin/env bash",
		"set -euo pipefail",
		`HOME_OPS_ROOT="${HOME_OPS_ROOT:-/Users/ethan.shold/git/home-ops}"`,
		`if [[ -z "${RENOVATE_CHART_TO:-}" ]]; then`,
		"detect_renovate_chart_version()",
		"update_renovate_chart_version()",
		`/^[[:space:]]*helmCharts:[[:space:]]*($|#)/`,
		`CURRENT_CHART_VERSION="$(detect_renovate_chart_version "${KUSTOMIZATION}" "${RENOVATE_CHART_NAME}")"`,
		`if [[ "${CURRENT_CHART_VERSION}" == "${RENOVATE_CHART_TO}" ]]; then`,
		`update_renovate_chart_version "${KUSTOMIZATION}" "${RENOVATE_CHART_NAME}" "${RENOVATE_CHART_TO}" "${KUSTOMIZATION}.tmp"`,
		"mktemp -d",
		"git -C \"${ROOT}\" worktree add --detach \"${BASELINE}\" HEAD",
		"git -C \"${ROOT}\" worktree add --detach \"${CURRENT}\" HEAD",
		`KUSTOMIZATION="${CURRENT}/apps/renovate/kustomization.yaml"`,
		`go run ./cmd/drydock diff apps --path-orig "${BASELINE}" --path "${CURRENT}" --changed-only=true --exit-code=false`,
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("smoke script missing %q:\n%s", want, content)
		}
	}

	for _, notWant := range []string{
		`RENOVATE_CHART_FROM="${RENOVATE_CHART_FROM:-4.8.0}"`,
		`RENOVATE_CHART_TO="${RENOVATE_CHART_TO:-4.8.1}"`,
	} {
		if strings.Contains(content, notWant) {
			t.Fatalf("smoke script contains stale pinned default %q:\n%s", notWant, content)
		}
	}
}

func TestHomeOpsRenovateSmokeScriptUpdatesOnlyTopLevelChartVersion(t *testing.T) {
	scriptPath := filepath.Join("..", "..", "scripts", "home-ops-renovate-smoke.sh")
	input := filepath.Join(t.TempDir(), "kustomization.yaml")
	output := filepath.Join(t.TempDir(), "kustomization.out.yaml")
	if err := os.WriteFile(input, []byte(`apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
helmCharts:
  - name: unrelated
    valuesInline:
      helmCharts:
        - name: renovate-operator
          version: nested-chart-list
      initContainers:
        - name: renovate-operator
          version: nested-list
    version: 9.9.9
  - repo: oci://ghcr.io/mogenius/helm-charts
    version: 4.8.1
    valuesInline:
      initContainers:
        - name: renovate-operator
          version: nested-before-name
      version: nested
    name: renovate-operator
  - name: other
    version: 1.0.0
`), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	command := exec.Command("bash", "-c", `source "$1"; detect_renovate_chart_version "$2" renovate-operator; update_renovate_chart_version "$2" renovate-operator 4.8.0 "$3"`, "bash", scriptPath, input, output)
	command.Env = append(os.Environ(), "DRYDOCK_SMOKE_LIB_ONLY=true")
	combined, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("script helper command error = %v\n%s", err, string(combined))
	}
	if got := strings.TrimSpace(string(combined)); got != "4.8.1" {
		t.Fatalf("detected version = %q, want 4.8.1", got)
	}

	contentBytes, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("read updated fixture: %v", err)
	}
	content := string(contentBytes)
	for _, want := range []string{
		"          version: nested-chart-list",
		"          version: nested-list",
		"    version: 9.9.9",
		"          version: nested-before-name",
		"      version: nested",
		"    version: 4.8.0",
		"    version: 1.0.0",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("updated fixture missing %q:\n%s", want, content)
		}
	}
	if strings.Contains(content, "    version: 4.8.1") {
		t.Fatalf("updated fixture retained old chart version:\n%s", content)
	}
}

func TestHomeOpsRenovateSmokeScriptCleanupKeepsTempDirWhenWorktreeRemovalFails(t *testing.T) {
	scriptPath := filepath.Join("..", "..", "scripts", "home-ops-renovate-smoke.sh")
	fakeBin := t.TempDir()
	fakeGit := filepath.Join(fakeBin, "git")
	if err := os.WriteFile(fakeGit, []byte("#!/usr/bin/env bash\necho fake git failure >&2\nexit 1\n"), 0o700); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
	root := t.TempDir()
	tmpDir := filepath.Join(t.TempDir(), "smoke")
	for _, dir := range []string{tmpDir, filepath.Join(tmpDir, "baseline"), filepath.Join(tmpDir, "current")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
	}

	command := exec.Command("bash", "-c", `source "$1"; ROOT="$2"; TMP_DIR="$3"; BASELINE="$3/baseline"; CURRENT="$3/current"; cleanup`, "bash", scriptPath, root, tmpDir)
	command.Env = append(os.Environ(), "DRYDOCK_SMOKE_LIB_ONLY=true", "PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	combined, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("cleanup command error = nil, want failure\n%s", string(combined))
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
		t.Fatalf("cleanup command error = %v, want exit 2\n%s", err, string(combined))
	}
	output := string(combined)
	for _, want := range []string{
		"failed to remove baseline worktree",
		"failed to remove current worktree",
		"leaving temporary smoke directory for inspection",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("cleanup output missing %q:\n%s", want, output)
		}
	}
	if _, err := os.Stat(tmpDir); err != nil {
		t.Fatalf("stat temp dir after failed cleanup: %v", err)
	}
}
