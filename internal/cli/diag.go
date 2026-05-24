package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/home-operations/argocd-local/internal/app"
	"github.com/home-operations/argocd-local/internal/diagnostic"
	cliformat "github.com/home-operations/argocd-local/internal/format"
	"github.com/spf13/cobra"
)

type diagReport struct {
	Diagnostics []diagnostic.Diagnostic `json:"diagnostics" yaml:"diagnostics"`
}

func newDiagCommand(deps Dependencies) *cobra.Command {
	flags := defaultCommonFlags()
	cmd := &cobra.Command{
		Use:   "diag",
		Short: "Report repository diagnostics",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			output, parseErr := parseDiagOutput(flags.output)
			if parseErr != nil {
				return parseErr
			}
			repoMaps, err := parseRepoMaps(flags.repoMaps)
			if err != nil {
				return err
			}
			result, err := deps.Orchestrator.Diag(context.Background(), app.DiagRequest{
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
			})
			result.Diagnostics = diagnostic.WithStableCodes(result.Diagnostics)
			switch output {
			case "text":
				if renderErr := renderDiagnostics(cmd.ErrOrStderr(), result.Diagnostics); renderErr != nil {
					return renderErr
				}
			case string(cliformat.OutputJSON):
				if renderErr := cliformat.JSON(cmd.OutOrStdout(), diagReport{Diagnostics: result.Diagnostics}); renderErr != nil {
					return renderErr
				}
			case string(cliformat.OutputYAML):
				if renderErr := cliformat.YAML(cmd.OutOrStdout(), diagReport{Diagnostics: result.Diagnostics}); renderErr != nil {
					return renderErr
				}
			default:
				return fmt.Errorf("unsupported output %q for diag", output)
			}
			return err
		},
	}
	bindCommonFlags(cmd, &flags)
	return cmd
}

func parseDiagOutput(value string) (string, error) {
	switch strings.TrimSpace(value) {
	case "", "diff", "text":
		return "text", nil
	case string(cliformat.OutputJSON):
		return string(cliformat.OutputJSON), nil
	case string(cliformat.OutputYAML):
		return string(cliformat.OutputYAML), nil
	default:
		return "", fmt.Errorf("diag output supports text, json, or yaml, got %q", value)
	}
}

func renderDiagnostics(w io.Writer, diags []diagnostic.Diagnostic) error {
	for _, diag := range diagnostic.WithStableCodes(diags) {
		if _, err := fmt.Fprintf(w, "%s %s: %s%s\n", diag.Severity, diag.Category, diag.Message, formatDiagnosticProvenance(diag.Provenance)); err != nil {
			return err
		}
	}
	return nil
}

func formatDiagnosticProvenance(provenance diagnostic.Provenance) string {
	var parts []string
	if provenance.Path != "" {
		parts = append(parts, "path: "+provenance.Path)
	}
	if provenance.Pointer != "" {
		parts = append(parts, "pointer: "+provenance.Pointer)
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}
