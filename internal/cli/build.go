package cli

import (
	"context"
	"fmt"

	"github.com/home-operations/argocd-local/internal/app"
	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v4"
)

func newBuildCommand(deps Dependencies) *cobra.Command {
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
			result, err := deps.Orchestrator.Build(context.Background(), app.BuildRequest{
				Path:                   appsFlags.path,
				Strict:                 appsFlags.strict,
				Offline:                appsFlags.offline,
				RefreshCharts:          appsFlags.refreshCharts,
				ChartCacheDir:          appsFlags.chartCacheDir,
				RefreshRemoteResources: appsFlags.refreshRemotes,
				RemoteResourceCacheDir: appsFlags.remoteCacheDir,
			})
			if err != nil {
				return err
			}
			if err := renderDiagnostics(cmd.ErrOrStderr(), result.Diagnostics); err != nil {
				return err
			}
			for _, manifest := range result.Manifests {
				data, err := yaml.Marshal(manifest.Object.Object)
				if err != nil {
					return err
				}
				if _, err := fmt.Fprintln(cmd.OutOrStdout(), "---"); err != nil {
					return err
				}
				if _, err := cmd.OutOrStdout().Write(data); err != nil {
					return err
				}
			}
			return nil
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
