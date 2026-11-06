package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/diagnostic"
)

// writeConfigMapApp writes Application <name> with one ConfigMap whose
// data.value is value. ignoreDifferences is appended to the Application spec.
func writeConfigMapApp(t *testing.T, root, name, value, ignoreDifferences string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", name+".yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: `+name+`
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    path: manifests/`+name+`
    targetRevision: main
  destination:
    name: in-cluster
    namespace: `+name+`
`+ignoreDifferences)
	writeTestFile(t, filepath.Join(root, "manifests", name, "configmap.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: `+name+`
data:
  ignored: `+value+`
  kept: same
`)
}

// Argo CD v3.6.1 builds every jq ignore patch before it matches a resource
// (util/argo/normalizers/diff_normalizer.go:159-168), so a jq expression that
// does not parse fails the comparison of its Application even when no resource
// matches the rule. Only that Application fails.
func TestOrchestratorDiffAppsInvalidJQRuleFailsOnlyItsApplication(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	broken := `  ignoreDifferences:
    - kind: Service
      jqPathExpressions:
        - .spec.ports[
`
	for _, side := range []struct {
		root, value string
	}{{left, "a"}, {right, "b"}} {
		writeConfigMapApp(t, side.root, "broken", side.value, broken)
		writeConfigMapApp(t, side.root, "healthy", side.value, "")
	}

	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{LeftPath: left, RightPath: right, Unified: 3})
	if err == nil || !strings.Contains(err.Error(), "Application argocd/broken") {
		t.Fatalf("DiffApps() error = %v, want the failure of Application argocd/broken", err)
	}
	failures := diagnosticsWithCode(result.Diagnostics, diffIgnoreRuleInvalidCode)
	if len(failures) != 1 || failures[0].Severity != diagnostic.SeverityError || !strings.Contains(failures[0].Message, "Application argocd/broken") {
		t.Fatalf("%s diagnostics = %#v, want one error for Application argocd/broken", diffIgnoreRuleInvalidCode, failures)
	}
	if len(result.Results) != 1 || result.Results[0].Parent.Name != "healthy" {
		t.Fatalf("Results = %#v, want only the change of Application argocd/healthy", result.Results)
	}
}

// A jq expression in any resource.customizations entry fails every
// Application, as Argo CD builds all override patches for each comparison.
func TestOrchestratorDiffAppsInvalidCustomizationJQRuleFailsApplications(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	for _, side := range []struct {
		root, value string
	}{{left, "a"}, {right, "b"}} {
		writeConfigMapApp(t, side.root, "demo", side.value, "")
		writeTestFile(t, filepath.Join(side.root, "argocd-cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
  namespace: argocd
data:
  resource.customizations.ignoreDifferences.apps_StatefulSet: |
    jqPathExpressions:
      - .spec[
`)
	}

	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{LeftPath: left, RightPath: right, Unified: 3})
	if err == nil {
		t.Fatal("DiffApps() error = nil, want the comparison of Application argocd/demo to fail")
	}
	failures := diagnosticsWithCode(result.Diagnostics, diffIgnoreRuleInvalidCode)
	if len(failures) != 1 || !strings.Contains(failures[0].Message, "resource.customizations apps/StatefulSet") {
		t.Fatalf("%s diagnostics = %#v, want one naming the customization", diffIgnoreRuleInvalidCode, failures)
	}
}

// Argo CD applies a jsonPointer that is not valid RFC 6901 as a JSON-patch
// path and skips it where the patch fails. drydock does the same, keeps the
// other rules, and warns. --strict fails on the warning.
func TestOrchestratorDiffAppsInvalidIgnorePointerWarns(t *testing.T) {
	for _, strict := range []bool{false, true} {
		t.Run(fmt.Sprintf("strict=%t", strict), func(t *testing.T) {
			root := t.TempDir()
			left := filepath.Join(root, "left")
			right := filepath.Join(root, "right")
			rules := `  ignoreDifferences:
    - kind: ConfigMap
      jsonPointers:
        - data/ignored
        - /data/ignored
`
			writeConfigMapApp(t, left, "demo", "a", rules)
			writeConfigMapApp(t, right, "demo", "b", rules)

			result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{LeftPath: left, RightPath: right, Unified: 3, Strict: strict})
			if strict != (err != nil) {
				t.Fatalf("DiffApps() error = %v, want an error only with strict", err)
			}
			warnings := diagnosticsWithCode(result.Diagnostics, diffIgnorePointerInvalidCode)
			wantSeverity := diagnostic.SeverityWarning
			if strict {
				wantSeverity = diagnostic.SeverityError
			}
			if len(warnings) != 1 || warnings[0].Severity != wantSeverity ||
				!strings.Contains(warnings[0].Message, "Application argocd/demo") ||
				!strings.Contains(warnings[0].Message, `"data/ignored"`) ||
				!strings.Contains(warnings[0].Message, "must be empty or start with /") {
				t.Fatalf("%s diagnostics = %#v, want one %s naming the Application, the pointer and the reason", diffIgnorePointerInvalidCode, warnings, wantSeverity)
			}
			if len(result.Results) != 0 {
				t.Fatalf("Results = %#v, want the valid pointer still applied", result.Results)
			}
		})
	}
}

// Argo CD compares the new desired state under the Application's rules after
// the change. A rule that only the baseline Application has does not hide a
// change, and a rule that only the current Application has does.
func TestOrchestratorDiffAppsUsesCurrentSideApplicationRules(t *testing.T) {
	rule := `  ignoreDifferences:
    - kind: ConfigMap
      jsonPointers:
        - /data/ignored
`
	for _, tt := range []struct {
		name                string
		leftRule, rightRule string
		wantResults         int
	}{
		{name: "rule removed", leftRule: rule, wantResults: 1},
		{name: "rule added", rightRule: rule, wantResults: 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			left := filepath.Join(root, "left")
			right := filepath.Join(root, "right")
			writeConfigMapApp(t, left, "demo", "a", tt.leftRule)
			writeConfigMapApp(t, right, "demo", "b", tt.rightRule)

			result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{LeftPath: left, RightPath: right, Unified: 3})
			if err != nil {
				t.Fatalf("DiffApps() error = %v", err)
			}
			if len(result.Results) != tt.wantResults {
				t.Fatalf("Results = %#v, want %d", result.Results, tt.wantResults)
			}
		})
	}
}
