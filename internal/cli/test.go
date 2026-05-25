package cli

import (
	"context"
	"fmt"
	"runtime"
	"strings"

	"github.com/sholdee/drydock/internal/app"
	cliformat "github.com/sholdee/drydock/internal/format"
	"github.com/sholdee/drydock/internal/source"
	"github.com/spf13/cobra"
)

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
	appsFlags.parallelism = defaultTestAppsParallelism()
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

func defaultTestAppsParallelism() int {
	value := runtime.GOMAXPROCS(0)
	if value < 1 {
		return 1
	}
	return value
}

func buildRequestFromFlags(flags commonFlags, repoMaps []source.RepoMap) app.BuildRequest {
	return requestOptionsFromFlags(flags, repoMaps).Build()
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
		return writeStructuredOutput(cmd.OutOrStdout(), output, statuses)
	case string(cliformat.OutputYAML):
		return writeStructuredOutput(cmd.OutOrStdout(), output, statuses)
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
