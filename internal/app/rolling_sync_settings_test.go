package app

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"

	"github.com/sholdee/drydock/internal/diagnostic"
)

// writeRollingSyncWithoutSteps writes an ApplicationSet whose RollingSync
// strategy has an empty steps list.
func writeRollingSyncWithoutSteps(t *testing.T, root string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "appsets", "rolling.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: rolling
  namespace: argocd
spec:
  goTemplate: true
  strategy:
    type: RollingSync
    rollingSync:
      steps: []
  generators:
    - list:
        elements:
          - name: rolling-alpha
  template:
    metadata:
      name: '{{.name}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: apps/{{.name}}
        targetRevision: main
      destination:
        name: in-cluster
        namespace: default
`)
}

func diagnosticCodes(diags []diagnostic.Diagnostic) []string {
	codes := make([]string, 0, len(diags))
	for _, diag := range diagnostic.WithStableCodes(diags) {
		codes = append(codes, diag.Code)
	}
	return codes
}

// TestRenderedArgoCDInstallProgressiveSyncsGateRollingSync pins that drydock
// reads applicationsetcontroller.enable.progressive.syncs from the rendered
// Argo CD install. Argo CD v3.6.1 ignores a RollingSync strategy without it,
// and with it an empty steps list stops the ApplicationSet from reconciling
// any Application (applicationset_controller.go:254-295). Fleet discovery
// learns the flag only after the first ApplicationSet expansion, so the
// Application generated before must go away.
func TestRenderedArgoCDInstallProgressiveSyncsGateRollingSync(t *testing.T) {
	for _, shape := range argoCDInstallShapes() {
		t.Run(shape.name, func(t *testing.T) {
			for _, tc := range []struct {
				value    string
				wantApp  bool
				wantCode string
			}{
				{value: "true", wantApp: false, wantCode: "appset.rollout-no-steps"},
				{value: "TRUE", wantApp: false, wantCode: "appset.rollout-no-steps"},
				{value: "1", wantApp: true, wantCode: diagnostic.CodeAppSetRolloutStrategyIgnored},
			} {
				root := t.TempDir()
				shape.write(t, root, []string{"values.yaml"})
				writeTestFile(t, filepath.Join(root, "argocd", "values.yaml"), `configs:
  params:
    applicationsetcontroller.enable.progressive.syncs: "`+tc.value+`"
`)
				writeRollingSyncWithoutSteps(t, root)

				result := listArgoCDInstallSettings(t, root, shape, fakeArgoCDChartAcquirer(t))
				if got := result.Settings.EnableProgressiveSyncs.Value; got != tc.value {
					t.Fatalf("%q: EnableProgressiveSyncs = %q, want the configs.params value", tc.value, got)
				}
				gotApp := slices.ContainsFunc(result.Applications, func(app argoappv1.Application) bool {
					return app.Name == "rolling-alpha"
				})
				if gotApp != tc.wantApp {
					t.Fatalf("%q: rolling-alpha generated = %t, want %t", tc.value, gotApp, tc.wantApp)
				}
				codes := diagnosticCodes(result.Diagnostics)
				if !slices.Contains(codes, tc.wantCode) {
					t.Fatalf("%q: diagnostic codes = %v, want %s", tc.value, codes, tc.wantCode)
				}
				for _, other := range []string{"appset.rollout-no-steps", diagnostic.CodeAppSetRolloutStrategyIgnored} {
					if other != tc.wantCode && slices.Contains(codes, other) {
						t.Fatalf("%q: diagnostic codes = %v, want no %s", tc.value, codes, other)
					}
				}
			}
		})
	}
}

// writeRollingSyncAppOfApps writes an ApplicationSet with an empty RollingSync
// steps list that, without progressive syncs, generates a duplicated name
// (alpha) and an app-of-apps (beta) whose render declares grandchild.
func writeRollingSyncAppOfApps(t *testing.T, root string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "appsets", "rolling.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: rolling
  namespace: argocd
spec:
  goTemplate: true
  strategy:
    type: RollingSync
    rollingSync:
      steps: []
  generators:
    - list:
        elements:
          - name: alpha
          - name: alpha
          - name: beta
  template:
    metadata:
      name: '{{.name}}'
      namespace: argocd
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: children/{{.name}}
        targetRevision: main
      destination:
        name: in-cluster
        namespace: argocd
`)
	writeTestFile(t, filepath.Join(root, "children", "alpha", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: alpha
data:
  value: alpha
`)
	// A Helm chart, so only a render of beta declares the grandchild.
	writeTestFile(t, filepath.Join(root, "children", "beta", "Chart.yaml"), `apiVersion: v2
name: beta
version: 0.1.0
`)
	writeTestFile(t, filepath.Join(root, "children", "beta", "values.yaml"), "suffix: rendered\n")
	writeTestFile(t, filepath.Join(root, "children", "beta", "templates", "grandchild.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: grandchild-{{ .Values.suffix }}
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    path: children/alpha
    targetRevision: main
  destination:
    name: in-cluster
    namespace: default
`)
}

// discoveryOutcome is what a discovery result shows of an ApplicationSet:
// the Application names and the ApplicationSet diagnostic codes.
func discoveryOutcome(result BuildResult) ([]string, []string) {
	names := make([]string, 0, len(result.Applications))
	for _, app := range result.Applications {
		names = append(names, app.Namespace+"/"+app.Name)
	}
	var codes []string
	for _, code := range diagnosticCodes(result.Diagnostics) {
		if strings.HasPrefix(code, "appset.") {
			codes = append(codes, code)
		}
	}
	slices.Sort(names)
	slices.Sort(codes)
	return names, codes
}

// TestProgressiveSyncsFromRenderedInstallMatchesKnownEarly pins that the
// result does not depend on where drydock finds
// applicationsetcontroller.enable.progressive.syncs. With the flag in a
// committed argocd-cmd-params-cm, the first ApplicationSet expansion already
// knows it. With the flag only in the rendered Argo CD install, the first
// expansion does not, and drydock must still end with no Application from
// the empty steps list, no Application those would declare, and none of
// their warnings: Argo CD returns at the steps check
// (applicationset_controller.go:265-277), before it validates names.
func TestProgressiveSyncsFromRenderedInstallMatchesKnownEarly(t *testing.T) {
	for _, shape := range argoCDInstallShapes() {
		t.Run(shape.name, func(t *testing.T) {
			outcome := func(early bool) ([]string, []string) {
				root := t.TempDir()
				shape.write(t, root, []string{"values.yaml"})
				writeTestFile(t, filepath.Join(root, "argocd", "values.yaml"), `configs:
  params:
    applicationsetcontroller.enable.progressive.syncs: "true"
`)
				if early {
					writeTestFile(t, filepath.Join(root, "settings", "argocd-cmd-params-cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cmd-params-cm
  namespace: argocd
data:
  applicationsetcontroller.enable.progressive.syncs: "true"
`)
				}
				writeRollingSyncAppOfApps(t, root)
				return discoveryOutcome(listArgoCDInstallSettings(t, root, shape, fakeArgoCDChartAcquirer(t)))
			}
			earlyNames, earlyCodes := outcome(true)
			lateNames, lateCodes := outcome(false)
			for _, name := range []string{"argocd/alpha", "argocd/beta", "argocd/grandchild-rendered"} {
				if slices.Contains(earlyNames, name) {
					t.Fatalf("flag known early: Applications = %v, want no %s", earlyNames, name)
				}
			}
			if !slices.Equal(lateNames, earlyNames) {
				t.Fatalf("flag from rendered install: Applications = %v, want %v as with the flag known early", lateNames, earlyNames)
			}
			if !slices.Equal(lateCodes, earlyCodes) {
				t.Fatalf("flag from rendered install: appset codes = %v, want %v as with the flag known early", lateCodes, earlyCodes)
			}
			if !slices.Contains(earlyCodes, "appset.rollout-no-steps") || slices.Contains(earlyCodes, "appset.application-validation-failed") {
				t.Fatalf("flag known early: appset codes = %v, want rollout-no-steps and no name validation warning", earlyCodes)
			}
		})
	}
}
