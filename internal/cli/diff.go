package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/sholdee/drydock/internal/app"
	cliformat "github.com/sholdee/drydock/internal/format"
	"github.com/sholdee/drydock/internal/report"
	"github.com/sholdee/drydock/internal/source"
	"github.com/spf13/cobra"
)

const (
	diffColorAuto   = "auto"
	diffColorAlways = "always"
	diffColorNever  = "never"
)

func newDiffCommand(deps Dependencies) *cobra.Command {
	flags := defaultCommonFlags()
	cmd := &cobra.Command{
		Use:   "diff",
		Short: "Diff rendered desired manifests",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return fmt.Errorf("%s requires a subcommand", cmd.CommandPath())
		},
	}
	bindCommonFlags(cmd, &flags)

	cmd.AddCommand(newDiffAppsCommand(deps), newDiffAppCommand(deps), newDiffImagesCommand(deps))
	return cmd
}

func newDiffAppsCommand(deps Dependencies) *cobra.Command {
	appsFlags := defaultCommonFlags()
	appsFlags.parallelism = defaultRenderAppsParallelism()
	appsColor := diffColorAuto
	appsMarkdownMaxBytes := report.DefaultMaxBytes
	appsRawOutputFile := ""
	apps := &cobra.Command{
		Use:   "apps",
		Short: "Diff all Applications",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			output, err := parseDiffOutput(appsFlags.output, "diff apps")
			if err != nil {
				return err
			}
			if err := validateDiffMarkdownFlags(output, appsMarkdownMaxBytes); err != nil {
				return err
			}
			colorMode, err := parseDiffColorMode(appsColor)
			if err != nil {
				return err
			}
			repoMaps, err := parseRepoMaps(appsFlags.repoMaps)
			if err != nil {
				return err
			}
			result, err := deps.Orchestrator.DiffApps(context.Background(), diffRequestFromFlags(cmd, appsFlags, repoMaps))
			diagnosticColor := deps.isTerminal(cmd.ErrOrStderr())
			if err != nil {
				if renderErr := renderDiagnosticsWithColor(cmd.ErrOrStderr(), result.Diagnostics, diagnosticColor); renderErr != nil {
					return renderErr
				}
				return err
			}
			diffColor := output == diffOutputUnified && shouldColorDiffOutput(colorMode, deps, cmd.OutOrStdout())
			return renderDiffResult(cmd, result, !appsFlags.exitCode, output, diffColor, diagnosticColor, appsRawOutputFile, appsMarkdownMaxBytes)
		},
	}
	bindCommonFlags(apps, &appsFlags)
	bindDiffRefFlags(apps, &appsFlags)
	bindShowIgnoredFieldsFlag(apps, &appsFlags)
	bindDiffColorFlag(apps, &appsColor)
	bindDiffMarkdownFlags(apps, &appsMarkdownMaxBytes, &appsRawOutputFile)

	return apps
}

func newDiffAppCommand(deps Dependencies) *cobra.Command {
	appFlags := defaultCommonFlags()
	appColor := diffColorAuto
	appMarkdownMaxBytes := report.DefaultMaxBytes
	appRawOutputFile := ""
	appCmd := &cobra.Command{
		Use:   "app NAME",
		Short: "Diff one Application",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			output, err := parseDiffOutput(appFlags.output, "diff app")
			if err != nil {
				return err
			}
			if err := validateDiffMarkdownFlags(output, appMarkdownMaxBytes); err != nil {
				return err
			}
			colorMode, err := parseDiffColorMode(appColor)
			if err != nil {
				return err
			}
			repoMaps, err := parseRepoMaps(appFlags.repoMaps)
			if err != nil {
				return err
			}
			result, err := deps.Orchestrator.DiffApp(context.Background(), app.DiffAppRequest{
				Name:        args[0],
				DiffRequest: diffRequestFromFlags(cmd, appFlags, repoMaps),
			})
			diagnosticColor := deps.isTerminal(cmd.ErrOrStderr())
			if err != nil {
				if renderErr := renderDiagnosticsWithColor(cmd.ErrOrStderr(), result.Diagnostics, diagnosticColor); renderErr != nil {
					return renderErr
				}
				return err
			}
			diffColor := output == diffOutputUnified && shouldColorDiffOutput(colorMode, deps, cmd.OutOrStdout())
			return renderDiffResult(cmd, result, !appFlags.exitCode, output, diffColor, diagnosticColor, appRawOutputFile, appMarkdownMaxBytes)
		},
	}
	bindCommonFlags(appCmd, &appFlags)
	bindDiffRefFlags(appCmd, &appFlags)
	bindShowIgnoredFieldsFlag(appCmd, &appFlags)
	bindDiffColorFlag(appCmd, &appColor)
	bindDiffMarkdownFlags(appCmd, &appMarkdownMaxBytes, &appRawOutputFile)

	return appCmd
}

func newDiffImagesCommand(deps Dependencies) *cobra.Command {
	imagesFlags := defaultCommonFlags()
	imagesFlags.parallelism = defaultRenderAppsParallelism()
	imagesColor := diffColorAuto
	imagesMarkdownMaxBytes := report.DefaultMaxBytes
	images := &cobra.Command{
		Use:   "images",
		Short: "Diff rendered image references",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			output, err := parseImageDiffOutput(imagesFlags.output)
			if err != nil {
				return err
			}
			if err := validateDiffMarkdownFlags(output, imagesMarkdownMaxBytes); err != nil {
				return err
			}
			colorMode, err := parseDiffColorMode(imagesColor)
			if err != nil {
				return err
			}
			repoMaps, err := parseRepoMaps(imagesFlags.repoMaps)
			if err != nil {
				return err
			}
			result, err := deps.Orchestrator.DiffImages(context.Background(), diffRequestFromFlags(cmd, imagesFlags, repoMaps))
			diagnosticColor := deps.isTerminal(cmd.ErrOrStderr())
			if err != nil {
				if renderErr := renderDiagnosticsWithColor(cmd.ErrOrStderr(), result.Diagnostics, diagnosticColor); renderErr != nil {
					return renderErr
				}
				return err
			}
			diffColor := output == diffOutputUnified && shouldColorDiffOutput(colorMode, deps, cmd.OutOrStdout())
			return renderImageDiffResult(cmd, result, !imagesFlags.exitCode, output, diffColor, diagnosticColor, imagesMarkdownMaxBytes)
		},
	}
	bindCommonFlags(images, &imagesFlags)
	bindDiffRefFlags(images, &imagesFlags)
	bindDiffColorFlag(images, &imagesColor)
	bindDiffMarkdownMaxBytesFlag(images, &imagesMarkdownMaxBytes)

	return images
}

type imageDiffOutput struct {
	Added     []string `json:"added" yaml:"added"`
	Removed   []string `json:"removed" yaml:"removed"`
	Unchanged []string `json:"unchanged" yaml:"unchanged"`
}

func diffRequestFromFlags(cmd *cobra.Command, flags commonFlags, repoMaps []source.RepoMap) app.DiffRequest {
	return requestOptionsFromFlags(commandAwareFlags(cmd, flags), repoMaps).Diff()
}

func bindDiffColorFlag(cmd *cobra.Command, colorMode *string) {
	cmd.Flags().StringVar(colorMode, "color", *colorMode, "color diff output: auto, always, or never")
}

func bindDiffMarkdownFlags(cmd *cobra.Command, markdownMaxBytes *int, rawOutputFile *string) {
	bindDiffMarkdownMaxBytesFlag(cmd, markdownMaxBytes)
	cmd.Flags().StringVar(rawOutputFile, "raw-output-file", *rawOutputFile, "write full uncolored unified diff output to this file")
}

func bindDiffMarkdownMaxBytesFlag(cmd *cobra.Command, markdownMaxBytes *int) {
	cmd.Flags().IntVar(markdownMaxBytes, "markdown-max-bytes", *markdownMaxBytes,
		fmt.Sprintf("maximum markdown output bytes; 0 disables the limit, positive values must be at least %d", report.MinPositiveMaxByte))
}

func validateDiffMarkdownFlags(output string, markdownMaxBytes int) error {
	if output != diffOutputMarkdown {
		return nil
	}
	if markdownMaxBytes < 0 {
		return fmt.Errorf("markdown max bytes must be greater than or equal to zero")
	}
	if markdownMaxBytes > 0 && markdownMaxBytes < report.MinPositiveMaxByte {
		return fmt.Errorf("markdown max bytes must be zero or at least %d", report.MinPositiveMaxByte)
	}
	return nil
}

func parseDiffColorMode(value string) (string, error) {
	mode := strings.TrimSpace(value)
	if mode == "" {
		return diffColorAuto, nil
	}
	switch mode {
	case diffColorAuto, diffColorAlways, diffColorNever:
		return mode, nil
	default:
		return "", fmt.Errorf("color must be auto, always, or never, got %q", value)
	}
}

func shouldColorDiffOutput(mode string, deps Dependencies, w io.Writer) bool {
	switch mode {
	case diffColorAlways:
		return true
	case diffColorNever:
		return false
	default:
		return deps.isTerminal(w)
	}
}

func renderDiffResult(cmd *cobra.Command, result app.DiffResult, disableDiffExitCode bool, output string, diffColor, diagnosticColor bool, rawOutputFile string, markdownMaxBytes int) error {
	if output != diffOutputMarkdown {
		if err := renderDiagnosticsWithColor(cmd.ErrOrStderr(), result.Diagnostics, diagnosticColor); err != nil {
			return err
		}
	}
	if rawOutputFile != "" {
		if err := os.WriteFile(rawOutputFile, []byte(report.RawUnifiedDiff(result.Results)), 0o644); err != nil {
			return fmt.Errorf("write raw diff output: %w", err)
		}
	}
	switch output {
	case diffOutputUnified:
		for _, item := range result.Results {
			if _, err := fmt.Fprint(cmd.OutOrStdout(), colorizeUnifiedDiff(item.Diff, diffColor)); err != nil {
				return err
			}
		}
	case diffOutputMarkdown:
		if _, err := report.WriteDiffMarkdown(cmd.OutOrStdout(), result, report.MarkdownOptions{MaxBytes: markdownMaxBytes}); err != nil {
			return err
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

func renderImageDiffResult(cmd *cobra.Command, result app.ImageDiffResult, disableDiffExitCode bool, output string, diffColor, diagnosticColor bool, markdownMaxBytes int) error {
	if output != diffOutputMarkdown {
		if err := renderDiagnosticsWithColor(cmd.ErrOrStderr(), result.Diagnostics, diagnosticColor); err != nil {
			return err
		}
	}
	switch output {
	case diffOutputUnified:
		for _, image := range result.Removed {
			if _, err := fmt.Fprint(cmd.OutOrStdout(), colorizeDiffLine(fmt.Sprintf("- %s\n", image), diffColor)); err != nil {
				return err
			}
		}
		for _, image := range result.Added {
			if _, err := fmt.Fprint(cmd.OutOrStdout(), colorizeDiffLine(fmt.Sprintf("+ %s\n", image), diffColor)); err != nil {
				return err
			}
		}
	case string(cliformat.OutputJSON), string(cliformat.OutputYAML):
		payload := imageDiffOutput{
			Added:     cloneStringSlice(result.Added),
			Removed:   cloneStringSlice(result.Removed),
			Unchanged: cloneStringSlice(result.Unchanged),
		}
		if err := writeStructuredOutput(cmd.OutOrStdout(), output, payload); err != nil {
			return err
		}
	case string(cliformat.OutputName):
		if err := cliformat.Name(cmd.OutOrStdout(), result.Added); err != nil {
			return err
		}
	case diffOutputMarkdown:
		if _, err := report.WriteImageDiffMarkdown(cmd.OutOrStdout(), result, report.MarkdownOptions{MaxBytes: markdownMaxBytes}); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported output %q for diff images", output)
	}
	code := exitCode(nil, disableDiffExitCode, len(result.Added) > 0 || len(result.Removed) > 0)
	if code != 0 {
		return ExitError{Code: code}
	}
	return nil
}

func colorizeUnifiedDiff(text string, color bool) string {
	if !color || text == "" {
		return text
	}
	var builder strings.Builder
	builder.Grow(len(text))
	for _, line := range strings.SplitAfter(text, "\n") {
		builder.WriteString(colorizeDiffLine(line, true))
	}
	return builder.String()
}

func colorizeDiffLine(line string, color bool) string {
	if !color || line == "" {
		return line
	}
	body := line
	trailingNewline := ""
	if before, ok := strings.CutSuffix(body, "\n"); ok {
		body = before
		trailingNewline = "\n"
	}
	if body == "" {
		return line
	}
	switch {
	case strings.HasPrefix(body, "--- "), strings.HasPrefix(body, "-"):
		return "\x1b[31m" + body + "\x1b[0m" + trailingNewline
	case strings.HasPrefix(body, "+++ "), strings.HasPrefix(body, "+"):
		return "\x1b[32m" + body + "\x1b[0m" + trailingNewline
	case strings.HasPrefix(body, "@@"):
		return "\x1b[36m" + body + "\x1b[0m" + trailingNewline
	default:
		return line
	}
}

func cloneStringSlice(values []string) []string {
	return append([]string{}, values...)
}
