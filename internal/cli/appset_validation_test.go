package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func writeAppSetValidationFixture(t *testing.T, root string) {
	t.Helper()
	writeCLITestFile(t, filepath.Join(root, "appset.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: guestbook
  namespace: argocd
spec:
  goTemplate: true
  generators:
    - list:
        elements:
          - name: alpha
          - name: beta
          - name: alpha
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

// writeProgressiveSyncsOn writes an argocd-cmd-params-cm that turns on
// applicationsetcontroller.enable.progressive.syncs.
func writeProgressiveSyncsOn(t *testing.T, root string) {
	t.Helper()
	writeCLITestFile(t, filepath.Join(root, "argocd", "argocd-cmd-params-cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cmd-params-cm
  namespace: argocd
data:
  applicationsetcontroller.enable.progressive.syncs: "true"
`)
}

// writeRollingSyncWithoutSteps writes an ApplicationSet whose RollingSync
// strategy has an empty steps list.
func writeRollingSyncWithoutSteps(t *testing.T, root string) {
	t.Helper()
	writeCLITestFile(t, filepath.Join(root, "appset.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: guestbook
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

// With progressive syncs on, Argo CD v3.6.1 reconciles no Application of an
// ApplicationSet whose RollingSync steps list is empty, so drydock lists
// none, warns, and --strict fails as for other ApplicationSet errors.
func TestGetAppsDropsRollingSyncWithoutStepsWhenProgressiveSyncsOn(t *testing.T) {
	root := t.TempDir()
	writeRollingSyncWithoutSteps(t, root)
	writeProgressiveSyncsOn(t, root)

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"get", "apps", "--path", root, "-o", "name"})
	var out, stderr bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&stderr)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstderr:\n%s", err, stderr.String())
	}
	if got := out.String(); got != "" {
		t.Fatalf("stdout = %q, want no Applications", got)
	}
	want := "warning appset.rollout-no-steps: ApplicationSet argocd/guestbook uses the RollingSync strategy with an empty steps list: No steps defined for rollout;"
	if !strings.Contains(stderr.String(), want) {
		t.Fatalf("stderr missing %q:\n%s", want, stderr.String())
	}
	if strings.Contains(stderr.String(), "generated zero Applications") {
		t.Fatalf("stderr also reports generated zero Applications:\n%s", stderr.String())
	}

	strictCmd := NewRootCommand(VersionInfo{})
	strictCmd.SetArgs([]string{"get", "apps", "--path", root, "-o", "name", "--strict"})
	out.Reset()
	stderr.Reset()
	strictCmd.SetOut(&out)
	strictCmd.SetErr(&stderr)
	err := strictCmd.Execute()
	if err == nil || !strings.Contains(stderr.String()+err.Error(), "No steps defined for rollout") {
		t.Fatalf("Execute(--strict) error = %v, want the rollout-no-steps warning to fail\nstderr:\n%s", err, stderr.String())
	}
}

// With progressive syncs off (the Argo CD default), Argo CD ignores the
// RollingSync strategy, so drydock lists the Applications and gives one
// advisory that --strict does not fail on.
func TestGetAppsIgnoresRollingSyncWhenProgressiveSyncsOff(t *testing.T) {
	root := t.TempDir()
	writeRollingSyncWithoutSteps(t, root)

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"get", "apps", "--path", root, "-o", "name", "--strict"})
	var out, stderr bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&stderr)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute(--strict) error = %v\nstderr:\n%s", err, stderr.String())
	}
	if got, want := out.String(), "argocd/alpha\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	want := "warning appset.rollout-strategy-ignored: ApplicationSet argocd/guestbook: the RollingSync strategy is not used, because applicationsetcontroller.enable.progressive.syncs is not true in argocd-cmd-params-cm"
	if !strings.Contains(stderr.String(), want) {
		t.Fatalf("stderr missing %q:\n%s", want, stderr.String())
	}
	if strings.Contains(stderr.String(), "rollout-no-steps") {
		t.Fatalf("stderr reports rollout-no-steps with progressive syncs off:\n%s", stderr.String())
	}
}

func TestGetAppsReportsGeneratedApplicationValidationWarnings(t *testing.T) {
	root := t.TempDir()
	writeAppSetValidationFixture(t, root)

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"get", "apps", "--path", root, "-o", "name"})
	var out, stderr bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&stderr)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstderr:\n%s", err, stderr.String())
	}
	if got, want := out.String(), "argocd/beta\n"; got != want {
		t.Fatalf("stdout = %q, want %q (every duplicated name dropped, stdout free of diagnostics)", got, want)
	}
	for _, want := range []string{
		"warning appset.application-validation-failed: generated Application validation failed: ApplicationSet guestbook contains applications with duplicate name: alpha;",
	} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("stderr missing %q:\n%s", want, stderr.String())
		}
	}

	strictCmd := NewRootCommand(VersionInfo{})
	strictCmd.SetArgs([]string{"get", "apps", "--path", root, "-o", "name", "--strict"})
	out.Reset()
	stderr.Reset()
	strictCmd.SetOut(&out)
	strictCmd.SetErr(&stderr)
	err := strictCmd.Execute()
	if err == nil {
		t.Fatalf("Execute(--strict) error = nil, want promoted appset warnings to fail\nstdout:\n%s\nstderr:\n%s", out.String(), stderr.String())
	}
	if !strings.Contains(stderr.String()+err.Error(), "duplicate name: alpha") {
		t.Fatalf("strict failure = %v\nstderr:\n%s\nwant the validation diagnostic", err, stderr.String())
	}
}

// With progressive syncs on, Argo CD v3.6.1 reports an invalid RollingSync
// config as an InvalidRolloutConfig condition that never blocks
// reconciliation, so --strict keeps appset.invalid-rollout-config a warning
// and does not fail on it.
func TestGetAppsStrictDoesNotFailOnInvalidRolloutConfig(t *testing.T) {
	root := t.TempDir()
	writeProgressiveSyncsOn(t, root)
	writeCLITestFile(t, filepath.Join(root, "appset.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: staged
  namespace: argocd
spec:
  goTemplate: true
  strategy:
    type: RollingSync
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
  generators:
    - list:
        elements:
          - name: alpha
            env: dev
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
`)

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"get", "apps", "--path", root, "-o", "name", "--strict"})
	var out, stderr bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&stderr)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute(--strict) error = %v\nstderr:\n%s", err, stderr.String())
	}
	if got, want := out.String(), "argocd/alpha\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	want := "warning appset.invalid-rollout-config: ApplicationSet argocd/staged has an invalid RollingSync rollout config: Steps [2] have no applications matching their criteria;"
	if !strings.Contains(stderr.String(), want) {
		t.Fatalf("stderr missing %q (strict must keep it a warning):\n%s", want, stderr.String())
	}
	if strings.Contains(stderr.String(), "error appset.") {
		t.Fatalf("stderr escalated a rollout lint under --strict:\n%s", stderr.String())
	}
}

// writeListWithApplicationSet writes one kind: List that holds an
// ApplicationSet and a static Application. List flattening gives both items
// the same document index.
func writeListWithApplicationSet(t *testing.T, root, strategy, elements string) {
	t.Helper()
	writeCLITestFile(t, filepath.Join(root, "list.yaml"), `apiVersion: v1
kind: List
items:
  - apiVersion: argoproj.io/v1alpha1
    kind: ApplicationSet
    metadata:
      name: guestbook
      namespace: argocd
    spec:
      goTemplate: true
`+strategy+`      generators:
        - list:
            elements: `+elements+`
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
  - apiVersion: argoproj.io/v1alpha1
    kind: Application
    metadata:
      name: static
      namespace: argocd
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: apps/static
        targetRevision: main
      destination:
        name: in-cluster
        namespace: default
`)
}

// An ApplicationSet that generates no Application must not hide a static
// Application that shares its kind: List. Argo CD deletes only the
// Applications an ApplicationSet owns (applicationset_controller.go:888-895).
func TestGetAppsKeepsStaticApplicationInListWithEmptyApplicationSet(t *testing.T) {
	for name, tc := range map[string]struct {
		strategy string
		elements string
		progress bool
	}{
		"no elements, progressive syncs off": {elements: "[]"},
		"empty steps, progressive syncs on": {
			strategy: "      strategy:\n        type: RollingSync\n        rollingSync:\n          steps: []\n",
			elements: "[{name: alpha}]",
			progress: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeListWithApplicationSet(t, root, tc.strategy, tc.elements)
			if tc.progress {
				writeProgressiveSyncsOn(t, root)
			}
			cmd := NewRootCommand(VersionInfo{})
			cmd.SetArgs([]string{"get", "apps", "--path", root, "-o", "name"})
			var out, stderr bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&stderr)
			if err := cmd.Execute(); err != nil {
				t.Fatalf("Execute() error = %v\nstderr:\n%s", err, stderr.String())
			}
			if got, want := out.String(), "argocd/static\n"; got != want {
				t.Fatalf("stdout = %q, want %q\nstderr:\n%s", got, want, stderr.String())
			}
		})
	}
}
