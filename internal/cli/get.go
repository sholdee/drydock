package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newGetCommand() *cobra.Command {
	flags := defaultCommonFlags()
	cmd := &cobra.Command{
		Use:   "get",
		Short: "List discovered Argo CD objects",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return fmt.Errorf("%s is not fully wired yet for path %s", cmd.CommandPath(), flags.path)
		},
	}
	bindCommonFlags(cmd, &flags)

	appsFlags := defaultCommonFlags()
	apps := &cobra.Command{
		Use:   "apps",
		Short: "List Applications",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return fmt.Errorf("%s is not fully wired yet for path %s", cmd.CommandPath(), appsFlags.path)
		},
	}
	bindCommonFlags(apps, &appsFlags)

	cmd.AddCommand(apps)
	return cmd
}
