package app

import (
	"context"
	"encoding/json"
	"fmt"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/home-operations/argocd-local/internal/diagnostic"
	"github.com/home-operations/argocd-local/internal/manifest"
	"github.com/home-operations/argocd-local/internal/render"
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
			return result, fmt.Errorf("render source %d: %w", sourcePlan.Index, err)
		}
		manifests, diags, err := provider.RenderSource(ctx, render.ResolvedSource{
			Path:  sourcePlan.Source.Path,
			Chart: sourcePlan.Source.Chart,
		}, opts)
		if err != nil {
			return result, fmt.Errorf("render source %d: %w", sourcePlan.Index, err)
		}

		result.Diagnostics = append(result.Diagnostics, diags...)
		for _, rendered := range manifests {
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
		AppName:   application.Name,
		Namespace: application.Spec.Destination.Namespace,
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
	valuesObject, err := helmValuesObject(source.Helm)
	if err != nil {
		return render.RenderOptions{}, err
	}
	opts.ValuesObject = valuesObject
	return opts, nil
}

func helmValuesObject(helm *argoappv1.ApplicationSourceHelm) (map[string]any, error) {
	if helm.ValuesObject == nil || len(helm.ValuesObject.Raw) == 0 {
		return nil, nil
	}

	var values map[string]any
	if err := json.Unmarshal(helm.ValuesObject.Raw, &values); err != nil {
		return nil, fmt.Errorf("decode helm valuesObject: %w", err)
	}
	return values, nil
}
