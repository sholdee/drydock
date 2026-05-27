package app

import (
	"fmt"
	"path/filepath"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/discovery"
)

func markDiscoveryTier(result *discovery.Result, tier discovery.SourceTier, inputPaths []string) {
	for i := range result.Applications {
		result.Applications[i].Tier = tier
		result.Applications[i].InputPaths = fallbackInputPaths(result.Applications[i].InputPaths, inputPaths, result.Applications[i].Path)
	}
	for i := range result.ApplicationSets {
		result.ApplicationSets[i].Tier = tier
		result.ApplicationSets[i].InputPaths = fallbackInputPaths(result.ApplicationSets[i].InputPaths, inputPaths, result.ApplicationSets[i].Path)
	}
	for i := range result.Projects {
		result.Projects[i].Tier = tier
	}
	for i := range result.SettingsCandidates {
		result.SettingsCandidates[i].Tier = tier
	}
}

func fallbackInputPaths(existing, fallback []string, path string) []string {
	if len(fallback) != 0 {
		return uniqueStrings(fallback)
	}
	if len(existing) != 0 {
		return uniqueStrings(existing)
	}
	if path == "" {
		return nil
	}
	return []string{filepath.ToSlash(path)}
}

func displayDiscoveryKey(key string) string {
	parts := strings.Split(key, "\x00")
	if len(parts) < 4 {
		return key
	}
	if parts[2] == "" {
		return parts[3]
	}
	return parts[2] + "/" + parts[3]
}

func discoveryDepthExceededDiagnostic(maxDepth int) diagnostic.Diagnostic {
	diag := diagnostic.Diagnostic{
		Severity: diagnostic.SeverityError,
		Category: "discovery",
		Message:  fmt.Sprintf("maximum discovery depth %d reached before rendered Application discovery converged", maxDepth),
	}
	diag.Code = diagnostic.StableCode(diag)
	return diag
}

func dedupeDiagnostics(input []diagnostic.Diagnostic) []diagnostic.Diagnostic {
	if len(input) == 0 {
		return nil
	}
	out := make([]diagnostic.Diagnostic, 0, len(input))
	seen := map[string]struct{}{}
	for _, diag := range input {
		key := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s", diag.Code, diag.Severity, diag.Category, diag.Message, diag.Provenance.Path, diag.Provenance.Pointer)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, diag)
	}
	return out
}

func uniqueStrings(input []string) []string {
	if len(input) == 0 {
		return nil
	}
	out := make([]string, 0, len(input))
	seen := map[string]struct{}{}
	for _, value := range input {
		value = filepath.ToSlash(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func applicationDiscoveryKey(app argoappv1.Application) string {
	return objectDiscoveryKey("argoproj.io/v1alpha1", "Application", app.Namespace, app.Name)
}

func applicationSetDiscoveryKey(appSet argoappv1.ApplicationSet) string {
	return objectDiscoveryKey("argoproj.io/v1alpha1", "ApplicationSet", appSet.Namespace, appSet.Name)
}

func projectDiscoveryKey(project argoappv1.AppProject) string {
	return objectDiscoveryKey("argoproj.io/v1alpha1", "AppProject", project.Namespace, project.Name)
}

func settingsDiscoveryKey(candidate discovery.SettingsCandidate) string {
	if candidate.Object != nil && candidate.Object.GetKind() != "" && candidate.Object.GetName() != "" {
		return objectDiscoveryKey(candidate.Object.GetAPIVersion(), candidate.Object.GetKind(), candidate.Object.GetNamespace(), candidate.Object.GetName())
	}
	if candidate.APIVersion != "" && candidate.Name != "" {
		return objectDiscoveryKey(candidate.APIVersion, settingsObjectKind(candidate.Kind), candidate.Namespace, candidate.Name)
	}
	return fmt.Sprintf("settings\x00%s\x00%s\x00%d", candidate.Kind, filepath.ToSlash(candidate.Path), candidate.DocumentIndex)
}

func settingsObjectKind(kind string) string {
	switch kind {
	case "argocd-cm", "argocd-cmp-cm":
		return "ConfigMap"
	case "repository-secret":
		return "Secret"
	default:
		return kind
	}
}

func objectDiscoveryKey(apiVersion, kind, namespace, name string) string {
	return apiVersion + "\x00" + kind + "\x00" + namespace + "\x00" + name
}
