package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newPRCommand(info VersionInfo) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pr",
		Short: "Pull request helpers for CI",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return fmt.Errorf("%s requires a subcommand", cmd.CommandPath())
		},
	}
	cmd.AddCommand(newPRCommentCommand(info))
	return cmd
}
