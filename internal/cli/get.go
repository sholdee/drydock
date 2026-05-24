package cli

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/app"
	"github.com/sholdee/drydock/internal/diff"
	cliformat "github.com/sholdee/drydock/internal/format"
	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v4"
	"k8s.io/apimachinery/pkg/labels"
)

func newGetCommand(deps Dependencies) *cobra.Command {
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
	appsFlags.output = string(cliformat.OutputTable)
	apps := &cobra.Command{
		Use:   "apps",
		Short: "List Applications",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			output, err := cliformat.ParseOutput(appsFlags.output)
			if err != nil {
				return err
			}
			selector, err := parseApplicationSelector(appsFlags.selector)
			if err != nil {
				return err
			}
			result, err := deps.Orchestrator.ListApplications(context.Background(), app.BuildRequest{Path: appsFlags.path, Strict: appsFlags.strict})
			if err != nil {
				if renderErr := renderDiagnostics(cmd.ErrOrStderr(), result.Diagnostics); renderErr != nil {
					return renderErr
				}
				return err
			}
			if err := renderDiagnostics(cmd.ErrOrStderr(), result.Diagnostics); err != nil {
				return err
			}
			return renderGetApps(cmd, output, filterApplicationsBySelector(result.Applications, selector))
		},
	}
	bindCommonFlags(apps, &appsFlags)

	imagesFlags := defaultCommonFlags()
	imagesFlags.output = string(cliformat.OutputTable)
	images := &cobra.Command{
		Use:   "images",
		Short: "List rendered container images",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			output, err := cliformat.ParseOutput(imagesFlags.output)
			if err != nil {
				return err
			}
			selector, err := parseApplicationSelector(imagesFlags.selector)
			if err != nil {
				return err
			}
			repoMaps, err := parseRepoMaps(imagesFlags.repoMaps)
			if err != nil {
				return err
			}
			buildRequest := app.BuildRequest{
				Path:                         imagesFlags.path,
				Strict:                       imagesFlags.strict,
				Offline:                      imagesFlags.offline,
				RefreshCharts:                imagesFlags.refreshCharts,
				ChartCacheDir:                imagesFlags.chartCacheDir,
				ChartCredentials:             imagesFlags.chartCredentials(),
				RepoMaps:                     repoMaps,
				AllowNetwork:                 imagesFlags.allowNetwork,
				GitCacheDir:                  imagesFlags.gitCacheDir,
				RefreshGit:                   imagesFlags.refreshGit,
				GitCredentials:               imagesFlags.gitCredentials(),
				RefreshRemoteResources:       imagesFlags.refreshRemotes,
				RemoteResourceCacheDir:       imagesFlags.remoteCacheDir,
				RemoteResourceCredentials:    imagesFlags.remoteCredentials(),
				RemoteResourceGitCredentials: imagesFlags.remoteGitCredentials(),
				SkipKinds:                    append([]string(nil), imagesFlags.skipKinds...),
				SkipCRDs:                     imagesFlags.skipCRDs,
				SkipSecrets:                  imagesFlags.skipSecrets,
			}
			listResult, err := deps.Orchestrator.ListApplications(context.Background(), buildRequest)
			if err != nil {
				if renderErr := renderDiagnostics(cmd.ErrOrStderr(), listResult.Diagnostics); renderErr != nil {
					return renderErr
				}
				return err
			}
			buildRequest.Applications = filterApplicationsBySelector(listResult.Applications, selector)
			buildResult, err := deps.Orchestrator.Build(context.Background(), buildRequest)
			diagnostics := slices.Clone(listResult.Diagnostics)
			diagnostics = append(diagnostics, buildResult.Diagnostics...)
			if renderErr := renderDiagnostics(cmd.ErrOrStderr(), diagnostics); renderErr != nil {
				return renderErr
			}
			if err != nil {
				return err
			}
			return renderGetImages(cmd, output, imagesFromBuild(buildResult))
		},
	}
	bindCommonFlags(images, &imagesFlags)

	cmd.AddCommand(apps, images)
	return cmd
}

func renderGetApps(cmd *cobra.Command, output cliformat.Output, applications []argoappv1.Application) error {
	projections := applicationProjections(applications)
	switch output {
	case cliformat.OutputTable:
		return cliformat.Table(cmd.OutOrStdout(), []cliformat.Column{
			{Header: "NAMESPACE", Key: "namespace"},
			{Header: "NAME", Key: "name"},
			{Header: "PROJECT", Key: "project"},
			{Header: "DESTINATION", Key: "destination"},
			{Header: "SOURCES", Key: "sources"},
		}, applicationTableRows(projections))
	case cliformat.OutputName:
		return cliformat.Name(cmd.OutOrStdout(), applicationNames(applications))
	case cliformat.OutputJSON:
		return cliformat.JSON(cmd.OutOrStdout(), projections)
	case cliformat.OutputYAML:
		return cliformat.YAMLMulti(cmd.OutOrStdout(), anySlice(projections))
	default:
		return fmt.Errorf("unsupported output %q", output)
	}
}

func renderGetImages(cmd *cobra.Command, output cliformat.Output, images []string) error {
	projections := imageProjections(images)
	switch output {
	case cliformat.OutputTable:
		return cliformat.Table(cmd.OutOrStdout(), []cliformat.Column{{Header: "IMAGE", Key: "image"}}, imageTableRows(images))
	case cliformat.OutputName:
		return cliformat.Name(cmd.OutOrStdout(), images)
	case cliformat.OutputJSON:
		return cliformat.JSON(cmd.OutOrStdout(), projections)
	case cliformat.OutputYAML:
		return cliformat.YAMLMulti(cmd.OutOrStdout(), anySlice(projections))
	default:
		return fmt.Errorf("unsupported output %q", output)
	}
}

func parseApplicationSelector(raw string) (labels.Selector, error) {
	if strings.TrimSpace(raw) == "" {
		return labels.Everything(), nil
	}
	selector, err := labels.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid selector %q: %w", raw, err)
	}
	return selector, nil
}

func filterApplicationsBySelector(applications []argoappv1.Application, selector labels.Selector) []argoappv1.Application {
	if selector == nil {
		selector = labels.Everything()
	}
	filtered := make([]argoappv1.Application, 0, len(applications))
	for _, application := range applications {
		if selector.Matches(labels.Set(application.Labels)) {
			filtered = append(filtered, application)
		}
	}
	return sortApplications(filtered)
}

func sortApplications(applications []argoappv1.Application) []argoappv1.Application {
	out := append([]argoappv1.Application(nil), applications...)
	sort.Slice(out, func(i, j int) bool {
		left := qualifiedApplicationName(out[i])
		right := qualifiedApplicationName(out[j])
		return left < right
	})
	return out
}

func applicationProjections(applications []argoappv1.Application) []map[string]any {
	sorted := sortApplications(applications)
	out := make([]map[string]any, 0, len(sorted))
	for _, application := range sorted {
		out = append(out, map[string]any{
			"namespace": application.Namespace,
			"name":      application.Name,
			"project":   application.Spec.Project,
			"destination": map[string]string{
				"name":      application.Spec.Destination.Name,
				"server":    application.Spec.Destination.Server,
				"namespace": application.Spec.Destination.Namespace,
			},
			"sources": sourceSummary(application),
		})
	}
	return out
}

func applicationTableRows(applications []map[string]any) []map[string]string {
	rows := make([]map[string]string, 0, len(applications))
	for _, application := range applications {
		rows = append(rows, map[string]string{
			"namespace":   stringValue(application["namespace"]),
			"name":        stringValue(application["name"]),
			"project":     stringValue(application["project"]),
			"destination": destinationTableValue(application["destination"]),
			"sources":     sourcesTableValue(application["sources"]),
		})
	}
	return rows
}

func applicationNames(applications []argoappv1.Application) []string {
	sorted := sortApplications(applications)
	names := make([]string, 0, len(sorted))
	for _, application := range sorted {
		names = append(names, qualifiedApplicationName(application))
	}
	return names
}

func qualifiedApplicationName(application argoappv1.Application) string {
	if application.Namespace == "" {
		return application.Name
	}
	return application.Namespace + "/" + application.Name
}

func sourceSummary(application argoappv1.Application) []map[string]string {
	sources := applicationSources(application)
	out := make([]map[string]string, 0, len(sources))
	for _, source := range sources {
		summary := map[string]string{
			"repoURL":        source.RepoURL,
			"targetRevision": source.TargetRevision,
		}
		if source.Path != "" {
			summary["path"] = source.Path
		}
		if source.Chart != "" {
			summary["chart"] = source.Chart
		}
		if source.Ref != "" {
			summary["ref"] = source.Ref
		}
		out = append(out, summary)
	}
	return out
}

func applicationSources(application argoappv1.Application) []argoappv1.ApplicationSource {
	if len(application.Spec.Sources) > 0 {
		return append([]argoappv1.ApplicationSource(nil), application.Spec.Sources...)
	}
	if application.Spec.Source != nil {
		return []argoappv1.ApplicationSource{*application.Spec.Source}
	}
	return nil
}

func destinationTableValue(value any) string {
	destination, ok := value.(map[string]string)
	if !ok {
		return ""
	}
	target := destination["name"]
	if target == "" {
		target = destination["server"]
	}
	if namespace := destination["namespace"]; namespace != "" {
		if target == "" {
			return namespace
		}
		return target + "/" + namespace
	}
	return target
}

func sourcesTableValue(value any) string {
	sources, ok := value.([]map[string]string)
	if !ok {
		return ""
	}
	parts := make([]string, 0, len(sources))
	for _, source := range sources {
		part := source["path"]
		if part == "" {
			part = source["chart"]
		}
		if part == "" {
			part = source["ref"]
		}
		if revision := source["targetRevision"]; revision != "" {
			part += "@" + revision
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, ",")
}

func stringValue(value any) string {
	out, _ := value.(string)
	return out
}

func imageProjections(images []string) []map[string]any {
	out := make([]map[string]any, 0, len(images))
	for _, image := range images {
		out = append(out, map[string]any{"image": image})
	}
	return out
}

func imageTableRows(images []string) []map[string]string {
	rows := make([]map[string]string, 0, len(images))
	for _, image := range images {
		rows = append(rows, map[string]string{"image": image})
	}
	return rows
}

func imagesFromBuild(result app.BuildResult) []string {
	docs := make([]diff.Document, 0, len(result.Manifests))
	for _, manifest := range result.Manifests {
		if manifest.Object == nil {
			continue
		}
		body, err := yaml.Marshal(manifest.Object.Object)
		if err != nil {
			continue
		}
		docs = append(docs, diff.Document{Body: string(body)})
	}
	return diff.ExtractImages(docs)
}

func anySlice[T any](values []T) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}
