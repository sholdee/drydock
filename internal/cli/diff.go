package cli

import (
	"context"
	"fmt"

	"github.com/home-operations/argocd-local/internal/app"
	"github.com/spf13/cobra"
)

//nolint:gocyclo // Cobra wiring keeps diff subcommands and shared flag handling together.
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
				if renderErr := renderDiagnostics(cmd.ErrOrStderr(), result.Diagnostics); renderErr != nil {
					return renderErr
				}
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
	appCmd := &cobra.Command{
		Use:   "app NAME",
		Short: "Diff one Application",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("%s for %q is not wired yet for path %s", cmd.CommandPath(), args[0], appFlags.path)
		},
	}
	bindCommonFlags(appCmd, &appFlags)

	imagesFlags := defaultCommonFlags()
	images := &cobra.Command{
		Use:   "images",
		Short: "Diff rendered container images",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := deps.Orchestrator.DiffImages(context.Background(), app.DiffRequest{
				LeftPath:          imagesFlags.pathOrig,
				RightPath:         imagesFlags.path,
				ChangedOnly:       imagesFlags.changedOnly,
				StrictChangedOnly: imagesFlags.strictChangedOnly,
				Strict:            imagesFlags.strict,
				Offline:           imagesFlags.offline,
				RefreshCharts:     imagesFlags.refreshCharts,
				ChartCacheDir:     imagesFlags.chartCacheDir,
			})
			if err != nil {
				if renderErr := renderDiagnostics(cmd.ErrOrStderr(), result.Diagnostics); renderErr != nil {
					return renderErr
				}
				return err
			}
			if err := renderDiagnostics(cmd.ErrOrStderr(), result.Diagnostics); err != nil {
				return err
			}
			for _, image := range result.Removed {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "- %s\n", image); err != nil {
					return err
				}
			}
			for _, image := range result.Added {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "+ %s\n", image); err != nil {
					return err
				}
			}
			code := exitCode(nil, !imagesFlags.exitCode, len(result.Added) > 0 || len(result.Removed) > 0)
			if code != 0 {
				return ExitError{Code: code}
			}
			return nil
		},
	}
	bindCommonFlags(images, &imagesFlags)

	cmd.AddCommand(apps, appCmd, images)
	return cmd
}
