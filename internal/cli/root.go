package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

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

func placeholderCommand(use, short string) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return fmt.Errorf("%s requires a later MVP implementation task", cmd.CommandPath())
		},
	}
}

func newGetCommand() *cobra.Command {
	cmd := placeholderCommand("get", "List discovered Argo CD objects")
	cmd.AddCommand(placeholderCommand("apps", "List Applications"))
	return cmd
}

func newBuildCommand() *cobra.Command {
	cmd := placeholderCommand("build", "Render Applications")
	cmd.AddCommand(placeholderCommand("app NAME", "Render one Application"))
	cmd.AddCommand(placeholderCommand("apps", "Render all Applications"))
	return cmd
}

func newDiffCommand() *cobra.Command {
	cmd := placeholderCommand("diff", "Diff rendered desired manifests")
	cmd.AddCommand(placeholderCommand("app NAME", "Diff one Application"))
	cmd.AddCommand(placeholderCommand("apps", "Diff all Applications"))
	cmd.AddCommand(placeholderCommand("images", "Diff rendered container images"))
	return cmd
}

func newDiagCommand() *cobra.Command {
	return placeholderCommand("diag", "Report repository diagnostics")
}
