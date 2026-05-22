package cli

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

func newVersionCommand(info VersionInfo) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(cmd.OutOrStdout(),
				"version: %s\ncommit: %s\ngoVersion: %s\nargocdModule: %s\n",
				info.Version,
				info.Commit,
				runtime.Version(),
				info.ArgoCDModule,
			)
			return err
		},
	}
}
