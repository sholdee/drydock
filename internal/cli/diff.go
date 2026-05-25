package cli

import (
	"context"
	"fmt"

	"github.com/sholdee/drydock/internal/app"
	cliformat "github.com/sholdee/drydock/internal/format"
	"github.com/sholdee/drydock/internal/source"
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
			output, err := parseDiffOutput(appsFlags.output, "diff apps")
			if err != nil {
				return err
			}
			repoMaps, err := parseRepoMaps(appsFlags.repoMaps)
			if err != nil {
				return err
			}
			result, err := deps.Orchestrator.DiffApps(context.Background(), diffRequestFromFlags(appsFlags, repoMaps))
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
				Name:        args[0],
				DiffRequest: diffRequestFromFlags(appFlags, repoMaps),
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
			result, err := deps.Orchestrator.DiffImages(context.Background(), diffRequestFromFlags(imagesFlags, repoMaps))
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

func diffRequestFromFlags(flags commonFlags, repoMaps []source.RepoMap) app.DiffRequest {
	return requestOptionsFromFlags(flags, repoMaps).Diff()
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
		if err := writeStructuredOutput(cmd.OutOrStdout(), output, result.Results); err != nil {
			return err
		}
	case string(cliformat.OutputYAML):
		if err := writeStructuredOutput(cmd.OutOrStdout(), output, result.Results); err != nil {
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
