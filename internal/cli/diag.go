package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/home-operations/argocd-local/internal/app"
	"github.com/home-operations/argocd-local/internal/diagnostic"
	"github.com/spf13/cobra"
)

func newDiagCommand(deps Dependencies) *cobra.Command {
	flags := defaultCommonFlags()
	cmd := &cobra.Command{
		Use:   "diag",
		Short: "Report repository diagnostics",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
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
			if renderErr := renderDiagnostics(cmd.ErrOrStderr(), result.Diagnostics); renderErr != nil {
				return renderErr
			}
			return err
		},
	}
	bindCommonFlags(cmd, &flags)
	return cmd
}

func renderDiagnostics(w io.Writer, diags []diagnostic.Diagnostic) error {
	for _, diag := range diags {
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
