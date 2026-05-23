package cli

import (
	"os"
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
		`RENOVATE_CHART_FROM="${RENOVATE_CHART_FROM:-4.8.0}"`,
		`RENOVATE_CHART_TO="${RENOVATE_CHART_TO:-4.8.1}"`,
		"mktemp -d",
		"git -C \"${ROOT}\" worktree add --detach \"${BASELINE}\" HEAD",
		"git -C \"${ROOT}\" worktree add --detach \"${CURRENT}\" HEAD",
		`KUSTOMIZATION="${CURRENT}/apps/renovate/kustomization.yaml"`,
		`go run ./cmd/argocd-local diff apps --path-orig "${BASELINE}" --path "${CURRENT}" --changed-only=true --exit-code=false`,
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("smoke script missing %q:\n%s", want, content)
		}
	}
}
