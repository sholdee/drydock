package cli

import (
	"context"
	"fmt"

	"github.com/home-operations/argocd-local/internal/app"
	"github.com/spf13/cobra"
)

func newDiffCommand(deps Dependencies) *cobra.Command {
	flags := defaultCommonFlags()
	cmd := &cobra.Command{
		Use:   "diff",
		Short: "Diff rendered desired manifests",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return fmt.Errorf("%s is not wired yet for path %s", cmd.CommandPath(), flags.path)
		},
	}
	bindCommonFlags(cmd, &flags)

	appsFlags := defaultCommonFlags()
	apps := &cobra.Command{
		Use:   "apps",
		Short: "Diff all Applications",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := deps.Orchestrator.DiffApps(context.Background(), app.DiffRequest{
				LeftPath:          appsFlags.pathOrig,
				RightPath:         appsFlags.path,
				ChangedOnly:       appsFlags.changedOnly,
				StrictChangedOnly: appsFlags.strictChangedOnly,
				Strict:            appsFlags.strict,
				Unified:           appsFlags.unified,
				Offline:           appsFlags.offline,
				RefreshCharts:     appsFlags.refreshCharts,
				ChartCacheDir:     appsFlags.chartCacheDir,
			})
			if err != nil {
				return err
			}
			if err := renderDiagnostics(cmd.ErrOrStderr(), result.Diagnostics); err != nil {
				return err
			}
			for _, item := range result.Results {
				if _, err := fmt.Fprint(cmd.OutOrStdout(), item.Diff); err != nil {
					return err
				}
			}
			code := exitCode(nil, !appsFlags.exitCode, len(result.Results) > 0)
			if code != 0 {
				return ExitError{Code: code}
			}
			return nil
		},
	}
	bindCommonFlags(apps, &appsFlags)

	appFlags := defaultCommonFlags()
	app := &cobra.Command{
		Use:   "app NAME",
		Short: "Diff one Application",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("%s for %q is not wired yet for path %s", cmd.CommandPath(), args[0], appFlags.path)
		},
	}
	bindCommonFlags(app, &appFlags)

	imagesFlags := defaultCommonFlags()
	images := &cobra.Command{
		Use:   "images",
		Short: "Diff rendered container images",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return fmt.Errorf("%s is not wired yet for path %s", cmd.CommandPath(), imagesFlags.path)
		},
	}
	bindCommonFlags(images, &imagesFlags)

	cmd.AddCommand(apps, app, images)
	return cmd
}
