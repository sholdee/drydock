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
			repoMaps, err := parseRepoMaps(appsFlags.repoMaps)
			if err != nil {
				return err
			}
			result, err := deps.Orchestrator.Build(context.Background(), app.BuildRequest{
				Path:                   appsFlags.path,
				Strict:                 appsFlags.strict,
				Offline:                appsFlags.offline,
				RefreshCharts:          appsFlags.refreshCharts,
				ChartCacheDir:          appsFlags.chartCacheDir,
				ChartCredentials:       appsFlags.chartCredentials(),
				RepoMaps:               repoMaps,
				AllowNetwork:           appsFlags.allowNetwork,
				GitCacheDir:            appsFlags.gitCacheDir,
				RefreshGit:             appsFlags.refreshGit,
				GitCredentials:         appsFlags.gitCredentials(),
				RefreshRemoteResources: appsFlags.refreshRemotes,
				RemoteResourceCacheDir: appsFlags.remoteCacheDir,
				SkipKinds:              append([]string(nil), appsFlags.skipKinds...),
				SkipCRDs:               appsFlags.skipCRDs,
				SkipSecrets:            appsFlags.skipSecrets,
			})
			if err != nil {
				if renderErr := renderDiagnostics(cmd.ErrOrStderr(), result.Diagnostics); renderErr != nil {
					return renderErr
				}
				return err
			}
			return renderBuildResult(cmd, result)
		},
	}
	bindCommonFlags(apps, &appsFlags)

	appFlags := defaultCommonFlags()
	appCmd := &cobra.Command{
		Use:   "app NAME",
		Short: "Render one Application",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repoMaps, err := parseRepoMaps(appFlags.repoMaps)
			if err != nil {
				return err
			}
			result, err := deps.Orchestrator.BuildApp(context.Background(), app.BuildAppRequest{
				Name: args[0],
				BuildRequest: app.BuildRequest{
					Path:                   appFlags.path,
					Strict:                 appFlags.strict,
					Offline:                appFlags.offline,
					RefreshCharts:          appFlags.refreshCharts,
					ChartCacheDir:          appFlags.chartCacheDir,
					ChartCredentials:       appFlags.chartCredentials(),
					RepoMaps:               repoMaps,
					AllowNetwork:           appFlags.allowNetwork,
					GitCacheDir:            appFlags.gitCacheDir,
					RefreshGit:             appFlags.refreshGit,
					GitCredentials:         appFlags.gitCredentials(),
					RefreshRemoteResources: appFlags.refreshRemotes,
					RemoteResourceCacheDir: appFlags.remoteCacheDir,
					SkipKinds:              append([]string(nil), appFlags.skipKinds...),
					SkipCRDs:               appFlags.skipCRDs,
					SkipSecrets:            appFlags.skipSecrets,
				},
			})
			if err != nil {
				if renderErr := renderDiagnostics(cmd.ErrOrStderr(), result.Diagnostics); renderErr != nil {
					return renderErr
				}
				return err
			}
			return renderBuildResult(cmd, result)
		},
	}
	bindCommonFlags(appCmd, &appFlags)

	cmd.AddCommand(apps, appCmd)
	return cmd
}

func renderBuildResult(cmd *cobra.Command, result app.BuildResult) error {
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
}
