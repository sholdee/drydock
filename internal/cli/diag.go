package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/home-operations/argocd-local/internal/diagnostic"
	"github.com/spf13/cobra"
)

func newDiagCommand() *cobra.Command {
	flags := defaultCommonFlags()
	cmd := &cobra.Command{
		Use:   "diag",
		Short: "Report repository diagnostics",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return fmt.Errorf("%s is not fully wired yet for path %s", cmd.CommandPath(), flags.path)
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
