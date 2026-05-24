package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/manifest"
	"github.com/sholdee/drydock/internal/render"
	sourcepkg "github.com/sholdee/drydock/internal/source"
	"go.yaml.in/yaml/v4"
)

type RenderResult struct {
	Manifests   []render.Manifest
	Diagnostics []diagnostic.Diagnostic
}

type StaticRenderers map[string][]render.Manifest

func (s StaticRenderers) RenderSource(_ context.Context, source render.ResolvedSource, _ render.RenderOptions) ([]render.Manifest, []diagnostic.Diagnostic, error) {
	return s[source.Path], nil, nil
}

func RenderApplication(ctx context.Context, application argoappv1.Application, provider render.Provider) (RenderResult, error) {
	plan, err := Plan(application)
	if err != nil {
		return RenderResult{}, err
	}

	byID := map[manifest.Identity]int{}
	var result RenderResult
	for _, sourcePlan := range plan.Sources {
		if sourcePlan.RefOnly {
			continue
		}

		opts, err := renderOptions(application, sourcePlan.Source)
		if err != nil {
			return result, fmt.Errorf("%s: %w", renderSourceContext(application, sourcePlan), err)
		}
		refRoots, refSources, err := renderRefsForSource(plan, sourcePlan, opts.ValueFiles)
		if err != nil {
			return result, fmt.Errorf("%s: %w", renderSourceContext(application, sourcePlan), err)
		}
		opts.RefRoots = mergeRefRoots(opts.RefRoots, refRoots)
		opts.RefSources = refSources
		manifests, diags, err := provider.RenderSource(ctx, render.ResolvedSource{
			Path:           sourcePlan.Source.Path,
			Chart:          sourcePlan.Source.Chart,
			RepoURL:        sourcePlan.Source.RepoURL,
			TargetRevision: sourcePlan.Source.TargetRevision,
		}, opts)
		result.Diagnostics = append(result.Diagnostics, sourceDiagnostics(application, sourcePlan, diags)...)
		if err != nil {
			return result, fmt.Errorf("%s: %w", renderSourceContext(application, sourcePlan), err)
		}

		for _, rendered := range manifests {
			if rendered.Object != nil {
				rendered.Object = rendered.Object.DeepCopy()
			}
			rendered.SourceIndex = sourcePlan.Index
			rendered.SourceName = sourcePlan.Name
			ApplyDestinationNamespace(application, rendered.Object)

			id := manifest.IdentityOf(rendered.Object)
			if existing, ok := byID[id]; ok {
				result.Manifests[existing] = rendered
				result.Diagnostics = append(result.Diagnostics, diagnostic.Diagnostic{
					Severity: diagnostic.SeverityWarning,
					Category: "repeated-resource",
					Message:  fmt.Sprintf("resource %s repeated in Application %s; later source wins", id.String(), application.Name),
				})
				continue
			}

			byID[id] = len(result.Manifests)
			result.Manifests = append(result.Manifests, rendered)
		}
	}
	return result, nil
}

func renderOptions(application argoappv1.Application, source argoappv1.ApplicationSource) (render.RenderOptions, error) {
	opts := render.RenderOptions{
		AppName:      application.Name,
		AppNamespace: application.Namespace,
		Project:      application.Spec.Project,
		Namespace:    application.Spec.Destination.Namespace,
	}
	if source.Plugin != nil {
		plugin := source.Plugin.DeepCopy()
		opts.Plugin = &render.PluginConfig{
			Name:       plugin.Name,
			Env:        append(argoappv1.Env(nil), plugin.Env...),
			Parameters: plugin.Parameters.DeepCopy(),
		}
	}
	if source.Helm == nil {
		return opts, nil
	}

	opts.ReleaseName = source.Helm.ReleaseName
	if source.Helm.Namespace != "" {
		opts.Namespace = source.Helm.Namespace
	}
	opts.KubeVersion = source.Helm.KubeVersion
	opts.APIVersions = append(opts.APIVersions, source.Helm.APIVersions...)
	opts.ValueFiles = append(opts.ValueFiles, source.Helm.ValueFiles...)
	opts.IgnoreMissingValueFiles = source.Helm.IgnoreMissingValueFiles
	opts.IncludeCRDsSet = true
	opts.IncludeCRDs = !source.Helm.SkipCrds
	opts.SkipTests = source.Helm.SkipTests
	valuesObject, err := helmValues(source.Helm)
	if err != nil {
		return render.RenderOptions{}, err
	}
	opts.ValuesObject = valuesObject
	return opts, nil
}

func helmValues(helm *argoappv1.ApplicationSourceHelm) (map[string]any, error) {
	valuesObject, ok, err := helmValuesObject(helm)
	if err != nil {
		return nil, err
	}
	if ok {
		return valuesObject, nil
	}
	return helmValuesString(helm.Values)
}

func helmValuesString(raw string) (map[string]any, error) {
	if strings.TrimSpace(raw) == "" {
		return map[string]any{}, nil
	}
	var values map[string]any
	if err := yaml.Unmarshal([]byte(raw), &values); err != nil {
		return nil, fmt.Errorf("helm values must be a YAML mapping: %w", err)
	}
	if values == nil {
		return nil, fmt.Errorf("helm values must be a YAML mapping")
	}
	return values, nil
}

func helmValuesObject(helm *argoappv1.ApplicationSourceHelm) (map[string]any, bool, error) {
	if helm.ValuesObject == nil || len(helm.ValuesObject.Raw) == 0 {
		return map[string]any{}, false, nil
	}

	var values map[string]any
	if err := json.Unmarshal(helm.ValuesObject.Raw, &values); err != nil {
		return nil, false, fmt.Errorf("decode helm valuesObject: %w", err)
	}
	return values, true, nil
}

func renderRefsForSource(plan PlanResult, sourcePlan SourcePlan, valueFiles []string) (map[string]string, map[string]render.ResolvedSource, error) {
	if len(valueFiles) == 0 {
		return map[string]string{}, map[string]render.ResolvedSource{}, nil
	}

	refRoots := map[string]string{}
	refSources := map[string]render.ResolvedSource{}
	for _, valueFile := range valueFiles {
		refKey, ok, err := helmValueFileRefKey(valueFile)
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			continue
		}

		refSource, exists := plan.Refs[refKey]
		if !exists {
			return nil, nil, fmt.Errorf("helm value file %q references unknown ref %s", valueFile, refKey)
		}
		if isSameSourceRevision(refSource.Source, sourcePlan.Source) {
			refRoots[refKey] = "."
			continue
		}
		refSources[refKey] = render.ResolvedSource{
			RepoURL:        refSource.Source.RepoURL,
			TargetRevision: refSource.Source.TargetRevision,
		}
	}
	return refRoots, refSources, nil
}

func isSameSourceRevision(left, right argoappv1.ApplicationSource) bool {
	return sourcepkg.NormalizeURL(left.RepoURL) == sourcepkg.NormalizeURL(right.RepoURL) &&
		normalizeTargetRevision(left.TargetRevision) == normalizeTargetRevision(right.TargetRevision)
}

func normalizeTargetRevision(revision string) string {
	trimmed := strings.TrimSpace(revision)
	if trimmed == "" {
		return "HEAD"
	}
	return trimmed
}

func helmValueFileRefKey(valueFile string) (string, bool, error) {
	if !strings.HasPrefix(valueFile, "$") {
		return "", false, nil
	}
	ref, refPath, ok := strings.Cut(strings.TrimPrefix(valueFile, "$"), "/")
	if !ok || ref == "" || refPath == "" {
		return "", false, fmt.Errorf("helm value file %q must use $ref/path syntax", valueFile)
	}
	return "$" + ref, true, nil
}

func sourceDiagnostics(application argoappv1.Application, sourcePlan SourcePlan, diags []diagnostic.Diagnostic) []diagnostic.Diagnostic {
	if len(diags) == 0 {
		return nil
	}

	context := renderSourceContext(application, sourcePlan)
	out := make([]diagnostic.Diagnostic, len(diags))
	for i, diag := range diags {
		diag.Message = fmt.Sprintf("%s: %s", context, diag.Message)
		out[i] = diag
	}
	return out
}

func renderSourceContext(application argoappv1.Application, sourcePlan SourcePlan) string {
	parts := []string{
		fmt.Sprintf("Application %s source[%d]", applicationName(application), sourcePlan.Index),
	}
	if sourcePlan.Name != "" {
		parts = append(parts, fmt.Sprintf("name=%q", sourcePlan.Name))
	}
	if sourcePlan.Source.Path != "" {
		parts = append(parts, fmt.Sprintf("path=%q", sourcePlan.Source.Path))
	}
	if sourcePlan.Source.Chart != "" {
		parts = append(parts, fmt.Sprintf("chart=%q", sourcePlan.Source.Chart))
	}
	return strings.Join(parts, " ")
}

func applicationName(application argoappv1.Application) string {
	if application.Namespace == "" {
		return application.Name
	}
	return application.Namespace + "/" + application.Name
}
