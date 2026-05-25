package cli

import (
	"context"
	"io"

	"github.com/sholdee/drydock/internal/app"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

type VersionInfo struct {
	Version      string
	Commit       string
	ArgoCDModule string
}

type Dependencies struct {
	Orchestrator Orchestrator
	IsTerminal   func(io.Writer) bool
}

type Orchestrator interface {
	Build(context.Context, app.BuildRequest) (app.BuildResult, error)
	BuildApp(context.Context, app.BuildAppRequest) (app.BuildResult, error)
	ListApplications(context.Context, app.BuildRequest) (app.BuildResult, error)
	DiffApps(context.Context, app.DiffRequest) (app.DiffResult, error)
	DiffApp(context.Context, app.DiffAppRequest) (app.DiffResult, error)
	DiffImages(context.Context, app.DiffRequest) (app.ImageDiffResult, error)
	Diag(context.Context, app.DiagRequest) (app.DiagResult, error)
}

func defaultDependencies() Dependencies {
	return Dependencies{
		Orchestrator: app.Orchestrator{},
		IsTerminal:   isTerminalWriter,
	}
}

func NewRootCommand(info VersionInfo) *cobra.Command {
	return NewRootCommandWithDependencies(info, defaultDependencies())
}

func NewRootCommandWithDependencies(info VersionInfo, deps Dependencies) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "drydock",
		Short:         "Inspect your Argo CD fleet without getting wet",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.AddCommand(newGetCommand(deps))
	cmd.AddCommand(newBuildCommand(deps))
	cmd.AddCommand(newTestCommand(deps))
	cmd.AddCommand(newDiffCommand(deps))
	cmd.AddCommand(newCacheCommand())
	cmd.AddCommand(newDiagCommand(deps))
	cmd.AddCommand(newVersionCommand(info))
	return cmd
}

func (deps Dependencies) isTerminal(w io.Writer) bool {
	if deps.IsTerminal != nil {
		return deps.IsTerminal(w)
	}
	return isTerminalWriter(w)
}

func isTerminalWriter(w io.Writer) bool {
	file, ok := w.(interface{ Fd() uintptr })
	return ok && term.IsTerminal(int(file.Fd()))
}
