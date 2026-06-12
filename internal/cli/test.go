package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/app"
	cliformat "github.com/sholdee/drydock/internal/format"
	"github.com/sholdee/drydock/internal/source"
	"github.com/spf13/cobra"
)

//nolint:gocyclo // CLI flag binding and output-mode branching stay together for command readability.
func newTestCommand(info VersionInfo, deps Dependencies) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "test",
		Short: "Test whether Applications render",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return fmt.Errorf("%s requires a subcommand", cmd.CommandPath())
		},
	}

	appsFlags := defaultCommonFlags()
	appsFlags.output = testOutputText
	appsFlags.parallelism = defaultRenderAppsParallelism()
	appsFlags.engineFingerprint = engineFingerprintFromVersionInfo(info)
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
			buildRequest := buildRequestFromFlags(cmd, appsFlags, repoMaps)
			buildRequest.StatusOnly = true
			buildRequest.ValidateLuaHealth = !appsFlags.skipLuaHealth
			var liveReporter *testLiveReporter
			if output == testOutputText && deps.isTerminal(cmd.OutOrStdout()) {
				liveReporter = newTestLiveReporter(cmd.OutOrStdout(), cmd.ErrOrStderr(), deps.isTerminal(cmd.ErrOrStderr()))
				buildRequest.StatusCallback = liveReporter.Handle
			}
			var result app.BuildResult
			if strings.TrimSpace(appsFlags.selector) != "" {
				selector, parseErr := parseApplicationSelector(appsFlags.selector)
				if parseErr != nil {
					return parseErr
				}
				result, err = deps.Orchestrator.BuildSelection(context.Background(), buildRequest, func(apps []argoappv1.Application) []argoappv1.Application {
					return filterApplicationsBySelector(apps, selector)
				})
			} else {
				result, err = deps.Orchestrator.Build(context.Background(), buildRequest)
			}
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
			if appsFlags.cacheEvents {
				if eventsErr := renderCacheEventsText(cmd.ErrOrStderr(), result.CacheEvents); eventsErr != nil {
					return eventsErr
				}
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
			if renderErr := renderTestResult(cmd, result.Statuses, output, false, true); renderErr != nil {
				return renderErr
			}
			return testCommandError(err, result.Statuses)
		},
	}
	bindCommonFlags(apps, &appsFlags)
	bindLuaHealthTestFlag(apps, &appsFlags)

	appFlags := defaultCommonFlags()
	appFlags.output = testOutputText
	appFlags.parallelism = defaultRenderAppsParallelism()
	appFlags.engineFingerprint = engineFingerprintFromVersionInfo(info)
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
			buildRequest := buildRequestFromFlags(cmd, appFlags, repoMaps)
			buildRequest.StatusOnly = true
			buildRequest.ValidateLuaHealth = !appFlags.skipLuaHealth
			result, err := deps.Orchestrator.BuildApp(context.Background(), app.BuildAppRequest{
				Name:         args[0],
				BuildRequest: buildRequest,
			})
			if renderErr := renderDiagnostics(cmd.ErrOrStderr(), result.Diagnostics); renderErr != nil {
				return renderErr
			}
			if appFlags.cacheEvents {
				if eventsErr := renderCacheEventsText(cmd.ErrOrStderr(), result.CacheEvents); eventsErr != nil {
					return eventsErr
				}
			}
			if renderErr := renderTestResult(cmd, result.Statuses, output, deps.isTerminal(cmd.OutOrStdout()), false); renderErr != nil {
				return renderErr
			}
			return testCommandError(err, result.Statuses)
		},
	}
	bindCommonFlags(appCmd, &appFlags)
	bindLuaHealthTestFlag(appCmd, &appFlags)

	cmd.AddCommand(apps, appCmd)
	return cmd
}

func buildRequestFromFlags(cmd *cobra.Command, flags commonFlags, repoMaps []source.RepoMap) app.BuildRequest {
	return requestOptionsFromFlags(commandAwareFlags(cmd, flags), repoMaps).Build()
}

func renderTestResult(cmd *cobra.Command, statuses []app.ApplicationStatus, output string, color, showEmptyMessage bool) error {
	switch output {
	case testOutputText:
		return renderTestStatuses(cmd.OutOrStdout(), statuses, color, showEmptyMessage)
	case string(cliformat.OutputJSON):
		return writeStructuredOutput(cmd.OutOrStdout(), output, nonNilTestStatuses(statuses))
	case string(cliformat.OutputYAML):
		return writeStructuredOutput(cmd.OutOrStdout(), output, nonNilTestStatuses(statuses))
	default:
		return fmt.Errorf("unsupported output %q for test", output)
	}
}

func renderTestStatuses(w io.Writer, statuses []app.ApplicationStatus, color, showEmptyMessage bool) error {
	if len(statuses) == 0 {
		if !showEmptyMessage {
			return nil
		}
		_, err := fmt.Fprintln(w, "No Applications discovered.")
		return err
	}
	for _, status := range statuses {
		if err := renderApplicationStatus(w, status, color); err != nil {
			return err
		}
	}
	return nil
}

func nonNilTestStatuses(statuses []app.ApplicationStatus) []app.ApplicationStatus {
	if statuses == nil {
		return []app.ApplicationStatus{}
	}
	return statuses
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
	if len(statuses) == 0 {
		_, err := fmt.Fprintln(reporter.out, "No Applications discovered.")
		return err
	}
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
