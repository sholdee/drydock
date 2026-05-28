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
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, err := fmt.Fprintf(cmd.OutOrStdout(),
				"version: %s\ncommit: %s\ngoVersion: %s\n",
				info.Version,
				info.Commit,
				runtime.Version(),
			); err != nil {
				return err
			}
			for _, module := range []struct {
				name  string
				value string
			}{
				{name: "argocdModule", value: info.ArgoCDModule},
				{name: "gitopsEngineModule", value: info.GitOpsEngineModule},
				{name: "helmModule", value: info.HelmModule},
				{name: "kustomizeModule", value: info.KustomizeModule},
				{name: "kubernetesModule", value: info.KubernetesModule},
			} {
				if module.value == "" {
					continue
				}
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", module.name, module.value); err != nil {
					return err
				}
			}
			return nil
		},
	}
}
