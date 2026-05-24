package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHomeOpsPatternSmokeScriptContract(t *testing.T) {
	scriptPath := filepath.Join("..", "..", "scripts", "home-ops-pattern-smoke.sh")
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
		"replace_once_literal()",
		"update_helm_chart_version_once()",
		"run_diff()",
		"git -C \"${ROOT}\" worktree add --detach \"${BASELINE}\" HEAD",
		"git -C \"${ROOT}\" worktree add --detach \"${CURRENT}\" HEAD",
		"apps/adguard/manifests/deployment.yaml",
		"apps/monitoring/kromgo/manifests/config.yaml",
		"apps/renovate/kustomization.yaml",
		"apps/external-secrets/kustomization.yaml",
		"adguard/adguardhome:v0.107.76@sha256:7157eb1dc3b26c7af1d6898759a7b3f7d0fa09891fbd2d3caa6abc1057a9179b",
		`query: count(kube_node_info)`,
		"renovate-operator",
		"external-secrets",
		"system-upgrade remote resource",
		"apps/system-upgrade/plan.yaml",
		"--remote-cache-dir",
		"go run ./cmd/drydock diff apps --path-orig \"${BASELINE}\" --path \"${CURRENT}\"",
		"--remote-cache-dir \"${REMOTE_CACHE}\"",
		"--changed-only=true --exit-code=false",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("smoke script missing %q:\n%s", want, content)
		}
	}
}
