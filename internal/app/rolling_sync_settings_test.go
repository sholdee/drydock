package app

import (
	"path/filepath"
	"slices"
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
