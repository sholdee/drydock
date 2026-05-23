package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/home-operations/argocd-local/internal/app"
	"github.com/home-operations/argocd-local/internal/chart"
)

type fixtureChartAcquirer struct {
	root string
}

func (acquirer fixtureChartAcquirer) Acquire(_ context.Context, request chart.Request, _ chart.Options) (chart.Result, error) {
	return chart.Result{
		ChartDir:   filepath.Join(acquirer.root, "charts", request.Name, request.Version),
		Repository: request.Repository,
		Name:       request.Name,
		Version:    request.Version,
		Kind:       request.Kind,
	}, nil
}

func TestDiffAppsSimulatedRenovateChartBump(t *testing.T) {
	fixtureRoot := filepath.Join("..", "..", "testdata", "renovate-diff")
	cmd := NewRootCommandWithDependencies(VersionInfo{}, Dependencies{
		Orchestrator: app.Orchestrator{
			ChartAcquirer: fixtureChartAcquirer{root: fixtureRoot},
		},
	})
	cmd.SetArgs([]string{
		"diff", "apps",
		"--path-orig", filepath.Join(fixtureRoot, "baseline"),
		"--path", filepath.Join(fixtureRoot, "current"),
		"--exit-code=false",
	})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}

	for _, want := range []string{
		"Application: argocd/renovate",
		"renovate-operator",
		"-          image: ghcr.io/example/renovate-operator:4.8.0",
		"+          image: ghcr.io/example/renovate-operator:4.8.1",
		"RENOVATE_LOG_LEVEL",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout missing %q:\nstdout:\n%s\nstderr:\n%s", want, stdout.String(), stderr.String())
		}
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestDiffImagesSimulatedRenovateChartBump(t *testing.T) {
	fixtureRoot := filepath.Join("..", "..", "testdata", "renovate-diff")
	cmd := NewRootCommandWithDependencies(VersionInfo{}, Dependencies{
		Orchestrator: app.Orchestrator{
			ChartAcquirer: fixtureChartAcquirer{root: fixtureRoot},
		},
	})
	cmd.SetArgs([]string{
		"diff", "images",
		"--path-orig", filepath.Join(fixtureRoot, "baseline"),
		"--path", filepath.Join(fixtureRoot, "current"),
		"--exit-code=false",
	})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}

	for _, want := range []string{
		"- ghcr.io/example/renovate-operator:4.8.0",
		"+ ghcr.io/example/renovate-operator:4.8.1",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout missing %q:\nstdout:\n%s\nstderr:\n%s", want, stdout.String(), stderr.String())
		}
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}
