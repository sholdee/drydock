package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"time"

	"github.com/sholdee/drydock/internal/app"
	cliformat "github.com/sholdee/drydock/internal/format"
	"github.com/sholdee/drydock/internal/source"
	"github.com/spf13/cobra"
)

const maxDefaultTestAppsParallelism = 8

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
			buildRequest.StatusOnly = true
			var liveReporter *testLiveReporter
			if output == testOutputText && deps.isTerminal(cmd.OutOrStdout()) {
				liveReporter = newTestLiveReporter(cmd.OutOrStdout(), cmd.ErrOrStderr(), deps.isTerminal(cmd.ErrOrStderr()))
				buildRequest.StatusCallback = liveReporter.Handle
			}
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
			if liveReporter != nil {
				var liveErr liveTestOutputError
				if errors.As(err, &liveErr) {
					return err
				}
				if renderErr := liveReporter.Clear(); renderErr != nil {
					return renderErr
				}
			}
			if renderErr := renderDiagnostics(cmd.ErrOrStderr(), result.Diagnostics); renderErr != nil {
				return renderErr
			}
			if liveReporter != nil {
				if renderErr := liveReporter.RenderMissingStatuses(result.Statuses); renderErr != nil {
					return renderErr
				}
				if renderErr := liveReporter.Summary(result.Statuses); renderErr != nil {
					return renderErr
				}
				return testCommandError(err, result.Statuses)
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
			buildRequest := buildRequestFromFlags(appFlags, repoMaps)
			buildRequest.StatusOnly = true
			result, err := deps.Orchestrator.BuildApp(context.Background(), app.BuildAppRequest{
				Name:         args[0],
				BuildRequest: buildRequest,
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
	if value > maxDefaultTestAppsParallelism {
		return maxDefaultTestAppsParallelism
	}
	return value
}

func buildRequestFromFlags(flags commonFlags, repoMaps []source.RepoMap) app.BuildRequest {
	return requestOptionsFromFlags(flags, repoMaps).Build()
}

func renderTestResult(cmd *cobra.Command, statuses []app.ApplicationStatus, output string) error {
	switch output {
	case testOutputText:
		return renderTestStatuses(cmd.OutOrStdout(), statuses, false)
	case string(cliformat.OutputJSON):
		return writeStructuredOutput(cmd.OutOrStdout(), output, statuses)
	case string(cliformat.OutputYAML):
		return writeStructuredOutput(cmd.OutOrStdout(), output, statuses)
	default:
		return fmt.Errorf("unsupported output %q for test", output)
	}
	return nil
}

func renderTestStatuses(w io.Writer, statuses []app.ApplicationStatus, color bool) error {
	for _, status := range statuses {
		if err := renderApplicationStatus(w, status, color); err != nil {
			return err
		}
	}
	return nil
}

func renderApplicationStatus(w io.Writer, status app.ApplicationStatus, color bool) error {
	label := status.Status
	if color {
		label = colorizeTestStatus(label)
	}
	if status.Message == "" {
		_, err := fmt.Fprintf(w, "%s %s\n", label, applicationStatusName(status))
		return err
	}
	_, err := fmt.Fprintf(w, "%s %s %s\n", label, applicationStatusName(status), status.Message)
	return err
}

func colorizeTestStatus(status string) string {
	switch status {
	case app.ApplicationStatusPass:
		return "\x1b[32m" + status + "\x1b[0m"
	case app.ApplicationStatusFail:
		return "\x1b[31m" + status + "\x1b[0m"
	case app.ApplicationStatusSkipped:
		return "\x1b[33m" + status + "\x1b[0m"
	default:
		return status
	}
}

type testLiveReporter struct {
	out      io.Writer
	errOut   io.Writer
	started  time.Time
	emitted  map[string]struct{}
	progress bool
}

func newTestLiveReporter(out, errOut io.Writer, progress bool) *testLiveReporter {
	return &testLiveReporter{
		out:      out,
		errOut:   errOut,
		started:  time.Now(),
		emitted:  map[string]struct{}{},
		progress: progress,
	}
}

func (reporter *testLiveReporter) Handle(event app.ApplicationStatusEvent) error {
	if err := reporter.Clear(); err != nil {
		return err
	}
	if err := renderApplicationStatus(reporter.out, event.Status, true); err != nil {
		return liveTestOutputError{err: fmt.Errorf("write live test status: %w", err)}
	}
	reporter.emitted[applicationStatusKey(event.Status)] = struct{}{}
	if !reporter.progress {
		return nil
	}
	if _, err := fmt.Fprintf(reporter.errOut, "\rTesting apps %d/%d", event.Completed, event.Total); err != nil {
		return liveTestOutputError{err: fmt.Errorf("write live test progress: %w", err)}
	}
	return nil
}

func (reporter *testLiveReporter) Clear() error {
	if !reporter.progress {
		return nil
	}
	if _, err := fmt.Fprint(reporter.errOut, "\r\x1b[2K"); err != nil {
		return liveTestOutputError{err: fmt.Errorf("clear live test progress: %w", err)}
	}
	return nil
}

func (reporter *testLiveReporter) RenderMissingStatuses(statuses []app.ApplicationStatus) error {
	for _, status := range statuses {
		if _, ok := reporter.emitted[applicationStatusKey(status)]; ok {
			continue
		}
		if err := renderApplicationStatus(reporter.out, status, true); err != nil {
			return err
		}
	}
	return nil
}

func (reporter *testLiveReporter) Summary(statuses []app.ApplicationStatus) error {
	counts := summarizeTestStatuses(statuses)
	if _, err := fmt.Fprintf(reporter.out, "%d applications: %d passed, %d failed, %d skipped in %s\n",
		len(statuses), counts.passed, counts.failed, counts.skipped, formatTestDuration(time.Since(reporter.started))); err != nil {
		return err
	}
	return nil
}

type testSummaryCounts struct {
	passed  int
	failed  int
	skipped int
}

func summarizeTestStatuses(statuses []app.ApplicationStatus) testSummaryCounts {
	var counts testSummaryCounts
	for _, status := range statuses {
		switch status.Status {
		case app.ApplicationStatusPass:
			counts.passed++
		case app.ApplicationStatusFail:
			counts.failed++
		case app.ApplicationStatusSkipped:
			counts.skipped++
		}
	}
	return counts
}

func formatTestDuration(duration time.Duration) string {
	if duration < time.Second {
		return duration.Round(time.Millisecond).String()
	}
	return duration.Round(10 * time.Millisecond).String()
}

type liveTestOutputError struct {
	err error
}

func (e liveTestOutputError) Error() string {
	return e.err.Error()
}

func (e liveTestOutputError) Unwrap() error {
	return e.err
}

func applicationStatusName(status app.ApplicationStatus) string {
	if status.Namespace == "" {
		return status.Name
	}
	return status.Namespace + "/" + status.Name
}

func applicationStatusKey(status app.ApplicationStatus) string {
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
