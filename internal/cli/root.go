package cli

import "github.com/spf13/cobra"

type VersionInfo struct {
	Version      string
	Commit       string
	ArgoCDModule string
}

func NewRootCommand(info VersionInfo) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "argocd-local",
		Short:         "Render and diff Argo CD GitOps repositories locally",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.AddCommand(newGetCommand())
	cmd.AddCommand(newBuildCommand())
	cmd.AddCommand(newDiffCommand())
	cmd.AddCommand(newDiagCommand())
	cmd.AddCommand(newVersionCommand(info))
	return cmd
}
