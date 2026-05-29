package app

import (
	"fmt"
	"path/filepath"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/discovery"
)

func appendDiscoveredProjects(projects []argoappv1.AppProject, discovered discovery.Result) []argoappv1.AppProject {
	for _, projectFile := range discovered.Projects {
		projects = append(projects, projectFile.Project)
	}
	return projects
}

func loadSettingsFromDiscovery(root string, discovered discovery.Result) (config.ArgoSettings, []diagnostic.Diagnostic, error) {
	var candidates []config.ArgoSettings
	var diags []diagnostic.Diagnostic
	seen := make(map[string]struct{}, len(discovered.SettingsCandidates))
	for _, candidate := range discovered.SettingsCandidates {
		key := fmt.Sprintf("%s\x00%s\x00%d", candidate.Kind, candidate.Path, candidate.DocumentIndex)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		settings, nextDiags, handled, err := loadSettingsCandidate(root, candidate)
		if !handled {
			continue
		}
		if err != nil {
			return config.DefaultSettings(), diags, err
		}
		candidates = append(candidates, settings)
		diags = append(diags, nextDiags...)
	}
	merged, mergeDiags := config.MergeDiscovered(candidates)
	diags = append(diags, mergeDiags...)
	return merged, diags, nil
}

func loadSettingsCandidate(root string, candidate discovery.SettingsCandidate) (config.ArgoSettings, []diagnostic.Diagnostic, bool, error) {
	path := filepath.Join(root, candidate.Path)
	switch candidate.Kind {
	case "argocd-cm":
		if candidate.Object != nil {
			settings, diags, err := config.LoadFromConfigMapObject(candidate.Path, candidate.Object)
			return settings, diags, true, err
		}
		settings, diags, err := config.LoadFromConfigMapDocument(path, candidate.DocumentIndex)
		return settings, diags, true, err
	case "argocd-cmd-params-cm":
		if candidate.Object != nil {
			settings, diags, err := config.LoadCommandParametersConfigMapObject(candidate.Path, candidate.Object)
			return settings, diags, true, err
		}
		settings, diags, err := config.LoadCommandParametersConfigMapDocument(path, candidate.DocumentIndex)
		return settings, diags, true, err
	case "argocd-cmp-cm":
		if candidate.Object != nil {
			settings, diags, err := config.LoadConfigManagementPluginConfigMapObject(candidate.Path, candidate.Object)
			return settings, diags, true, err
		}
		settings, diags, err := config.LoadConfigManagementPluginConfigMapDocument(path, candidate.DocumentIndex)
		return settings, diags, true, err
	case "argocd-values":
		if candidate.Object != nil {
			settings, diags, err := config.LoadFromHelmValuesObject(candidate.Path, candidate.Object)
			return settings, diags, true, err
		}
		settings, diags, err := config.LoadFromHelmValuesDocument(path, candidate.DocumentIndex)
		return settings, diags, true, err
	case "repository-secret":
		if candidate.Object != nil {
			settings, diags, err := config.LoadRepositorySecretObject(candidate.Path, candidate.Object)
			return settings, diags, true, err
		}
		settings, diags, err := config.LoadRepositorySecretDocument(path, candidate.DocumentIndex)
		return settings, diags, true, err
	case "cluster-secret":
		if candidate.Object != nil {
			settings, diags, err := config.LoadClusterSecretObject(candidate.Path, candidate.Object)
			return settings, diags, true, err
		}
		settings, diags, err := config.LoadClusterSecretDocument(path, candidate.DocumentIndex)
		return settings, diags, true, err
	default:
		return config.ArgoSettings{}, nil, false, nil
	}
}
