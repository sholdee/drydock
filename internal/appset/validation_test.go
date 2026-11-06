package appset

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/diagnostic"
)

func listAppSetYAML(elements, templateName, extraSpec string) []byte {
	return []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: guestbook
  namespace: argocd
spec:
  goTemplate: true
` + extraSpec + `
  generators:
    - list:
        elements:
` + elements + `
  template:
    metadata:
      name: ` + templateName + `
      labels:
        env: '{{.env}}'
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

func diagnosticsWithCode(diags []diagnostic.Diagnostic, code string) []diagnostic.Diagnostic {
	var out []diagnostic.Diagnostic
	for _, diag := range diags {
		if diagnostic.StableCode(diag) == code {
			out = append(out, diag)
		}
	}
	return out
}

func TestGenerateDropsGeneratedApplicationWithEmptyName(t *testing.T) {
	data := listAppSetYAML(`          - name: alpha
            env: dev
          - name: ""
            env: dev
          - name: beta
            env: dev`, `''`, `  templatePatch: |
    metadata:
      name: '{{.name}}'`)

	apps, diags, err := GenerateFromYAML(t.TempDir(), "app-set.yaml", data)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"alpha", "beta"}) {
		t.Fatalf("generated names = %#v, want the named Applications only", got)
	}
	if len(diags) != 1 {
		t.Fatalf("diagnostics = %#v, want one validation warning", diags)
	}
	diag := diags[0]
	if diag.Severity != diagnostic.SeverityWarning || diag.Category != "appset" || diag.Code != "appset.application-validation-failed" {
		t.Fatalf("diagnostic = %#v, want appset.application-validation-failed warning", diag)
	}
	want := "ApplicationSet guestbook contains an application with no name; a name must be set in the template's metadata.name or via a templatePatch (generateName is not supported)"
	if !strings.Contains(diag.Message, want) {
		t.Fatalf("diagnostic message = %q, want upstream message %q", diag.Message, want)
	}
	if diag.Provenance.Path != "app-set.yaml" {
		t.Fatalf("diagnostic provenance = %#v", diag.Provenance)
	}
}

func TestGenerateDropsGenerateNameOnlyApplications(t *testing.T) {
	data := listAppSetYAML(`          - name: alpha
            env: dev
          - name: beta
            env: dev`, `''`, `  templatePatch: |
    metadata:
      generateName: '{{.name}}-'`)

	apps, diags, err := GenerateFromYAML(t.TempDir(), "app-set.yaml", data)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if len(apps) != 0 {
		t.Fatalf("generated apps = %#v, want none", apps)
	}
	// Upstream keys errors by QualifiedName, so every nameless Application
	// collapses onto one validation error.
	if len(diags) != 1 || !strings.Contains(diags[0].Message, "generateName is not supported") {
		t.Fatalf("diagnostics = %#v, want one empty-name validation warning", diags)
	}
	if strings.Contains(diags[0].Message, "duplicate") {
		t.Fatalf("diagnostic = %q, want the empty-name check to run before duplicate detection", diags[0].Message)
	}
}

func TestGenerateDropsEveryApplicationSharingADuplicateName(t *testing.T) {
	data := listAppSetYAML(`          - name: alpha
            env: dev
          - name: beta
            env: dev
          - name: alpha
            env: prod
          - name: gamma
            env: dev
          - name: gamma
            env: prod`, `'{{.name}}'`, "")

	apps, diags, err := GenerateFromYAML(t.TempDir(), "app-set.yaml", data)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"beta"}) {
		t.Fatalf("generated names = %#v, want every duplicated name dropped", got)
	}
	if len(diags) != 2 {
		t.Fatalf("diagnostics = %#v, want one warning per duplicated name", diags)
	}
	for i, name := range []string{"alpha", "gamma"} {
		want := "ApplicationSet guestbook contains applications with duplicate name: " + name
		if diags[i].Code != "appset.application-validation-failed" || !strings.Contains(diags[i].Message, want) {
			t.Fatalf("diagnostic[%d] = %#v, want %q", i, diags[i], want)
		}
	}
}

func TestGenerateApplicationValidationIsSkippedAfterTemplateRenderFailure(t *testing.T) {
	data := listAppSetYAML(`          - name: alpha
            env: dev
          - name: alpha
            env: prod`, `'{{.name}}{{.missing.field}}'`, `  goTemplateOptions: ["missingkey=error"]`)

	apps, diags, err := GenerateFromYAML(t.TempDir(), "app-set.yaml", data)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if len(apps) != 0 {
		t.Fatalf("generated apps = %#v, want none", apps)
	}
	if got := diagnosticsWithCode(diags, "appset.application-validation-failed"); len(got) != 0 {
		t.Fatalf("diagnostics = %#v, want no validation warnings when rendering failed", diags)
	}
}

func rollingSyncSpec(strategy string) string {
	return "  strategy:\n" + strategy
}

// generateWithProgressiveSyncs generates as if argocd-cmd-params-cm sets
// applicationsetcontroller.enable.progressive.syncs to true.
func generateWithProgressiveSyncs(t *testing.T, data []byte) ([]GeneratedApplication, []diagnostic.Diagnostic, error) {
	t.Helper()
	return GenerateFromYAMLWithOptions(t.TempDir(), "app-set.yaml", data, Options{ProgressiveSyncs: true})
}

// With progressive syncs on, Argo CD v3.6.1 sets ErrorOccurred and returns
// before it creates, updates, or deletes any Application, and before it
// reports the ApplicationValidationError condition
// (applicationset_controller.go:263-277). The duplicated name proves that
// the validation warning is left out too.
func TestGenerateWarnsOnRollingSyncWithoutSteps(t *testing.T) {
	for name, strategy := range map[string]string{
		"omitted steps": "    type: RollingSync\n    rollingSync: {}\n",
		"empty steps":   "    type: RollingSync\n    rollingSync:\n      steps: []\n",
	} {
		t.Run(name, func(t *testing.T) {
			data := listAppSetYAML(`          - name: alpha
            env: dev
          - name: alpha
            env: dev
          - name: beta
            env: prod`, `'{{.name}}'`, rollingSyncSpec(strategy))
			apps, diags, err := generateWithProgressiveSyncs(t, data)
			if err != nil {
				t.Fatalf("GenerateFromYAML() error = %v", err)
			}
			if got := generatedNames(apps); len(got) != 0 {
				t.Fatalf("generated names = %#v, want none", got)
			}
			if len(diags) != 1 {
				t.Fatalf("diagnostics = %#v, want one rollout warning", diags)
			}
			diag := diags[0]
			if diag.Severity != diagnostic.SeverityWarning || diag.Code != "appset.rollout-no-steps" || !strings.Contains(diag.Message, "No steps defined for rollout") {
				t.Fatalf("diagnostic = %#v, want appset.rollout-no-steps warning", diag)
			}
			if !strings.Contains(diag.Message, "argocd/guestbook") || diag.Provenance.Pointer != "spec.strategy.rollingSync.steps" {
				t.Fatalf("diagnostic = %#v, want ApplicationSet name and strategy pointer", diag)
			}
		})
	}
}

func TestGenerateSkipsRolloutValidationWithoutRollingSync(t *testing.T) {
	for name, extra := range map[string]string{
		"no strategy":              "",
		"all at once":              rollingSyncSpec("    type: AllAtOnce\n"),
		"rolling sync without cfg": rollingSyncSpec("    type: RollingSync\n"),
		"rollingSync on AllAtOnce": rollingSyncSpec("    type: AllAtOnce\n    rollingSync:\n      steps:\n        - matchExpressions:\n            - key: env\n              operator: Exists\n"),
	} {
		t.Run(name, func(t *testing.T) {
			data := listAppSetYAML(`          - name: alpha
            env: dev`, `'{{.name}}'`, extra)
			apps, diags, err := GenerateFromYAML(t.TempDir(), "app-set.yaml", data)
			if err != nil {
				t.Fatalf("GenerateFromYAML() error = %v", err)
			}
			if len(apps) != 1 || len(diags) != 0 {
				t.Fatalf("apps = %#v diagnostics = %#v, want one app and no rollout warnings", generatedNames(apps), diags)
			}
		})
	}
}

func TestGenerateAcceptsValidRollingSyncConfig(t *testing.T) {
	data := listAppSetYAML(`          - name: alpha
            env: dev
          - name: beta
            env: prod`, `'{{.name}}'`, rollingSyncSpec(`    type: RollingSync
    rollingSync:
      steps:
        - matchExpressions:
            - key: env
              operator: In
              values: [dev]
        - matchExpressions:
            - key: env
              operator: NotIn
              values: [dev]
          maxUpdate: 50%
`))
	apps, diags, err := generateWithProgressiveSyncs(t, data)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if len(apps) != 2 || len(diags) != 0 {
		t.Fatalf("apps = %#v diagnostics = %#v, want both apps and no warnings", generatedNames(apps), diags)
	}
}

func TestGenerateWarnsOnInvalidRollingSyncConfig(t *testing.T) {
	tests := []struct {
		name     string
		elements string
		steps    string
		want     []string
	}{
		{
			name: "invalid operator",
			elements: `          - name: alpha
            env: dev`,
			steps: `        - matchExpressions:
            - key: env
              operator: Exists
              values: [dev]
`,
			want: []string{
				"Step 1 has invalid matchExpression operators: Exists. Supported Operators are 'In' and 'NotIn'",
				"Steps [1] have no applications matching their criteria",
			},
		},
		{
			name: "invalid operators in several steps",
			elements: `          - name: alpha
            env: dev`,
			steps: `        - matchExpressions:
            - key: env
              operator: Exists
        - matchExpressions:
            - key: env
              operator: In
              values: [dev]
        - matchExpressions:
            - key: env
              operator: DoesNotExist
`,
			want: []string{
				"Steps 1, 3 have invalid matchExpression operators: Exists, DoesNotExist. Supported Operators are 'In' and 'NotIn'",
				"Steps [1 3] have no applications matching their criteria",
			},
		},
		{
			name: "application selected by several steps",
			elements: `          - name: alpha
            env: dev
          - name: beta
            env: prod`,
			steps: `        - matchExpressions:
            - key: env
              operator: In
              values: [dev]
        - matchExpressions:
            - key: env
              operator: NotIn
              values: [prod]
        - matchExpressions:
            - key: env
              operator: In
              values: [prod]
`,
			want: []string{"Application 'alpha' is selected by multiple steps: (1-2)"},
		},
		{
			name: "applications selected by several steps",
			elements: `          - name: alpha
            env: dev
          - name: beta
            env: prod`,
			steps: `        - matchExpressions:
            - key: env
              operator: In
              values: [dev]
        - matchExpressions:
            - key: env
              operator: In
              values: [dev, prod]
        - matchExpressions:
            - key: env
              operator: In
              values: [prod]
`,
			want: []string{"Applications 'alpha', 'beta' are selected by multiple steps: [(1-2), (2-3)]"},
		},
		{
			name: "invalid maxUpdate",
			elements: `          - name: alpha
            env: dev`,
			steps: `        - matchExpressions:
            - key: env
              operator: In
              values: [dev]
          maxUpdate: two
`,
			want: []string{"Step 1 has an invalid maxUpdate value: two"},
		},
		{
			name: "step matching no applications",
			elements: `          - name: alpha
            env: dev`,
			steps: `        - matchExpressions:
            - key: env
              operator: In
              values: [dev]
        - matchExpressions:
            - key: env
              operator: In
              values: [prod]
        - matchExpressions:
            - key: tier
              operator: In
              values: [db]
`,
			want: []string{"Steps [2 3] have no applications matching their criteria"},
		},
		{
			name: "every issue class in upstream priority order",
			elements: `          - name: alpha
            env: dev
          - name: beta
            env: prod`,
			steps: `        - matchExpressions:
            - key: env
              operator: Exists
          maxUpdate: 1
        - matchExpressions:
            - key: env
              operator: NotIn
              values: [qa]
          maxUpdate: lots
        - matchExpressions:
            - key: env
              operator: In
              values: [prod]
        - matchExpressions:
            - key: env
              operator: In
              values: [qa]
`,
			want: []string{
				"Step 1 has invalid matchExpression operators: Exists. Supported Operators are 'In' and 'NotIn'",
				"Application 'beta' is selected by multiple steps: (2-3)",
				"Step 2 has an invalid maxUpdate value: lots",
				"Steps [1 4] have no applications matching their criteria",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := listAppSetYAML(tt.elements, `'{{.name}}'`, rollingSyncSpec("    type: RollingSync\n    rollingSync:\n      steps:\n"+tt.steps))
			apps, diags, err := generateWithProgressiveSyncs(t, data)
			if err != nil {
				t.Fatalf("GenerateFromYAML() error = %v", err)
			}
			wantApps := strings.Count(tt.elements, "- name:")
			if len(apps) != wantApps {
				t.Fatalf("generated apps = %#v, want %d (rollout warnings keep generation)", generatedNames(apps), wantApps)
			}
			if len(diags) != len(tt.want) {
				t.Fatalf("diagnostics = %#v, want %d rollout warnings", diags, len(tt.want))
			}
			for i, want := range tt.want {
				diag := diags[i]
				if diag.Severity != diagnostic.SeverityWarning || diag.Code != "appset.invalid-rollout-config" {
					t.Fatalf("diagnostic[%d] = %#v, want appset.invalid-rollout-config warning", i, diag)
				}
				if !strings.Contains(diag.Message, "argocd/guestbook has an invalid RollingSync rollout config: "+want+";") {
					t.Fatalf("diagnostic[%d] message = %q, want upstream message %q", i, diag.Message, want)
				}
			}
		})
	}
}

func TestGenerateRolloutValidationSeesApplicationsDroppedByNameValidation(t *testing.T) {
	// Argo CD evaluates rollout steps over every generated Application before
	// it drops the ones that fail validation, so a duplicated name selected by
	// one step is reported as selected twice.
	data := listAppSetYAML(`          - name: alpha
            env: dev
          - name: alpha
            env: dev
          - name: beta
            env: prod`, `'{{.name}}'`, rollingSyncSpec(`    type: RollingSync
    rollingSync:
      steps:
        - matchExpressions:
            - key: env
              operator: In
              values: [dev]
        - matchExpressions:
            - key: env
              operator: In
              values: [prod]
`))
	apps, diags, err := generateWithProgressiveSyncs(t, data)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"beta"}) {
		t.Fatalf("generated names = %#v", got)
	}
	if got := diagnosticsWithCode(diags, "appset.application-validation-failed"); len(got) != 1 {
		t.Fatalf("diagnostics = %#v, want one duplicate-name warning", diags)
	}
	rollout := diagnosticsWithCode(diags, "appset.invalid-rollout-config")
	if len(rollout) != 1 || !strings.Contains(rollout[0].Message, "Application 'alpha' is selected by multiple steps: (1-1)") {
		t.Fatalf("rollout diagnostics = %#v, want duplicate selection of alpha", rollout)
	}
}

// With progressive syncs off (the Argo CD default), the controller ignores
// the strategy (applicationset_controller.go:254-295): every Application
// generates, nothing is linted, and one advisory says why.
func TestGenerateIgnoresRollingSyncWithoutProgressiveSyncs(t *testing.T) {
	for name, strategy := range map[string]string{
		"empty steps": "    type: RollingSync\n    rollingSync:\n      steps: []\n",
		"invalid steps": `    type: RollingSync
    rollingSync:
      steps:
        - matchExpressions:
            - key: env
              operator: Exists
          maxUpdate: "1"
        - matchExpressions:
            - key: env
              operator: In
              values: [none]
`,
	} {
		t.Run(name, func(t *testing.T) {
			data := listAppSetYAML(`          - name: alpha
            env: dev
          - name: beta
            env: prod`, `'{{.name}}'`, rollingSyncSpec(strategy))
			apps, diags, err := GenerateFromYAML(t.TempDir(), "app-set.yaml", data)
			if err != nil {
				t.Fatalf("GenerateFromYAML() error = %v", err)
			}
			if got := generatedNames(apps); !slices.Equal(got, []string{"alpha", "beta"}) {
				t.Fatalf("generated names = %#v, want every Application", got)
			}
			if len(diags) != 1 {
				t.Fatalf("diagnostics = %#v, want one advisory", diags)
			}
			diag := diags[0]
			if diag.Severity != diagnostic.SeverityWarning || diag.Code != diagnostic.CodeAppSetRolloutStrategyIgnored || diagnostic.StableCode(diag) != diag.Code {
				t.Fatalf("diagnostic = %#v, want appset.rollout-strategy-ignored", diag)
			}
			if !strings.Contains(diag.Message, "argocd/guestbook") || !strings.Contains(diag.Message, "applicationsetcontroller.enable.progressive.syncs") || diag.Provenance.Pointer != "spec.strategy" {
				t.Fatalf("diagnostic = %#v, want ApplicationSet name, the setting, and the strategy pointer", diag)
			}
		})
	}
}

// With progressive syncs on and RollingSync steps set, Argo CD v3.6.1 writes
// every generated Application with automated.enabled false
// (progressive_sync.go:997-1013); otherwise it keeps the template's policy.
func TestGenerateDisablesAutomatedSyncForRollingSync(t *testing.T) {
	appSetYAML := func(strategy string) []byte {
		return []byte(`apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: guestbook
  namespace: argocd
spec:
  goTemplate: true
` + strategy + `  generators:
    - list:
        elements:
          - name: alpha
            env: dev
          - name: beta
            env: prod
  template:
    metadata:
      name: '{{.name}}'
      labels:
        env: '{{.env}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: apps/{{.name}}
        targetRevision: main
      destination:
        name: in-cluster
        namespace: default
      syncPolicy:
        automated:
          prune: true
`)
	}
	rollingSync := `  strategy:
    type: RollingSync
    rollingSync:
      steps:
        - matchExpressions:
            - key: env
              operator: In
              values: [dev]
`
	for _, tc := range []struct {
		name     string
		strategy string
		options  Options
		want     *bool
	}{
		{name: "progressive syncs on", strategy: rollingSync, options: Options{ProgressiveSyncs: true}, want: new(false)},
		{name: "progressive syncs off", strategy: rollingSync},
		{name: "no strategy", options: Options{ProgressiveSyncs: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			apps, _, err := GenerateFromYAMLWithOptions(t.TempDir(), "app-set.yaml", appSetYAML(tc.strategy), tc.options)
			if err != nil {
				t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
			}
			if len(apps) != 2 {
				t.Fatalf("generated names = %#v, want both", generatedNames(apps))
			}
			for _, app := range apps {
				automated := app.Application.Spec.SyncPolicy.Automated
				if automated == nil || automated.Prune == nil || !*automated.Prune {
					t.Fatalf("%s automated = %#v, want the template's prune kept", app.Application.Name, automated)
				}
				if got := automated.Enabled; (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
					t.Fatalf("%s automated.enabled = %s, want %s", app.Application.Name, boolPointerString(got), boolPointerString(tc.want))
				}
			}
		})
	}
}

func boolPointerString(value *bool) string {
	if value == nil {
		return "unset"
	}
	return strconv.FormatBool(*value)
}
