package cli

import (
	"github.com/sholdee/drydock/internal/app"
	"github.com/spf13/cobra"
)

type VersionInfo struct {
	Version      string
	Commit       string
	ArgoCDModule string
}

type Dependencies struct {
	Orchestrator app.Orchestrator
}

func defaultDependencies() Dependencies {
	return Dependencies{Orchestrator: app.Orchestrator{}}
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
	cmd.AddCommand(newDiagCommand(deps))
	cmd.AddCommand(newVersionCommand(info))
	return cmd
}
