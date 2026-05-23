package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newDiffCommand() *cobra.Command {
	flags := defaultCommonFlags()
	cmd := &cobra.Command{
		Use:   "diff",
		Short: "Diff rendered desired manifests",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return fmt.Errorf("diff orchestration requires Task 15 for path %s", flags.path)
		},
	}
	bindCommonFlags(cmd, &flags)

	appsFlags := defaultCommonFlags()
	apps := &cobra.Command{
		Use:   "apps",
		Short: "Diff all Applications",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return fmt.Errorf("diff apps orchestration requires Task 15 for path %s", appsFlags.path)
		},
	}
	bindCommonFlags(apps, &appsFlags)

	appFlags := defaultCommonFlags()
	app := &cobra.Command{
		Use:   "app NAME",
		Short: "Diff one Application",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return fmt.Errorf("diff app orchestration for %q requires Task 15 for path %s", args[0], appFlags.path)
		},
	}
	bindCommonFlags(app, &appFlags)

	imagesFlags := defaultCommonFlags()
	images := &cobra.Command{
		Use:   "images",
		Short: "Diff rendered container images",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return fmt.Errorf("diff images orchestration requires Task 15 for path %s", imagesFlags.path)
		},
	}
	bindCommonFlags(images, &imagesFlags)

	cmd.AddCommand(apps, app, images)
	return cmd
}
