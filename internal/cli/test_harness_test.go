package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/app"
	"github.com/sholdee/drydock/internal/remote"
	"github.com/spf13/cobra"
)

type cliCommandResult struct {
	Stdout string
	Stderr string
}

func runCLI(t *testing.T, args ...string) cliCommandResult {
	t.Helper()
	return runCLICommand(t, NewRootCommand(VersionInfo{}), args...)
}

func runCLIWithDependencies(t *testing.T, deps Dependencies, args ...string) cliCommandResult {
	t.Helper()
	return runCLICommand(t, NewRootCommandWithDependencies(VersionInfo{}, deps), args...)
}

func runCLICommand(t *testing.T, cmd *cobra.Command, args ...string) cliCommandResult {
	t.Helper()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetArgs(args)
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	return cliCommandResult{Stdout: stdout.String(), Stderr: stderr.String()}
}

func assertStdoutContainsAll(t *testing.T, result cliCommandResult, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(result.Stdout, want) {
			t.Fatalf("stdout missing %q:\nstdout:\n%s\nstderr:\n%s", want, result.Stdout, result.Stderr)
		}
	}
}

func assertStdoutExcludesAll(t *testing.T, result cliCommandResult, forbidden ...string) {
	t.Helper()
	for _, item := range forbidden {
		if strings.Contains(result.Stdout, item) {
			t.Fatalf("stdout contains forbidden %q:\nstdout:\n%s\nstderr:\n%s", item, result.Stdout, result.Stderr)
		}
	}
}

func assertStderrEmpty(t *testing.T, result cliCommandResult) {
	t.Helper()
	if result.Stderr != "" {
		t.Fatalf("stderr = %q, want empty\nstdout:\n%s", result.Stderr, result.Stdout)
	}
}

func portableSmokeFixtureRoot() string {
	return filepath.Join("..", "..", "testdata", "fixtures", "portable-smoke")
}

func homeOpsPatternFixtureRoot(parts ...string) string {
	elements := append([]string{"..", "..", "testdata", "home-ops-patterns"}, parts...)
	return filepath.Join(elements...)
}

func remoteFixtureDependencies(acquirer remote.Acquirer) Dependencies {
	return Dependencies{Orchestrator: app.Orchestrator{RemoteResourceAcquirer: acquirer}}
}
