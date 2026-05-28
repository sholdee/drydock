package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/avpcompat"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/manifest"
	"github.com/sholdee/drydock/internal/render"
	sourcepkg "github.com/sholdee/drydock/internal/source"
	"go.yaml.in/yaml/v4"
)

type RenderResult struct {
	Manifests        []render.Manifest
	Diagnostics      []diagnostic.Diagnostic
	PluginExecutions []PluginExecution
}

type StaticRenderers map[string][]render.Manifest

func (s StaticRenderers) RenderSource(_ context.Context, source render.ResolvedSource, _ render.RenderOptions) ([]render.Manifest, []diagnostic.Diagnostic, error) {
	return s[source.Path], nil, nil
}

type sourcePreparer interface {
	PrepareSource(ctx context.Context, application argoappv1.Application, sourcePlan SourcePlan) (SourcePlan, error)
}

func RenderApplication(ctx context.Context, application argoappv1.Application, provider render.Provider, pluginOptions ...PluginOptions) (RenderResult, error) {
	options := ApplicationRenderOptions{TrackingOptions: defaultTrackingOptions()}
	if len(pluginOptions) > 0 {
		options.PluginOptions = pluginOptions[0]
	}
	return RenderApplicationWithOptions(ctx, application, provider, options)
}

func RenderApplicationWithOptions(ctx context.Context, application argoappv1.Application, provider render.Provider, options ApplicationRenderOptions) (RenderResult, error) {
	plan, err := Plan(application)
	if err != nil {
		return RenderResult{}, err
	}

	pluginOpts := options.PluginOptions
	trackingOpts := normalizeTrackingOptions(options.TrackingOptions)
	byID := map[manifest.Identity]int{}
	var result RenderResult
	for _, sourcePlan := range plan.Sources {
		if sourcePlan.RefOnly {
			continue
		}
		if preparer, ok := provider.(sourcePreparer); ok {
			prepared, err := preparer.PrepareSource(ctx, application, sourcePlan)
			if err != nil {
				return result, fmt.Errorf("%s: %w", renderSourceContext(application, sourcePlan), err)
			}
			sourcePlan = prepared
		}

		opts, err := renderOptions(application, sourcePlan.Source)
		if err != nil {
			return result, fmt.Errorf("%s: %w", renderSourceContext(application, sourcePlan), err)
		}
		opts.EnableAVPCompat = pluginOpts.EnableAVPCompat
		opts.EnablePlugins = pluginOpts.EnablePlugins
		opts.SourceIndex = sourcePlan.Index
		opts.SourceName = sourcePlan.Name
		refRoots, refSources, err := renderRefsForSource(plan, sourcePlan, helmRefInputPaths(opts))
		if err != nil {
			return result, fmt.Errorf("%s: %w", renderSourceContext(application, sourcePlan), err)
		}
		opts.RefRoots = mergeRefRoots(opts.RefRoots, refRoots)
		opts.RefSources = refSources
		manifests, diags, err := provider.RenderSource(ctx, render.ResolvedSource{
			RepoRoot:       sourcePlan.SourceRoot,
			Path:           sourcePlan.Source.Path,
			Chart:          sourcePlan.Source.Chart,
			RepoURL:        sourcePlan.Source.RepoURL,
			TargetRevision: sourcePlan.Source.TargetRevision,
			ExplicitType:   sourcePlan.ExplicitType,
		}, opts)
		result.Diagnostics = append(result.Diagnostics, sourceDiagnostics(application, sourcePlan, diags)...)
		if err != nil {
			return result, fmt.Errorf("%s: %w", renderSourceContext(application, sourcePlan), err)
		}
		avpCompatSubstituted := false

		for _, rendered := range manifests {
			if rendered.Object != nil {
				rendered.Object = rendered.Object.DeepCopy()
			}
			if applyAVPCompatToManifest(&rendered, opts) {
				avpCompatSubstituted = true
			}
			rendered.SourceIndex = sourcePlan.Index
			rendered.SourceName = sourcePlan.Name
			ApplyDestinationNamespace(application, rendered.Object)
			if err := applyTrackingMetadata(application, rendered.Object, trackingOpts); err != nil {
				return result, fmt.Errorf("%s: %w", renderSourceContext(application, sourcePlan), err)
			}

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
		if avpCompatSubstituted {
			result.Diagnostics = append(result.Diagnostics, sourceDiagnostics(application, sourcePlan, []diagnostic.Diagnostic{avpCompatDiagnostic()})...)
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
		ArgoEnv:      argoRenderEnv(application, source),
	}
	if source.Kustomize != nil {
		opts.Kustomize = source.Kustomize.DeepCopy()
		if source.Kustomize.KubeVersion != "" {
			opts.KubeVersion = source.Kustomize.KubeVersion
		}
		if len(source.Kustomize.APIVersions) != 0 {
			opts.APIVersions = append(opts.APIVersions, source.Kustomize.APIVersions...)
		}
	}
	if source.Directory != nil {
		opts.DirectoryRecurse = source.Directory.Recurse
		opts.DirectoryInclude = source.Directory.Include
		opts.DirectoryExclude = source.Directory.Exclude
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
	opts.HelmParameters = append(opts.HelmParameters, source.Helm.Parameters...)
	opts.HelmFileParameters = append(opts.HelmFileParameters, source.Helm.FileParameters...)
	opts.IgnoreMissingValueFiles = source.Helm.IgnoreMissingValueFiles
	opts.SkipSchemaValidation = source.Helm.SkipSchemaValidation
	opts.PassCredentials = source.Helm.PassCredentials
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

func argoRenderEnv(application argoappv1.Application, source argoappv1.ApplicationSource) argoappv1.Env {
	revision := source.TargetRevision
	shortRevision := revision
	if len(shortRevision) > 7 {
		shortRevision = shortRevision[:7]
	}
	shortRevision8 := revision
	if len(shortRevision8) > 8 {
		shortRevision8 = shortRevision8[:8]
	}
	return argoappv1.Env{
		{Name: "ARGOCD_APP_NAME", Value: application.Name},
		{Name: "ARGOCD_APP_NAMESPACE", Value: application.Namespace},
		{Name: "ARGOCD_APP_PROJECT_NAME", Value: application.Spec.Project},
		{Name: "ARGOCD_APP_REVISION", Value: revision},
		{Name: "ARGOCD_APP_REVISION_SHORT", Value: shortRevision},
		{Name: "ARGOCD_APP_REVISION_SHORT_8", Value: shortRevision8},
		{Name: "ARGOCD_APP_SOURCE_REPO_URL", Value: source.RepoURL},
		{Name: "ARGOCD_APP_SOURCE_PATH", Value: source.Path},
		{Name: "ARGOCD_APP_SOURCE_TARGET_REVISION", Value: source.TargetRevision},
	}
}

func applyAVPCompatToManifest(manifest *render.Manifest, opts render.RenderOptions) bool {
	if !opts.EnableAVPCompat || manifest == nil || manifest.Object == nil {
		return false
	}
	value, changed := avpcompat.ReplaceValue(manifest.Object.Object)
	if !changed {
		return false
	}
	object, ok := value.(map[string]any)
	if !ok {
		return false
	}
	manifest.Object.Object = object
	return true
}

func avpCompatDiagnostic() diagnostic.Diagnostic {
	return diagnostic.Diagnostic{
		Code:     "plugin.avp-compat-substituted",
		Severity: diagnostic.SeverityWarning,
		Category: "plugin",
		Message:  "argocd-vault-plugin placeholders were replaced with deterministic redacted values",
	}
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

func helmRefInputPaths(opts render.RenderOptions) []string {
	paths := append([]string(nil), opts.ValueFiles...)
	for _, parameter := range opts.HelmFileParameters {
		paths = append(paths, parameter.Path)
	}
	return paths
}

func renderRefsForSource(plan PlanResult, sourcePlan SourcePlan, paths []string) (map[string]string, map[string]render.ResolvedSource, error) {
	if len(paths) == 0 {
		return map[string]string{}, map[string]render.ResolvedSource{}, nil
	}

	refRoots := map[string]string{}
	refSources := map[string]render.ResolvedSource{}
	for _, filePath := range paths {
		refKey, ok, err := helmValueFileRefKey(filePath)
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			continue
		}

		refSource, exists := plan.Refs[refKey]
		if !exists {
			return nil, nil, fmt.Errorf("helm file reference %q references unknown ref %s", filePath, refKey)
		}
		if isSameSourceRevision(refSource.Source, sourcePlan.Source) {
			refRoots[refKey] = "."
			continue
		}
		if candidate, ok := planSameRepoPathSource(plan, refSource); ok {
			refSources[refKey] = render.ResolvedSource{
				RepoRoot:       candidate.SourceRoot,
				Path:           candidate.Source.Path,
				RepoURL:        candidate.Source.RepoURL,
				TargetRevision: candidate.Source.TargetRevision,
				ExplicitType:   candidate.ExplicitType,
			}
			continue
		}
		refSources[refKey] = render.ResolvedSource{
			RepoURL:        refSource.Source.RepoURL,
			TargetRevision: refSource.Source.TargetRevision,
			ExplicitType:   refSource.ExplicitType,
		}
	}
	return refRoots, refSources, nil
}

func planSameRepoPathSource(plan PlanResult, refSource SourcePlan) (SourcePlan, bool) {
	for _, candidate := range plan.Sources {
		if candidate.Index == refSource.Index {
			continue
		}
		if strings.TrimSpace(candidate.Source.Path) == "" {
			continue
		}
		if isSameSourceRevision(candidate.Source, refSource.Source) {
			return candidate, true
		}
	}
	return SourcePlan{}, false
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
