package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/config"
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

// The Argo CD controller reads its jq timeout from argocd-cmd-params-cm
// controller.ignore.normalizer.jq.timeout. A jq rule that runs longer is
// skipped, so the change it would hide shows. With the default of 1 second,
// the same rule finishes and hides the change.
func TestOrchestratorDiffAppsUsesCommandParametersJQTimeout(t *testing.T) {
	// The range makes the expression run for some milliseconds, far below
	// the default and far above 1ns.
	rule := "  ignoreDifferences:\n    - kind: ConfigMap\n      jqPathExpressions:\n        - .data.ignored, .data[range(2000) | tostring]\n"
	for _, tt := range []struct {
		name        string
		timeout     string
		wantResults int
	}{
		{name: "default", wantResults: 0},
		{name: "timeout from argocd-cmd-params-cm", timeout: "1ns", wantResults: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			left := filepath.Join(root, "left")
			right := filepath.Join(root, "right")
			for _, side := range []struct {
				root, value string
			}{{left, "a"}, {right, "b"}} {
				writeConfigMapApp(t, side.root, "demo", side.value, rule)
				if tt.timeout != "" {
					writeTestFile(t, filepath.Join(side.root, "argocd-cmd-params-cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: argocd-cmd-params-cm\n  namespace: argocd\ndata:\n  controller.ignore.normalizer.jq.timeout: "+tt.timeout+"\n")
				}
			}

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

// The jq timeout changes diffs, not renders. So the changed-only settings
// signature sees the parsed timeout, and the render signature, which keys
// the render cache, does not see it at all.
func TestSettingsSignaturesSignJQTimeoutForChangedOnlyOnly(t *testing.T) {
	signatures := func(value string) (string, string) {
		t.Helper()
		settings := config.DefaultSettings()
		if value != "" {
			settings.IgnoreNormalizerJQTimeout = config.Value[string]{Value: value}
		}
		renderSig, err := renderSettingsSignature(settings)
		if err != nil {
			t.Fatalf("renderSettingsSignature() error = %v", err)
		}
		changedOnlySig, err := changedOnlySettingsSignature(settings)
		if err != nil {
			t.Fatalf("changedOnlySettingsSignature() error = %v", err)
		}
		return renderSig, changedOnlySig
	}
	defaultRender, defaultChangedOnly := signatures("")
	slowRender, slowChangedOnly := signatures("5s")
	if slowRender != defaultRender {
		t.Fatal("render signature changed with the jq timeout, want it to stay out of render cache keys")
	}
	if slowChangedOnly == defaultChangedOnly {
		t.Fatal("changed-only settings signature ignores the jq timeout")
	}
	for _, pair := range [][2]string{{"60s", "1m"}, {"", "1s"}, {"abc", "0"}} {
		_, left := signatures(pair[0])
		_, right := signatures(pair[1])
		if left != right {
			t.Fatalf("changed-only signatures of %q and %q differ, want the same parsed timeout to match", pair[0], pair[1])
		}
	}
}

// drydock parses jq ignore rules with the gojq version that Argo CD v3.6.1
// uses (v0.12.19). That version cannot parse `?//`, the alternative
// destructuring operator, so Argo CD fails the comparison of this
// Application. A newer gojq parses it and would hide the change.
func TestOrchestratorDiffAppsJQRuleThatArgoCDGojqCannotParse(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	rule := "  ignoreDifferences:\n    - kind: ConfigMap\n      jqPathExpressions:\n        - .data.ignored?//empty\n"
	writeConfigMapApp(t, left, "demo", "a", rule)
	writeConfigMapApp(t, right, "demo", "b", rule)

	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{LeftPath: left, RightPath: right, Unified: 3})
	failures := diagnosticsWithCode(result.Diagnostics, diffIgnoreRuleInvalidCode)
	if err == nil || len(failures) != 1 || !strings.Contains(failures[0].Message, "Application argocd/demo:") {
		t.Fatalf("DiffApps() error = %v, %s diagnostics = %#v, want one for Application argocd/demo", err, diffIgnoreRuleInvalidCode, failures)
	}
}

// A jq rule that removes the whole object makes Argo CD's client-side diff
// fail, which fails the comparison of the Application. Server-side diff
// compares null with null, so the change is hidden. The other Applications
// diff as usual in both modes. TestUpstreamIgnoreRuleThatRemovesTheObject
// in internal/diff pins the upstream behavior.
func TestOrchestratorDiffAppsIgnoreRuleThatRemovesTheObject(t *testing.T) {
	removeAll := "  ignoreDifferences:\n    - kind: ConfigMap\n      jqPathExpressions:\n        - .\n"
	serverSide := removeAll + "  syncPolicy:\n    syncOptions:\n      - ServerSideApply=true\n"
	for _, tt := range []struct {
		name        string
		rules       string
		wantFailure bool
	}{
		{name: "client-side diff", rules: removeAll, wantFailure: true},
		{name: "server-side diff", rules: serverSide},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			left := filepath.Join(root, "left")
			right := filepath.Join(root, "right")
			for _, side := range []struct {
				root, value string
			}{{left, "a"}, {right, "b"}} {
				writeConfigMapApp(t, side.root, "broken", side.value, tt.rules)
				writeConfigMapApp(t, side.root, "healthy", side.value, "")
			}

			result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{LeftPath: left, RightPath: right, Unified: 3})
			failures := diagnosticsWithCode(result.Diagnostics, diffComparisonFailedCode)
			if tt.wantFailure {
				if err == nil || len(failures) != 1 || !strings.Contains(failures[0].Message, "Application argocd/broken:") {
					t.Fatalf("DiffApps() error = %v, %s diagnostics = %#v, want one for Application argocd/broken", err, diffComparisonFailedCode, failures)
				}
			} else if err != nil || len(failures) != 0 {
				t.Fatalf("DiffApps() error = %v, %s diagnostics = %#v, want none", err, diffComparisonFailedCode, failures)
			}
			if len(result.Results) != 1 || result.Results[0].Parent.Name != "healthy" {
				t.Fatalf("Results = %#v, want only the change of Application healthy", result.Results)
			}
		})
	}
}

// `diff app` compares only the named Application, so a broken jq rule of
// another Application does not fail it.
func TestOrchestratorDiffAppIgnoresBrokenRulesOfOtherApplications(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	broken := "  ignoreDifferences:\n    - kind: ConfigMap\n      jqPathExpressions:\n        - .data[\n"
	for _, side := range []struct {
		root, value string
	}{{left, "a"}, {right, "b"}} {
		writeConfigMapApp(t, side.root, "broken", side.value, broken)
		writeConfigMapApp(t, side.root, "healthy", side.value, "")
	}

	request := DiffAppRequest{Name: "healthy"}
	request.LeftPath, request.RightPath, request.Unified = left, right, 3
	result, err := Orchestrator{}.DiffApp(context.Background(), request)
	if err != nil {
		t.Fatalf("DiffApp() error = %v, diagnostics = %#v", err, result.Diagnostics)
	}
	if len(result.Results) != 1 || result.Results[0].Parent.Name != "healthy" {
		t.Fatalf("Results = %#v, want the change of Application healthy", result.Results)
	}
}

// Compare options and knownTypeFields come from the current side's
// settings only, like the ignore rules.
func TestOrchestratorDiffAppsUsesCurrentSideCompareOptionsAndKnownTypes(t *testing.T) {
	writeApp := func(t *testing.T, root, verbs, cpu string) {
		t.Helper()
		writeTestFile(t, filepath.Join(root, "apps", "demo.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    path: manifests/demo
    targetRevision: main
  destination:
    name: in-cluster
    namespace: demo
`)
		writeTestFile(t, filepath.Join(root, "manifests", "demo", "clusterrole.yaml"), `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: aggregated
aggregationRule:
  clusterRoleSelectors:
    - matchLabels:
        example.com/aggregate: "true"
rules:
  - apiGroups: [""]
    resources: [configmaps]
    verbs: `+verbs+`
`)
		writeTestFile(t, filepath.Join(root, "manifests", "demo", "widget.yaml"), `apiVersion: example.com/v1
kind: Widget
metadata:
  name: widget
spec:
  resources:
    limits:
      cpu: `+cpu+`
`)
	}
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeApp(t, left, "[get]", "0.1")
	writeTestFile(t, filepath.Join(left, "argocd-cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
  namespace: argocd
data:
  resource.compareoptions: |
    ignoreAggregatedRoles: true
  resource.customizations.knownTypeFields.example.com_Widget: |
    - field: spec.resources
      type: core/v1/ResourceRequirements
`)
	writeApp(t, right, "[get, list]", "100m")

	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{LeftPath: left, RightPath: right, Unified: 3})
	if err != nil {
		t.Fatalf("DiffApps() error = %v, diagnostics = %#v", err, result.Diagnostics)
	}
	byName := resultsByResourceName(result.Results)
	for _, name := range []string{"ClusterRole/aggregated", "Widget/widget"} {
		if _, ok := byName[name]; !ok {
			t.Fatalf("Results = %#v, want a change of %s: baseline-only settings must not hide it", result.Results, name)
		}
	}
}

// Changed-only selection picks the Application whose rule and resource
// change, and normalizes it with the new rule.
func TestOrchestratorDiffAppsChangedOnlyUsesCurrentSideRules(t *testing.T) {
	rule := "  ignoreDifferences:\n    - kind: ConfigMap\n      jsonPointers:\n        - /data/ignored\n"
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
			writeConfigMapApp(t, left, "other", "a", "")
			writeConfigMapApp(t, right, "other", "a", "")

			result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
				LeftPath:          left,
				RightPath:         right,
				ChangedOnly:       true,
				StrictChangedOnly: true,
				Unified:           3,
			})
			if err != nil {
				t.Fatalf("DiffApps() error = %v, diagnostics = %#v", err, result.Diagnostics)
			}
			if len(result.Results) != tt.wantResults {
				t.Fatalf("Results = %#v, want %d", result.Results, tt.wantResults)
			}
			for _, res := range result.Results {
				if res.Parent.Name != "demo" {
					t.Fatalf("Results = %#v, want only Application demo", result.Results)
				}
			}
		})
	}
}

// applicationServerSideDiff follows shouldUseServerSideDiff
// (controller/state.go:1184-1191) without the controller-level switch.
func TestApplicationServerSideDiff(t *testing.T) {
	for _, tt := range []struct {
		name        string
		annotation  string
		syncOptions []string
		want        bool
	}{
		{name: "default"},
		{name: "ServerSideDiff=true annotation alone", annotation: "ServerSideDiff=true", want: true},
		{name: "ServerSideApply=true sync option alone", syncOptions: []string{"ServerSideApply=true"}, want: true},
		{name: "ServerSideDiff=false wins over ServerSideApply=true", annotation: "ServerSideDiff=false", syncOptions: []string{"ServerSideApply=true"}},
		{name: "comma-separated annotation", annotation: "IgnoreExtraneous,ServerSideDiff=true", want: true},
		{name: "comma-separated annotation with spaces", annotation: "IgnoreExtraneous, ServerSideDiff=true", want: true},
		{name: "comma-separated false wins", annotation: "ServerSideDiff=true,ServerSideDiff=false", syncOptions: []string{"ServerSideApply=true"}},
		{name: "other options only", annotation: "IgnoreExtraneous", syncOptions: []string{"CreateNamespace=true"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			application := argoappv1.Application{}
			if tt.annotation != "" {
				application.Annotations = map[string]string{"argocd.argoproj.io/compare-options": tt.annotation}
			}
			if tt.syncOptions != nil {
				application.Spec.SyncPolicy = &argoappv1.SyncPolicy{SyncOptions: tt.syncOptions}
			}
			if got := applicationServerSideDiff(application); got != tt.want {
				t.Fatalf("applicationServerSideDiff() = %t, want %t", got, tt.want)
			}
		})
	}
}

// A knownTypeFields field that fails to normalize on a changed resource gives
// the strict-exempt advisory diff.known-type-skipped, which names the
// Application, the resource and the field. Argo CD only logs the error.
func TestOrchestratorDiffAppsKnownTypeSkippedAdvisory(t *testing.T) {
	writeApp := func(t *testing.T, root, replicas string) {
		t.Helper()
		writeTestFile(t, filepath.Join(root, "apps", "demo.yaml"), "apiVersion: argoproj.io/v1alpha1\nkind: Application\nmetadata:\n  name: demo\n  namespace: argocd\nspec:\n  source:\n    repoURL: https://github.com/example/repo\n    path: manifests/demo\n    targetRevision: main\n  destination:\n    name: in-cluster\n    namespace: demo\n")
		writeTestFile(t, filepath.Join(root, "manifests", "demo", "widget.yaml"), "apiVersion: example.com/v1\nkind: Widget\nmetadata:\n  name: widget\nspec:\n  replicas: "+replicas+"\n  resources:\n    limits: notamap\n")
		writeTestFile(t, filepath.Join(root, "argocd-cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: argocd-cm\n  namespace: argocd\ndata:\n  resource.customizations.knownTypeFields.example.com_Widget: |\n    - field: spec.resources\n      type: core/v1/ResourceRequirements\n")
	}
	for _, strict := range []bool{false, true} {
		t.Run(fmt.Sprintf("strict=%t", strict), func(t *testing.T) {
			root := t.TempDir()
			left := filepath.Join(root, "left")
			right := filepath.Join(root, "right")
			writeApp(t, left, "1")
			writeApp(t, right, "2")

			result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{LeftPath: left, RightPath: right, Unified: 3, Strict: strict})
			if err != nil {
				t.Fatalf("DiffApps() error = %v, want the advisory to pass --strict", err)
			}
			advisories := diagnosticsWithCode(result.Diagnostics, diffKnownTypeSkippedCode)
			if len(advisories) != 1 || advisories[0].Severity != diagnostic.SeverityWarning {
				t.Fatalf("%s diagnostics = %#v, want one warning", diffKnownTypeSkippedCode, advisories)
			}
			for _, want := range []string{"Application argocd/demo:", "example.com/Widget demo/widget", `"spec.resources"`} {
				if !strings.Contains(advisories[0].Message, want) {
					t.Fatalf("advisory = %q, want %q", advisories[0].Message, want)
				}
			}
			if len(result.Results) != 1 {
				t.Fatalf("Results = %#v, want the Widget change", result.Results)
			}
		})
	}
}

// writeRolloutApp writes Application <name> with an Argo Rollout whose pod
// template the built-in Rollout known type fails to normalize.
func writeRolloutApp(t *testing.T, root, name, replicas string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", name+".yaml"), "apiVersion: argoproj.io/v1alpha1\nkind: Application\nmetadata:\n  name: "+name+"\n  namespace: argocd\nspec:\n  source:\n    repoURL: https://github.com/example/repo\n    path: manifests/"+name+"\n    targetRevision: main\n  destination:\n    name: in-cluster\n    namespace: "+name+"\n")
	writeTestFile(t, filepath.Join(root, "manifests", name, "rollout.yaml"), "apiVersion: argoproj.io/v1alpha1\nkind: Rollout\nmetadata:\n  name: "+name+"\nspec:\n  replicas: "+replicas+"\n  template:\n    spec:\n      containers: notalist\n")
}

// The advisory names the built-in Rollout known type, and an Application
// that only the baseline side has gives none: Argo CD never compares it.
func TestOrchestratorDiffAppsKnownTypeSkippedAdvisoryScope(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeRolloutApp(t, left, "kept", "1")
	writeRolloutApp(t, right, "kept", "2")
	writeRolloutApp(t, left, "gone", "1")

	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{LeftPath: left, RightPath: right, Unified: 3})
	if err != nil {
		t.Fatalf("DiffApps() error = %v, diagnostics = %#v", err, result.Diagnostics)
	}
	advisories := diagnosticsWithCode(result.Diagnostics, diffKnownTypeSkippedCode)
	if len(advisories) != 1 || !strings.Contains(advisories[0].Message, "Application argocd/kept:") ||
		!strings.Contains(advisories[0].Message, `the built-in Argo Rollout known type at "spec.template.spec"`) {
		t.Fatalf("%s diagnostics = %#v, want one for Application argocd/kept that names the Rollout default", diffKnownTypeSkippedCode, advisories)
	}
	if len(result.Results) != 2 {
		t.Fatalf("Results = %#v, want the change of kept and the removal of gone", result.Results)
	}
}
