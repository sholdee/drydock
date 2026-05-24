package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/home-operations/argocd-local/internal/app"
	cliformat "github.com/home-operations/argocd-local/internal/format"
	sourcepkg "github.com/home-operations/argocd-local/internal/source"
	"github.com/spf13/cobra"
)

const testOutputText = "text"

func newTestCommand(deps Dependencies) *cobra.Command {
	flags := defaultCommonFlags()
	flags.output = testOutputText
	cmd := &cobra.Command{
		Use:   "test",
		Short: "Test whether Applications render",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return fmt.Errorf("%s requires a subcommand", cmd.CommandPath())
		},
	}
	bindCommonFlags(cmd, &flags)

	appsFlags := defaultCommonFlags()
	appsFlags.output = testOutputText
	apps := &cobra.Command{
		Use:   "apps",
		Short: "Test all Applications",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			output, err := parseTestOutput(appsFlags.output)
			if err != nil {
				return err
			}
			repoMaps, err := parseRepoMaps(appsFlags.repoMaps)
			if err != nil {
				return err
			}
			buildRequest := buildRequestFromFlags(appsFlags, repoMaps)
			if strings.TrimSpace(appsFlags.selector) != "" {
				selector, err := parseApplicationSelector(appsFlags.selector)
				if err != nil {
					return err
				}
				listResult, err := deps.Orchestrator.ListApplications(context.Background(), buildRequest)
				if err != nil {
					if renderErr := renderDiagnostics(cmd.ErrOrStderr(), listResult.Diagnostics); renderErr != nil {
						return renderErr
					}
					return err
				}
				buildRequest.Applications = filterApplicationsBySelector(listResult.Applications, selector)
			}
			result, err := deps.Orchestrator.Build(context.Background(), buildRequest)
			if renderErr := renderDiagnostics(cmd.ErrOrStderr(), result.Diagnostics); renderErr != nil {
				return renderErr
			}
			if renderErr := renderTestResult(cmd, result.Statuses, output); renderErr != nil {
				return renderErr
			}
			return testCommandError(err, result.Statuses)
		},
	}
	bindCommonFlags(apps, &appsFlags)

	appFlags := defaultCommonFlags()
	appFlags.output = testOutputText
	appCmd := &cobra.Command{
		Use:   "app NAME",
		Short: "Test one Application",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			output, err := parseTestOutput(appFlags.output)
			if err != nil {
				return err
			}
			repoMaps, err := parseRepoMaps(appFlags.repoMaps)
			if err != nil {
				return err
			}
			result, err := deps.Orchestrator.BuildApp(context.Background(), app.BuildAppRequest{
				Name:         args[0],
				BuildRequest: buildRequestFromFlags(appFlags, repoMaps),
			})
			if renderErr := renderDiagnostics(cmd.ErrOrStderr(), result.Diagnostics); renderErr != nil {
				return renderErr
			}
			if renderErr := renderTestResult(cmd, result.Statuses, output); renderErr != nil {
				return renderErr
			}
			return testCommandError(err, result.Statuses)
		},
	}
	bindCommonFlags(appCmd, &appFlags)

	cmd.AddCommand(apps, appCmd)
	return cmd
}

func buildRequestFromFlags(flags commonFlags, repoMaps []sourcepkg.RepoMap) app.BuildRequest {
	return app.BuildRequest{
		Path:                         flags.path,
		Strict:                       flags.strict,
		Offline:                      flags.offline,
		RefreshCharts:                flags.refreshCharts,
		ChartCacheDir:                flags.chartCacheDir,
		ChartCredentials:             flags.chartCredentials(),
		RepoMaps:                     repoMaps,
		AllowNetwork:                 flags.allowNetwork,
		GitCacheDir:                  flags.gitCacheDir,
		RefreshGit:                   flags.refreshGit,
		GitCredentials:               flags.gitCredentials(),
		RefreshRemoteResources:       flags.refreshRemotes,
		RemoteResourceCacheDir:       flags.remoteCacheDir,
		RemoteResourceCredentials:    flags.remoteCredentials(),
		RemoteResourceGitCredentials: flags.remoteGitCredentials(),
		SkipKinds:                    append([]string(nil), flags.skipKinds...),
		SkipCRDs:                     flags.skipCRDs,
		SkipSecrets:                  flags.skipSecrets,
	}
}

func parseTestOutput(value string) (string, error) {
	output := strings.TrimSpace(value)
	switch output {
	case "", testOutputText:
		return testOutputText, nil
	case string(cliformat.OutputJSON), string(cliformat.OutputYAML):
		return output, nil
	default:
		return "", fmt.Errorf("unsupported output %q for test", value)
	}
}

func renderTestResult(cmd *cobra.Command, statuses []app.ApplicationStatus, output string) error {
	switch output {
	case testOutputText:
		for _, status := range statuses {
			if status.Message == "" {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", status.Status, applicationStatusName(status)); err != nil {
					return err
				}
				continue
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s %s %s\n", status.Status, applicationStatusName(status), status.Message); err != nil {
				return err
			}
		}
	case string(cliformat.OutputJSON):
		return cliformat.JSON(cmd.OutOrStdout(), statuses)
	case string(cliformat.OutputYAML):
		return cliformat.YAML(cmd.OutOrStdout(), statuses)
	default:
		return fmt.Errorf("unsupported output %q for test", output)
	}
	return nil
}

func applicationStatusName(status app.ApplicationStatus) string {
	if status.Namespace == "" {
		return status.Name
	}
	return status.Namespace + "/" + status.Name
}

func hasNonPassingStatus(statuses []app.ApplicationStatus) bool {
	for _, status := range statuses {
		if status.Status != app.ApplicationStatusPass {
			return true
		}
	}
	return false
}

func testCommandError(err error, statuses []app.ApplicationStatus) error {
	if err != nil && len(statuses) == 0 {
		return err
	}
	if err != nil || hasNonPassingStatus(statuses) {
		return ExitError{Code: 2}
	}
	return nil
}
