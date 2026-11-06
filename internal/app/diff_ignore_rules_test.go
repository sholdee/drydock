package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/diff"
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

const brokenCustomizationCM = `apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
  namespace: argocd
data:
  resource.customizations.ignoreDifferences.apps_StatefulSet: |
    jqPathExpressions:
      - .spec[
`

// A jq expression in any resource.customizations entry fails every
// Application, as Argo CD builds all override patches for each comparison.
func TestOrchestratorDiffAppsInvalidCustomizationJQRuleFailsApplications(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	for _, side := range []struct {
		root, value string
	}{{left, "a"}, {right, "b"}} {
		writeConfigMapApp(t, side.root, "alpha", side.value, "")
		writeConfigMapApp(t, side.root, "beta", side.value, "")
		writeTestFile(t, filepath.Join(side.root, "argocd-cm.yaml"), brokenCustomizationCM)
	}

	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{LeftPath: left, RightPath: right, Unified: 3})
	if err == nil {
		t.Fatal("DiffApps() error = nil, want the comparison of both Applications to fail")
	}
	failures := diagnosticsWithCode(result.Diagnostics, diffIgnoreRuleInvalidCode)
	if len(failures) != 2 {
		t.Fatalf("%s diagnostics = %#v, want one for each Application", diffIgnoreRuleInvalidCode, failures)
	}
	for i, name := range []string{"alpha", "beta"} {
		if !strings.Contains(failures[i].Message, "Application argocd/"+name+":") || !strings.Contains(failures[i].Message, "resource.customizations apps/StatefulSet") {
			t.Fatalf("%s diagnostic %d = %q, want Application argocd/%s and the customization", diffIgnoreRuleInvalidCode, i, failures[i].Message, name)
		}
	}
	if len(result.Results) != 0 {
		t.Fatalf("Results = %#v, want none", result.Results)
	}
}

// writeExtraConfigMap adds a second ConfigMap to Application <name>.
func writeExtraConfigMap(t *testing.T, root, name string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "manifests", name, "extra.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: extra
data:
  ignored: a
`)
}

// diffWithoutFailure runs DiffApps and requires no error and no
// diff.ignore-* diagnostic.
func diffWithoutFailure(t *testing.T, left, right string, strict bool) []diff.Result {
	t.Helper()
	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{LeftPath: left, RightPath: right, Unified: 3, Strict: strict})
	if err != nil {
		t.Fatalf("DiffApps() error = %v, diagnostics = %#v", err, result.Diagnostics)
	}
	for _, code := range []string{diffIgnoreRuleInvalidCode, diffIgnorePointerInvalidCode} {
		if found := diagnosticsWithCode(result.Diagnostics, code); len(found) != 0 {
			t.Fatalf("%s diagnostics = %#v, want none", code, found)
		}
	}
	return result.Results
}

func requireOnlyRemovedExtra(t *testing.T, results []diff.Result) {
	t.Helper()
	if len(results) != 1 || results[0].Resource.Name != "extra" || results[0].Change != diff.ChangeRemoved {
		t.Fatalf("Results = %#v, want only the removed ConfigMap extra", results)
	}
}

// Argo CD normalizes a live object that the new desired state no longer has
// with the Application's current rules (controller/state.go:931-932). So a
// pull request that fixes a broken jq rule and removes a resource that the
// rule matches diffs cleanly. The changed data.ignored stays hidden by the
// fixed rule.
func TestOrchestratorDiffAppsBaselineOnlyResourceUsesFixedApplicationRule(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeConfigMapApp(t, left, "demo", "a", "  ignoreDifferences:\n    - kind: ConfigMap\n      jqPathExpressions:\n        - .data[\n")
	writeExtraConfigMap(t, left, "demo")
	writeConfigMapApp(t, right, "demo", "b", "  ignoreDifferences:\n    - kind: ConfigMap\n      jqPathExpressions:\n        - .data.ignored\n")

	requireOnlyRemovedExtra(t, diffWithoutFailure(t, left, right, false))
}

// The same holds for a fixed resource.customizations jq expression and a
// removed resource of that kind.
func TestOrchestratorDiffAppsBaselineOnlyResourceUsesFixedCustomization(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	customization := func(expression string) string {
		return "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: argocd-cm\n  namespace: argocd\ndata:\n  resource.customizations.ignoreDifferences.ConfigMap: |\n    jqPathExpressions:\n      - " + expression + "\n"
	}
	writeConfigMapApp(t, left, "demo", "a", "")
	writeExtraConfigMap(t, left, "demo")
	writeTestFile(t, filepath.Join(left, "argocd-cm.yaml"), customization(".data["))
	writeConfigMapApp(t, right, "demo", "b", "")
	writeTestFile(t, filepath.Join(right, "argocd-cm.yaml"), customization(".data.ignored"))

	requireOnlyRemovedExtra(t, diffWithoutFailure(t, left, right, false))
}

// Argo CD never compares a deleted Application, so its broken rules cannot
// fail the diff. drydock applies no ignore rules to its removed resources.
func TestOrchestratorDiffAppsDeletedApplicationWithBrokenRules(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeConfigMapApp(t, left, "broken", "a", "  ignoreDifferences:\n    - kind: ConfigMap\n      jsonPointers:\n        - data/ignored\n      jqPathExpressions:\n        - .data[\n")
	writeConfigMapApp(t, left, "healthy", "a", "")
	writeConfigMapApp(t, right, "healthy", "a", "")

	results := diffWithoutFailure(t, left, right, true)
	if len(results) != 1 || results[0].Parent.Name != "broken" || results[0].Change != diff.ChangeRemoved || !strings.Contains(results[0].Diff, "-  ignored: a") {
		t.Fatalf("Results = %#v, want the removed ConfigMap of Application broken with no rule applied", results)
	}
}

// A pointer warning comes only from the current side's rules. A pull request
// that fixes an invalid pointer and removes a resource gives no warning, so
// --strict passes.
func TestOrchestratorDiffAppsFixedPointerGivesNoWarning(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeConfigMapApp(t, left, "demo", "a", "  ignoreDifferences:\n    - kind: ConfigMap\n      jsonPointers:\n        - data/ignored\n")
	writeExtraConfigMap(t, left, "demo")
	writeConfigMapApp(t, right, "demo", "b", "  ignoreDifferences:\n    - kind: ConfigMap\n      jsonPointers:\n        - /data/ignored\n")

	requireOnlyRemovedExtra(t, diffWithoutFailure(t, left, right, true))
}

// Argo CD builds the normalizer even for an Application with no resources
// (util/argo/diff/diff.go:298-325), so a jq rule that does not parse still
// fails its comparison.
func TestOrchestratorDiffAppsInvalidJQRuleFailsApplicationWithoutResources(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	for _, side := range []string{left, right} {
		writeConfigMapApp(t, side, "empty", "a", "  ignoreDifferences:\n    - kind: Service\n      jqPathExpressions:\n        - .spec[\n")
		if err := os.Remove(filepath.Join(side, "manifests", "empty", "configmap.yaml")); err != nil {
			t.Fatalf("Remove() error = %v", err)
		}
		writeTestFile(t, filepath.Join(side, "manifests", "empty", "README.md"), "no manifests\n")
	}

	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{LeftPath: left, RightPath: right, Unified: 3})
	failures := diagnosticsWithCode(result.Diagnostics, diffIgnoreRuleInvalidCode)
	if err == nil || len(failures) != 1 || !strings.Contains(failures[0].Message, "Application argocd/empty:") {
		t.Fatalf("DiffApps() error = %v, %s diagnostics = %#v, want one for Application argocd/empty", err, diffIgnoreRuleInvalidCode, failures)
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
