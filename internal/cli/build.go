package cli

import (
	"context"
	"fmt"

	"github.com/sholdee/drydock/internal/app"
	cliformat "github.com/sholdee/drydock/internal/format"
	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"
)

func newBuildCommand(info VersionInfo, deps Dependencies) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "build",
		Short: "Render Applications",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return fmt.Errorf("%s requires a subcommand", cmd.CommandPath())
		},
	}

	appsFlags := defaultCommonFlags()
	appsFlags.output = string(cliformat.OutputYAML)
	appsFlags.parallelism = defaultRenderAppsParallelism()
	appsFlags.engineFingerprint = engineFingerprintFromVersionInfo(info)
	apps := &cobra.Command{
		Use:   "apps",
		Short: "Render all Applications",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runBuildApps(cmd, deps, appsFlags)
		},
	}
	bindCommonFlags(apps, &appsFlags)

	appFlags := defaultCommonFlags()
	appFlags.output = string(cliformat.OutputYAML)
	appFlags.parallelism = defaultRenderAppsParallelism()
	appFlags.engineFingerprint = engineFingerprintFromVersionInfo(info)
	appCmd := &cobra.Command{
		Use:   "app NAME",
		Short: "Render one Application",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBuildApp(cmd, deps, appFlags, args[0])
		},
	}
	bindCommonFlags(appCmd, &appFlags)

	cmd.AddCommand(apps, appCmd)
	return cmd
}

func runBuildApps(cmd *cobra.Command, deps Dependencies, flags commonFlags) error {
	output, err := parseBuildOutput(flags.output)
	if err != nil {
		return err
	}
	repoMaps, err := parseRepoMaps(flags.repoMaps)
	if err != nil {
		return err
	}
	result, err := deps.Orchestrator.Build(context.Background(), buildRequestFromFlags(cmd, flags, repoMaps))
	if err != nil {
		if renderErr := renderDiagnostics(cmd.ErrOrStderr(), result.Diagnostics); renderErr != nil {
			return renderErr
		}
		if flags.cacheEvents {
			if eventsErr := renderCacheEventsText(cmd.ErrOrStderr(), result.CacheEvents); eventsErr != nil {
				return eventsErr
			}
		}
		return err
	}
	if err := renderBuildResult(cmd, output, result); err != nil {
		return err
	}
	if flags.cacheEvents {
		return renderCacheEventsText(cmd.ErrOrStderr(), result.CacheEvents)
	}
	return nil
}

func runBuildApp(cmd *cobra.Command, deps Dependencies, flags commonFlags, name string) error {
	output, err := parseBuildOutput(flags.output)
	if err != nil {
		return err
	}
	repoMaps, err := parseRepoMaps(flags.repoMaps)
	if err != nil {
		return err
	}
	result, err := deps.Orchestrator.BuildApp(context.Background(), app.BuildAppRequest{
		Name:         name,
		BuildRequest: buildRequestFromFlags(cmd, flags, repoMaps),
	})
	if err != nil {
		if renderErr := renderDiagnostics(cmd.ErrOrStderr(), result.Diagnostics); renderErr != nil {
			return renderErr
		}
		if flags.cacheEvents {
			if eventsErr := renderCacheEventsText(cmd.ErrOrStderr(), result.CacheEvents); eventsErr != nil {
				return eventsErr
			}
		}
		return err
	}
	if err := renderBuildResult(cmd, output, result); err != nil {
		return err
	}
	if flags.cacheEvents {
		return renderCacheEventsText(cmd.ErrOrStderr(), result.CacheEvents)
	}
	return nil
}

// manifestList is the v1 List envelope kubectl prints for multiple objects;
// one JSON document lets kubectl, kubeconform, and jq consume build output
// without splitting a stream.
type manifestList struct {
	APIVersion string           `json:"apiVersion"`
	Kind       string           `json:"kind"`
	Items      []map[string]any `json:"items"`
}

func renderBuildResult(cmd *cobra.Command, output string, result app.BuildResult) error {
	if err := renderDiagnostics(cmd.ErrOrStderr(), result.Diagnostics); err != nil {
		return err
	}
	switch output {
	case string(cliformat.OutputJSON):
		list := manifestList{APIVersion: "v1", Kind: "List", Items: make([]map[string]any, 0, len(result.Manifests))}
		for _, manifest := range result.Manifests {
			list.Items = append(list.Items, manifest.Object.Object)
		}
		return cliformat.JSON(cmd.OutOrStdout(), list)
	case string(cliformat.OutputYAML):
		for _, manifest := range result.Manifests {
			data, err := yaml.Marshal(manifest.Object.Object)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), "---"); err != nil {
				return err
			}
			if _, err := cmd.OutOrStdout().Write(data); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported output %q for build", output)
	}
}
