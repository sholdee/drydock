package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newBuildCommand() *cobra.Command {
	flags := defaultCommonFlags()
	cmd := &cobra.Command{
		Use:   "build",
		Short: "Render Applications",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return fmt.Errorf("%s orchestration requires Task 15 for path %s", cmd.CommandPath(), flags.path)
		},
	}
	bindCommonFlags(cmd, &flags)

	appsFlags := defaultCommonFlags()
	apps := &cobra.Command{
		Use:   "apps",
		Short: "Render all Applications",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return fmt.Errorf("%s orchestration requires Task 15 for path %s", cmd.CommandPath(), appsFlags.path)
		},
	}
	bindCommonFlags(apps, &appsFlags)

	appFlags := defaultCommonFlags()
	app := &cobra.Command{
		Use:   "app NAME",
		Short: "Render one Application",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("%s orchestration for %q requires Task 15 for path %s", cmd.CommandPath(), args[0], appFlags.path)
		},
	}
	bindCommonFlags(app, &appFlags)

	cmd.AddCommand(apps, app)
	return cmd
}
