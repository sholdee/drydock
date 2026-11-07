package app

import (
	"fmt"
	"path/filepath"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/discovery"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func appendDiscoveredProjects(projects []argoappv1.AppProject, discovered discovery.Result) []argoappv1.AppProject {
	for _, projectFile := range discovered.Projects {
		projects = append(projects, projectFile.Project)
	}
	return projects
}

// loadSettingsFromDiscovery loads and merges the discovered settings
// candidates. A committed values file or settings ConfigMap that a rendered
// settings ConfigMap read (RenderInputs) is left out: Helm merged it with the
// other values of its release and the chart defaults, and the rendered
// ConfigMap holds that result.
func loadSettingsFromDiscovery(root string, discovered discovery.Result) (config.ArgoSettings, []diagnostic.Diagnostic, error) {
	var candidates []config.ArgoSettings
	var diags []diagnostic.Diagnostic
	seen := make(map[string]struct{}, len(discovered.SettingsCandidates))
	renderInputs := renderedSettingsInputs(discovered.SettingsCandidates)
	for _, candidate := range discovered.SettingsCandidates {
		if supersededBySettingsRender(candidate, renderInputs) {
			continue
		}
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
	namespace, namespaceDiags := repoServerHelmNamespace(discovered.SettingsCandidates)
	merged.RepoServerHelmNamespace = namespace
	diags = append(diags, namespaceDiags...)
	return merged, diags, nil
}

// repoServerHelmNamespace works out the namespace that helm uses in the
// repo-server of the rendered Argo CD install (config.RepoServerHelmNamespace)
// from the rendered repo-server Deployment and ServiceAccounts. Committed
// files do not count: a committed Deployment is often only a patch, and
// only a render shows the whole install. It is nil without a rendered
// repo-server, and two rendered repo-servers that disagree give a warning
// and the first one's namespace.
func repoServerHelmNamespace(candidates []discovery.SettingsCandidate) (*config.Value[string], []diagnostic.Diagnostic) {
	var serviceAccounts []*unstructured.Unstructured
	for _, candidate := range candidates {
		if candidate.Kind == discovery.RepoServerServiceAccountKind && candidate.Tier != discovery.SourceTierStatic && candidate.Object != nil {
			serviceAccounts = append(serviceAccounts, candidate.Object)
		}
	}
	var out *config.Value[string]
	var diags []diagnostic.Diagnostic
	for _, candidate := range candidates {
		if candidate.Kind != discovery.RepoServerDeploymentKind || candidate.Tier == discovery.SourceTierStatic || candidate.Object == nil {
			continue
		}
		podNamespace := candidate.Object.GetNamespace()
		var sameNamespace []*unstructured.Unstructured
		for _, serviceAccount := range serviceAccounts {
			if serviceAccount.GetNamespace() == podNamespace {
				sameNamespace = append(sameNamespace, serviceAccount)
			}
		}
		namespace, ok := config.RepoServerHelmNamespace(candidate.Object, sameNamespace, podNamespace)
		if !ok {
			continue
		}
		provenance := diagnostic.Provenance{Path: filepath.ToSlash(candidate.Path)}
		if out == nil {
			out = &config.Value[string]{Value: namespace, Provenance: provenance}
			continue
		}
		if out.Value != namespace {
			diags = append(diags, diagnostic.Diagnostic{
				Severity:   diagnostic.SeverityWarning,
				Category:   "settings",
				Message:    fmt.Sprintf("conflicting Argo CD repo-server Helm namespaces %q and %q; drydock uses %q for Helm sources without a namespace", out.Value, namespace, out.Value),
				Provenance: provenance,
			})
		}
	}
	return out, diags
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
