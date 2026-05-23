package cli

import (
	"github.com/home-operations/argocd-local/internal/app"
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
		Use:           "argocd-local",
		Short:         "Render and diff Argo CD GitOps repositories locally",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.AddCommand(newGetCommand(deps))
	cmd.AddCommand(newBuildCommand(deps))
	cmd.AddCommand(newDiffCommand(deps))
	cmd.AddCommand(newDiagCommand(deps))
	cmd.AddCommand(newVersionCommand(info))
	return cmd
}
