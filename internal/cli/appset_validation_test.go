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
  strategy:
    type: RollingSync
    rollingSync:
      steps: []
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

func TestGetAppsReportsGeneratedApplicationValidationAndRolloutWarnings(t *testing.T) {
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
		"warning appset.rollout-no-steps: ApplicationSet argocd/guestbook uses the RollingSync strategy with an empty steps list: No steps defined for rollout;",
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

// Argo CD v3.6.1 evaluates RollingSync steps only with progressive syncs
// enabled, and InvalidRolloutConfig never blocks reconciliation, so --strict
// keeps appset.invalid-rollout-config a warning and does not fail on it.
func TestGetAppsStrictDoesNotFailOnInvalidRolloutConfig(t *testing.T) {
	root := t.TempDir()
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
