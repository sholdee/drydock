package app

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/diagnostic"
	sourcepkg "github.com/sholdee/drydock/internal/source"
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

// writeRollingSyncSettingsCycle writes an ApplicationSet with an empty
// RollingSync steps list that generates the only Application that sets
// applicationsetcontroller.enable.progressive.syncs to true. With the flag
// off it generates that Application, and with the flag on it does not.
func writeRollingSyncSettingsCycle(t *testing.T, root string) {
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
          - name: settings
  template:
    metadata:
      name: '{{.name}}'
      namespace: argocd
    spec:
      project: default
      source:
        repoURL: https://example.com/repo.git
        path: charts/settings
        targetRevision: main
      destination:
        name: in-cluster
        namespace: argocd
`)
	writeTestFile(t, filepath.Join(root, "charts", "settings", "Chart.yaml"), "apiVersion: v2\nname: settings\nversion: 0.1.0\n")
	writeTestFile(t, filepath.Join(root, "charts", "settings", "values.yaml"), "flag: \"true\"\n")
	writeTestFile(t, filepath.Join(root, "charts", "settings", "templates", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cmd-params-cm
  namespace: argocd
data:
  applicationsetcontroller.enable.progressive.syncs: {{ .Values.flag | quote }}
`)
}

// TestProgressiveSyncsCycleWarnsAfterSecondRun pins the case where the flag
// decides its own source. The first run, with the flag off, generates the
// Application that turns it on. The second run, with the flag on, does not.
// drydock keeps the second result, warns about the cycle, and never runs a
// third time.
func TestProgressiveSyncsCycleWarnsAfterSecondRun(t *testing.T) {
	root := t.TempDir()
	writeRollingSyncSettingsCycle(t, root)
	runs := 0
	orchestrator := Orchestrator{discoveryRunObserver: func() { runs++ }}
	result, err := orchestrator.ListApplications(context.Background(), BuildRequest{
		Path:        root,
		Parallelism: 1,
		RepoMaps:    []sourcepkg.RepoMap{{URL: "https://example.com/repo.git", Path: root}},
	})
	if err != nil {
		t.Fatalf("ListApplications() error = %v, diagnostics = %#v", err, result.Diagnostics)
	}
	if runs != 2 {
		t.Fatalf("discovery runs = %d, want 2", runs)
	}
	names, _ := discoveryOutcome(result)
	if slices.Contains(names, "argocd/settings") {
		t.Fatalf("Applications = %v, want the second run's result without argocd/settings", names)
	}
	var cycle *diagnostic.Diagnostic
	for _, diag := range diagnostic.WithStableCodes(result.Diagnostics) {
		if diag.Code == diagnostic.CodeSettingsProgressiveSyncsCycle {
			cycle = &diag
		}
	}
	if cycle == nil {
		t.Fatalf("diagnostic codes = %v, want %s", diagnosticCodes(result.Diagnostics), diagnostic.CodeSettingsProgressiveSyncsCycle)
	}
	if !strings.Contains(cycle.Message, "discovery with it true ends with it false") || cycle.Provenance.Path == "" {
		t.Fatalf("cycle diagnostic = %#v, want both values and the settings source", cycle)
	}
	if strictExemptDiagnostic(*cycle) {
		t.Fatal("the cycle diagnostic is strict-exempt, want --strict to fail on it")
	}
}

// TestProgressiveSyncsRestartNeedsRollingSync pins that discovery runs a
// second time only when an ApplicationSet has a RollingSync strategy: the
// flag changes nothing else.
func TestProgressiveSyncsRestartNeedsRollingSync(t *testing.T) {
	shape := argoCDInstallShapes()[1]
	for _, tc := range []struct {
		name        string
		rollingSync bool
		wantRuns    int
	}{
		{name: "no RollingSync ApplicationSet", wantRuns: 1},
		{name: "RollingSync ApplicationSet", rollingSync: true, wantRuns: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			shape.write(t, root, []string{"values.yaml"})
			writeTestFile(t, filepath.Join(root, "argocd", "values.yaml"), `configs:
  params:
    applicationsetcontroller.enable.progressive.syncs: "true"
`)
			if tc.rollingSync {
				writeRollingSyncWithoutSteps(t, root)
			}
			runs := 0
			orchestrator := Orchestrator{ChartAcquirer: fakeArgoCDChartAcquirer(t), discoveryRunObserver: func() { runs++ }}
			result, err := orchestrator.ListApplications(context.Background(), BuildRequest{Path: root, Parallelism: 1})
			if err != nil {
				t.Fatalf("ListApplications() error = %v, diagnostics = %#v", err, result.Diagnostics)
			}
			if got := result.Settings.EnableProgressiveSyncs.Value; got != "true" {
				t.Fatalf("EnableProgressiveSyncs = %q, want the rendered install's value", got)
			}
			if runs != tc.wantRuns {
				t.Fatalf("discovery runs = %d, want %d", runs, tc.wantRuns)
			}
		})
	}
}

// writeRollingSyncNestedApplicationSet writes an ApplicationSet with an empty
// RollingSync steps list that, without progressive syncs, generates the
// app-of-apps beta. beta's chart declares ApplicationSet q with the given
// generators. Argo CD never creates beta, so it never sees q.
func writeRollingSyncNestedApplicationSet(t *testing.T, root, generators string) {
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
	writeTestFile(t, filepath.Join(root, "children", "beta", "Chart.yaml"), "apiVersion: v2\nname: beta\nversion: 0.1.0\n")
	writeTestFile(t, filepath.Join(root, "children", "beta", "templates", "q.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: q
  namespace: argocd
spec:
  goTemplate: true
  generators:
`+generators+`  template:
    metadata:
      name: 'q-{{"{{"}}.name{{"}}"}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: x
        targetRevision: main
      destination:
        name: in-cluster
        namespace: default
`)
}

// TestProgressiveSyncsRestartAfterFailedFirstRun pins that a first discovery
// run that fails because it ran without the flag still restarts with the
// flag from the rendered install. The result and error must be those of a
// repository that commits the flag.
func TestProgressiveSyncsRestartAfterFailedFirstRun(t *testing.T) {
	cases := []struct {
		name       string
		generators string
		strict     bool
		wantErr    string
	}{
		{
			name: "bad git files glob",
			generators: `    - git:
        repoURL: https://github.com/example/repo
        revision: main
        files:
          - path: 'missing/[*.json'
`,
		},
		{
			name:       "empty list under strict",
			generators: "    - list:\n        elements: []\n",
			strict:     true,
			wantErr:    "No steps defined for rollout",
		},
	}
	for _, shape := range argoCDInstallShapes() {
		for _, tc := range cases {
			t.Run(shape.name+"/"+tc.name, func(t *testing.T) {
				outcome := func(early bool) ([]string, []string, error) {
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
					writeRollingSyncNestedApplicationSet(t, root, tc.generators)
					request := BuildRequest{Path: root, Parallelism: 1, Strict: tc.strict}
					if shape.repoMaps != nil {
						request.RepoMaps = shape.repoMaps(root)
					}
					result, err := Orchestrator{ChartAcquirer: fakeArgoCDChartAcquirer(t)}.ListApplications(context.Background(), request)
					names, codes := discoveryOutcome(result)
					return names, codes, err
				}
				_, _, earlyErr := outcome(true)
				lateNames, lateCodes, lateErr := outcome(false)
				for label, err := range map[string]error{"flag known early": earlyErr, "flag from rendered install": lateErr} {
					if tc.wantErr == "" && err != nil {
						t.Fatalf("%s: ListApplications() error = %v, want none", label, err)
					}
					if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
						t.Fatalf("%s: ListApplications() error = %v, want %q", label, err, tc.wantErr)
					}
				}
				if tc.wantErr == "" {
					if slices.Contains(lateNames, "argocd/beta") || !slices.Contains(lateCodes, "appset.rollout-no-steps") {
						t.Fatalf("flag from rendered install: Applications = %v, codes = %v, want no beta and rollout-no-steps", lateNames, lateCodes)
					}
				}
			})
		}
	}
}
