package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/app"
)

func TestRootHelp(t *testing.T) {
	cmd := NewRootCommand(VersionInfo{
		Version: "test",
		Commit:  "none",
	})
	cmd.SetArgs([]string{"--help"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	got := out.String()
	for _, want := range []string{"drydock", "diff", "build", "get", "cache", "diag", "version"} {
		if !strings.Contains(got, want) {
			t.Fatalf("help output missing %q:\n%s", want, got)
		}
	}
}

func TestNonDiffLeafHelpOmitsDiffOnlyFlags(t *testing.T) {
	for _, args := range [][]string{
		{"test", "apps", "--help"},
		{"get", "apps", "--help"},
	} {
		result := runCLI(t, args...)
		assertStdoutExcludesAll(t, result,
			"--path-orig",
			"--changed-only",
			"--strict-changed-only",
			"--exit-code",
			"--strip-attr",
			"--unified",
			"--show-ignored-fields",
			"--color",
			"--markdown-max-bytes",
			"--raw-output-file",
			"--ref-orig",
		)
	}
}

func TestSubcommandParentHelpOmitsOperationalFlags(t *testing.T) {
	result := runCLI(t, "test", "--help")
	assertStdoutExcludesAll(t, result,
		"--path",
		"--offline",
		"--output",
		"--path-orig",
		"--changed-only",
		"--exit-code",
	)
}

func TestDiffAppsHelpIncludesDiffFlags(t *testing.T) {
	result := runCLI(t, "diff", "apps", "--help")
	assertStdoutContainsAll(t, result,
		"--path-orig",
		"--changed-only",
		"--strict-changed-only",
		"--exit-code",
		"--strip-attr",
		"--unified",
		"--show-ignored-fields",
		"--color",
		"--markdown-max-bytes",
		"--raw-output-file",
		"--ref-orig",
	)
}

func TestVersionCommand(t *testing.T) {
	cmd := NewRootCommand(VersionInfo{
		Version:            "test",
		Commit:             "abc123",
		ArgoCDModule:       "github.com/argoproj/argo-cd/v3@test-version",
		GitOpsEngineModule: "github.com/argoproj/argo-cd/gitops-engine@test-version",
		HelmModule:         "helm.sh/helm/v4@test-version",
		KustomizeModule:    "sigs.k8s.io/kustomize/api@test-version",
		JsonnetModule:      "github.com/google/go-jsonnet@test-version",
		KubernetesModule:   "k8s.io/apimachinery@test-version",
	})
	cmd.SetArgs([]string{"version"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	got := out.String()
	for _, want := range []string{
		"version: test",
		"commit: abc123",
		"argocdModule: github.com/argoproj/argo-cd/v3@test-version",
		"gitopsEngineModule: github.com/argoproj/argo-cd/gitops-engine@test-version",
		"helmModule: helm.sh/helm/v4@test-version",
		"kustomizeModule: sigs.k8s.io/kustomize/api@test-version",
		"jsonnetModule: github.com/google/go-jsonnet@test-version",
		"kubernetesModule: k8s.io/apimachinery@test-version",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("version output missing %q:\n%s", want, got)
		}
	}
}

func TestVersionCommandOmitsEmptyModuleFields(t *testing.T) {
	cmd := NewRootCommand(VersionInfo{
		Version: "test",
		Commit:  "abc123",
	})
	cmd.SetArgs([]string{"version"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	got := out.String()
	for _, unwanted := range []string{"argocdModule:", "gitopsEngineModule:", "helmModule:", "kustomizeModule:", "jsonnetModule:", "kubernetesModule:"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("version output included empty module field %q:\n%s", unwanted, got)
		}
	}
}

func TestVersionCommandRejectsOperands(t *testing.T) {
	cmd := NewRootCommand(VersionInfo{
		Version: "test",
		Commit:  "abc123",
	})
	cmd.SetArgs([]string{"version", "unexpected"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	if err := cmd.Execute(); err == nil {
		t.Fatalf("Execute() error = nil, want error")
	}
}

func TestProfileWritesFileAndKeepsStructuredStdoutClean(t *testing.T) {
	root := t.TempDir()
	profileOut := filepath.Join(t.TempDir(), "profiles")
	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"--profile", "mem", "--profile-out", profileOut, "test", "apps", "--path", root, "-o", "json"})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if got, want := strings.TrimSpace(stdout.String()), "[]"; got != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
	if !strings.Contains(stderr.String(), "profile mem: wrote ") {
		t.Fatalf("stderr = %q, want profile write message", stderr.String())
	}
	matches, err := filepath.Glob(filepath.Join(profileOut, "drydock-test-apps-*.mem.pprof"))
	if err != nil {
		t.Fatalf("glob profile files: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("profile files = %v, want one mem profile", matches)
	}
	info, err := os.Stat(matches[0])
	if err != nil {
		t.Fatalf("stat profile file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("profile file mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestProfileRejectsInvalidMode(t *testing.T) {
	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"--profile", "sometimes", "version"})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), `profile must be cpu, mem, block, mutex, or trace, got "sometimes"`) {
		t.Fatalf("Execute() error = %v, want invalid profile mode", err)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}

func TestProfileStopErrorDoesNotReplaceCommandError(t *testing.T) {
	profileOut := filepath.Join(t.TempDir(), "profiles")
	originalErr := errors.New("original command failure")
	recorder := &recordingCLIOrchestrator{
		buildError: originalErr,
		buildHook: func(_ app.BuildRequest) error {
			if err := os.RemoveAll(profileOut); err != nil {
				return err
			}
			if err := os.WriteFile(profileOut, []byte("not a directory"), 0o600); err != nil {
				return err
			}
			return nil
		},
	}
	cmd := NewRootCommandWithDependencies(VersionInfo{}, Dependencies{Orchestrator: recorder})
	cmd.SetArgs([]string{"--profile", "mem", "--profile-out", profileOut, "build", "apps"})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if !errors.Is(err, originalErr) {
		t.Fatalf("Execute() error = %v, want original command error", err)
	}
	if !strings.Contains(stderr.String(), "warning profile:") {
		t.Fatalf("stderr = %q, want profile warning", stderr.String())
	}
}
