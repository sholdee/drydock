package cli

import (
	"context"
	"fmt"

	"github.com/home-operations/argocd-local/internal/app"
	"github.com/spf13/cobra"
)

func newGetCommand() *cobra.Command {
	flags := defaultCommonFlags()
	cmd := &cobra.Command{
		Use:   "get",
		Short: "List discovered Argo CD objects",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return fmt.Errorf("%s is not fully wired yet for path %s", cmd.CommandPath(), flags.path)
		},
	}
	bindCommonFlags(cmd, &flags)

	appsFlags := defaultCommonFlags()
	apps := &cobra.Command{
		Use:   "apps",
		Short: "List Applications",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := app.Orchestrator{}.ListApplications(context.Background(), app.BuildRequest{Path: appsFlags.path, Strict: appsFlags.strict})
			if err != nil {
				return err
			}
			for _, application := range result.Applications {
				if _, err := fmt.Fprintln(cmd.OutOrStdout(), application.Name); err != nil {
					return err
				}
			}
			return nil
		},
	}
	bindCommonFlags(apps, &appsFlags)

	cmd.AddCommand(apps)
	return cmd
}
