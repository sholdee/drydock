package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/home-operations/argocd-local/internal/app"
	cliformat "github.com/home-operations/argocd-local/internal/format"
	"github.com/spf13/cobra"
)

const diffOutputUnified = "diff"

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
			output, err := parseDiffOutput(appsFlags.output, "diff apps")
			if err != nil {
				return err
			}
			repoMaps, err := parseRepoMaps(appsFlags.repoMaps)
			if err != nil {
				return err
			}
			result, err := deps.Orchestrator.DiffApps(context.Background(), app.DiffRequest{
				LeftPath:               appsFlags.pathOrig,
				RightPath:              appsFlags.path,
				ChangedOnly:            appsFlags.changedOnly,
				StrictChangedOnly:      appsFlags.strictChangedOnly,
				Strict:                 appsFlags.strict,
				Unified:                appsFlags.unified,
				StripAttrs:             appsFlags.stripAttrs,
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
			})
			if err != nil {
				if renderErr := renderDiagnostics(cmd.ErrOrStderr(), result.Diagnostics); renderErr != nil {
					return renderErr
				}
				return err
			}
			return renderDiffResult(cmd, result, !appsFlags.exitCode, output)
		},
	}
	bindCommonFlags(apps, &appsFlags)

	appFlags := defaultCommonFlags()
	appCmd := &cobra.Command{
		Use:   "app NAME",
		Short: "Diff one Application",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			output, err := parseDiffOutput(appFlags.output, "diff app")
			if err != nil {
				return err
			}
			repoMaps, err := parseRepoMaps(appFlags.repoMaps)
			if err != nil {
				return err
			}
			result, err := deps.Orchestrator.DiffApp(context.Background(), app.DiffAppRequest{
				Name: args[0],
				DiffRequest: app.DiffRequest{
					LeftPath:               appFlags.pathOrig,
					RightPath:              appFlags.path,
					Strict:                 appFlags.strict,
					Unified:                appFlags.unified,
					StripAttrs:             appFlags.stripAttrs,
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
				},
			})
			if err != nil {
				if renderErr := renderDiagnostics(cmd.ErrOrStderr(), result.Diagnostics); renderErr != nil {
					return renderErr
				}
				return err
			}
			return renderDiffResult(cmd, result, !appFlags.exitCode, output)
		},
	}
	bindCommonFlags(appCmd, &appFlags)

	imagesFlags := defaultCommonFlags()
	images := &cobra.Command{
		Use:   "images",
		Short: "Diff rendered container images",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			repoMaps, err := parseRepoMaps(imagesFlags.repoMaps)
			if err != nil {
				return err
			}
			result, err := deps.Orchestrator.DiffImages(context.Background(), app.DiffRequest{
				LeftPath:               imagesFlags.pathOrig,
				RightPath:              imagesFlags.path,
				ChangedOnly:            imagesFlags.changedOnly,
				StrictChangedOnly:      imagesFlags.strictChangedOnly,
				Strict:                 imagesFlags.strict,
				Offline:                imagesFlags.offline,
				RefreshCharts:          imagesFlags.refreshCharts,
				ChartCacheDir:          imagesFlags.chartCacheDir,
				ChartCredentials:       imagesFlags.chartCredentials(),
				RepoMaps:               repoMaps,
				AllowNetwork:           imagesFlags.allowNetwork,
				GitCacheDir:            imagesFlags.gitCacheDir,
				RefreshGit:             imagesFlags.refreshGit,
				GitCredentials:         imagesFlags.gitCredentials(),
				RefreshRemoteResources: imagesFlags.refreshRemotes,
				RemoteResourceCacheDir: imagesFlags.remoteCacheDir,
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

func parseDiffOutput(value, command string) (string, error) {
	output := strings.TrimSpace(value)
	switch output {
	case "", diffOutputUnified:
		return diffOutputUnified, nil
	case string(cliformat.OutputJSON), string(cliformat.OutputYAML):
		return output, nil
	case string(cliformat.OutputName):
		return "", fmt.Errorf("name output is not supported for %s", command)
	default:
		return "", fmt.Errorf("unsupported output %q for %s", value, command)
	}
}

func renderDiffResult(cmd *cobra.Command, result app.DiffResult, disableDiffExitCode bool, output string) error {
	if err := renderDiagnostics(cmd.ErrOrStderr(), result.Diagnostics); err != nil {
		return err
	}
	switch output {
	case diffOutputUnified:
		for _, item := range result.Results {
			if _, err := fmt.Fprint(cmd.OutOrStdout(), item.Diff); err != nil {
				return err
			}
		}
	case string(cliformat.OutputJSON):
		if err := cliformat.JSON(cmd.OutOrStdout(), result.Results); err != nil {
			return err
		}
	case string(cliformat.OutputYAML):
		if err := cliformat.YAML(cmd.OutOrStdout(), result.Results); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported output %q for diff", output)
	}
	code := exitCode(nil, disableDiffExitCode, len(result.Results) > 0)
	if code != 0 {
		return ExitError{Code: code}
	}
	return nil
}
