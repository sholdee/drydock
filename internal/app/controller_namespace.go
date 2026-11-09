package app

import (
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/discovery"
	"github.com/sholdee/drydock/internal/project"
)

const (
	// controllerNamespaceAmbiguousCode warns that the argocd-cmd-params-cm
	// ConfigMaps name several namespaces, and the namespace that drydock
	// uses is one of them. A hub Argo CD next to tenant installs is a normal
	// layout, so --strict does not fail on it.
	controllerNamespaceAmbiguousCode = "settings.controller-namespace-ambiguous"
	// controllerNamespaceUnconfirmedCode warns that argocd-cmd-params-cm
	// ConfigMaps name namespaces, but none names the namespace that drydock
	// uses. --strict fails on it.
	controllerNamespaceUnconfirmedCode = "settings.controller-namespace-unconfirmed"
	// outsideControllerNamespaceCode warns that --argocd-namespace makes
	// drydock skip an Argo CD settings object or an AppProject in another
	// namespace, because Argo CD reads them only in its own namespace. Like
	// discovery.namespace-not-enabled, it reports what Argo CD ignores, so
	// --strict does not fail on it.
	outsideControllerNamespaceCode = "discovery.outside-controller-namespace"
)

// commandParametersCandidateKind is the settings candidate kind of an
// argocd-cmd-params-cm ConfigMap.
const commandParametersCandidateKind = "argocd-cmd-params-cm"

// commandParametersNamespaces returns the namespaces that the
// argocd-cmd-params-cm candidates name, each with the sorted paths of the
// candidates that name it. A candidate counts when it carries a namespace,
// as written in a committed file or after the render for a rendered one,
// and reaches this cluster: a rendered candidate unless only Applications
// on other clusters render it (SettingsCandidate.RemoteOnly), and a
// committed file unless only such Applications render it
// (staticFileRenders). A committed file that no Application renders counts.
// The result is a set, so the order of the candidates does not matter.
func commandParametersNamespaces(root string, candidates []discovery.SettingsCandidate, applications []discovery.ApplicationFile, settings config.ArgoSettings) map[string][]string {
	named := map[string][]string{}
	var committed []discovery.SettingsCandidate
	for _, candidate := range candidates {
		if candidate.Kind != commandParametersCandidateKind || candidateNamespace(candidate) == "" {
			continue
		}
		if candidate.Tier == discovery.SourceTierStatic {
			committed = append(committed, candidate)
			continue
		}
		if !candidate.RemoteOnly {
			addNamedNamespace(named, candidate)
		}
	}
	remote := len(committed) > 0 && hasRemoteApplication(applications, settings)
	for _, candidate := range committed {
		if remote && staticFileRenders(root, filepath.ToSlash(candidate.Path), applications, settings).remoteOnly {
			continue
		}
		addNamedNamespace(named, candidate)
	}
	return named
}

func addNamedNamespace(named map[string][]string, candidate discovery.SettingsCandidate) {
	namespace := candidateNamespace(candidate)
	path := filepath.ToSlash(candidate.Path)
	if !slices.Contains(named[namespace], path) {
		named[namespace] = append(named[namespace], path)
		sort.Strings(named[namespace])
	}
}

// candidateNamespace is the namespace that a settings candidate carries:
// the namespace of the rendered or decoded object, else the one of the
// committed document. It is empty when the object has none.
func candidateNamespace(candidate discovery.SettingsCandidate) string {
	if candidate.Object != nil {
		return strings.TrimSpace(candidate.Object.GetNamespace())
	}
	return strings.TrimSpace(candidate.Namespace)
}

// hasRemoteApplication reports whether a discovered Application deploys to
// another cluster. Only then can a committed file reach other clusters
// only, so drydock asks which Applications render it.
func hasRemoteApplication(applications []discovery.ApplicationFile, settings config.ArgoSettings) bool {
	for _, appFile := range applications {
		server, ok := project.ResolveDestinationServer(appFile.Application.Spec.Destination, settings)
		if ok && server != argoappv1.KubernetesInternalAPIServerAddr {
			return true
		}
	}
	return false
}

// controllerNamespaceDiagnostics warns when the argocd-cmd-params-cm
// ConfigMaps do not settle the controller namespace that settings use: they
// name several namespaces, or none of them names the one that drydock
// uses. Each case gives one warning that lists the namespaces and names the
// --argocd-namespace flag.
func controllerNamespaceDiagnostics(named map[string][]string, settings config.ArgoSettings) []diagnostic.Diagnostic {
	if len(named) == 0 {
		return nil
	}
	namespaces := make([]string, 0, len(named))
	for namespace := range named {
		namespaces = append(namespaces, namespace)
	}
	sort.Strings(namespaces)
	chosen := settings.ControllerNamespace("")
	flag := strings.TrimSpace(settings.ArgoCDNamespace) != ""
	listed := strings.Join(namespaces, ", ")
	provenance := diagnostic.Provenance{Path: named[namespaces[0]][0]}
	if _, ok := named[chosen]; ok {
		if len(namespaces) == 1 {
			return nil
		}
		reason := "it is the default; pass --argocd-namespace to name another namespace where Argo CD runs"
		if flag {
			reason = "--argocd-namespace names it"
		}
		return []diagnostic.Diagnostic{{
			Code:       controllerNamespaceAmbiguousCode,
			Severity:   diagnostic.SeverityWarning,
			Category:   "settings",
			Message:    fmt.Sprintf("argocd-cmd-params-cm ConfigMaps name several Argo CD namespaces (%s); drydock uses %q because %s", listed, chosen, reason),
			Provenance: provenance,
		}}
	}
	message := fmt.Sprintf("argocd-cmd-params-cm ConfigMaps name several Argo CD namespaces (%s) and none of them is %q, so drydock uses the default %q; pass --argocd-namespace to name the namespace where Argo CD runs", listed, chosen, chosen)
	if flag {
		message = fmt.Sprintf("--argocd-namespace names %q, but the argocd-cmd-params-cm ConfigMaps name only %s; check that --argocd-namespace names the namespace where Argo CD runs", chosen, listed)
	}
	return []diagnostic.Diagnostic{{
		Code:       controllerNamespaceUnconfirmedCode,
		Severity:   diagnostic.SeverityWarning,
		Category:   "settings",
		Message:    message,
		Provenance: provenance,
	}}
}

// controllerNamespaceScoped reports whether Argo CD reads a settings
// candidate of kind only in its own namespace: the settings ConfigMaps and
// the repository and cluster Secrets. A values file and the repo-server
// objects have other rules.
func controllerNamespaceScoped(kind string) bool {
	return discovery.IsSettingsConfigMapKind(kind) || kind == "repository-secret" || kind == "cluster-secret"
}

// settingsCandidatesInNamespace drops the settings candidates that Argo CD
// never reads because they carry another namespace than namespace, the one
// that --argocd-namespace names. Argo CD reads its settings ConfigMaps and
// its repository and cluster Secrets only in the namespace that it runs in
// (util/settings, util/db at v3.6.1). Without the flag, namespace is empty
// and every candidate stays. A candidate without a namespace always stays:
// a kustomization or the destination can put it in any namespace.
func settingsCandidatesInNamespace(candidates []discovery.SettingsCandidate, namespace string) ([]discovery.SettingsCandidate, []diagnostic.Diagnostic) {
	if namespace == "" {
		return candidates, nil
	}
	kept := candidates[:0:0]
	var diags []diagnostic.Diagnostic
	for _, candidate := range candidates {
		carried := candidateNamespace(candidate)
		if !controllerNamespaceScoped(candidate.Kind) || carried == "" || carried == namespace {
			kept = append(kept, candidate)
			continue
		}
		diags = append(diags, outsideControllerNamespaceDiagnostic(settingsCandidateDisplayKind(candidate.Kind), carried, candidateName(candidate), namespace, "reads its settings", candidate.Path))
	}
	return kept, diags
}

func candidateName(candidate discovery.SettingsCandidate) string {
	if candidate.Object != nil && candidate.Object.GetName() != "" {
		return candidate.Object.GetName()
	}
	if candidate.Name != "" {
		return candidate.Name
	}
	return candidate.Kind
}

func outsideControllerNamespaceDiagnostic(kind, namespace, name, controllerNamespace, what, path string) diagnostic.Diagnostic {
	return diagnostic.Diagnostic{
		Code:     outsideControllerNamespaceCode,
		Severity: diagnostic.SeverityWarning,
		Category: "discovery",
		Message: fmt.Sprintf("%s %s/%s is skipped: it is in namespace %q, but --argocd-namespace says that Argo CD runs in namespace %q and %s only there",
			kind, namespace, name, namespace, controllerNamespace, what),
		Provenance: diagnostic.Provenance{Path: filepath.ToSlash(path)},
	}
}

// settingsCandidateDisplayKind names the object of a settings candidate
// kind in a message.
func settingsCandidateDisplayKind(kind string) string {
	switch kind {
	case "repository-secret":
		return "repository Secret"
	case "cluster-secret":
		return "cluster Secret"
	default:
		return "ConfigMap"
	}
}

// projectInControllerNamespace reports whether the Argo CD controllers find
// proj. They look an AppProject up only in their own namespace
// (util/argo GetAppProject, projLister.AppProjects(ns) at v3.6.1). drydock
// applies that only when --argocd-namespace names the namespace. An
// AppProject without a namespace stays.
func projectInControllerNamespace(proj argoappv1.AppProject, settings config.ArgoSettings) bool {
	namespace := strings.TrimSpace(settings.ArgoCDNamespace)
	return namespace == "" || proj.Namespace == "" || proj.Namespace == namespace
}

// projectNamespaceDiagnostics warns once for each discovered AppProject
// that projectInControllerNamespace skips.
func projectNamespaceDiagnostics(projects []discovery.ProjectFile, settings config.ArgoSettings) []diagnostic.Diagnostic {
	var diags []diagnostic.Diagnostic
	for _, projectFile := range projects {
		proj := projectFile.Project
		if projectInControllerNamespace(proj, settings) {
			continue
		}
		diags = append(diags, outsideControllerNamespaceDiagnostic("AppProject", proj.Namespace, proj.Name, settings.ArgoCDNamespace, "looks AppProjects up", projectFile.Path))
	}
	return diags
}

// appendDiscoveredProjects appends the discovered AppProjects that the Argo
// CD controllers find with settings (projectInControllerNamespace).
// loadSettingsFromDiscovery warns about the others.
func appendDiscoveredProjects(projects []argoappv1.AppProject, discovered discovery.Result, settings config.ArgoSettings) []argoappv1.AppProject {
	for _, projectFile := range discovered.Projects {
		if projectInControllerNamespace(projectFile.Project, settings) {
			projects = append(projects, projectFile.Project)
		}
	}
	return projects
}

// addDiscoveredProjects adds the discovered AppProjects that the Argo CD
// controllers find to result. It records that the repository declares
// AppProjects before the namespace filter, so an Application that names a
// skipped one gets project.missing, as Argo CD fails to find it
// (util/argo/argo.go:819-822), not the permissive implicit default.
func addDiscoveredProjects(result *BuildResult, discovered discovery.Result) {
	result.projectsDeclared = result.projectsDeclared || len(discovered.Projects) > 0
	result.Projects = appendDiscoveredProjects(result.Projects, discovered, result.Settings)
}
