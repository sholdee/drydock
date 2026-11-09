package app

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/discovery"
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

// candidateNamespace is the namespace that a settings candidate carries:
// the namespace of the rendered or decoded object, else the one of the
// committed document. It is empty when the object has none.
func candidateNamespace(candidate discovery.SettingsCandidate) string {
	if candidate.Object != nil {
		return strings.TrimSpace(candidate.Object.GetNamespace())
	}
	return strings.TrimSpace(candidate.Namespace)
}

// controllerNamespaceDiagnostics warns when the argocd-cmd-params-cm
// ConfigMaps do not settle the controller namespace that settings use: they
// name several namespaces, none of them names the one that drydock uses, or
// other Argo CD installs exist. Each case gives one warning that lists the
// namespaces and names the --argocd-namespace flag.
func controllerNamespaceDiagnostics(found namedNamespaces, settings config.ArgoSettings) []diagnostic.Diagnostic {
	if len(found.named) == 0 && len(found.others) == 0 {
		return nil
	}
	chosen := settings.ControllerNamespace("")
	_, confirmed := found.named[chosen]
	if confirmed && len(found.named) == 1 && len(found.others) == 0 {
		return nil
	}
	if !confirmed && len(found.named) > 0 {
		return []diagnostic.Diagnostic{controllerNamespaceWarning(controllerNamespaceUnconfirmedCode, unconfirmedNamespaceMessage(found, chosen, settings), found)}
	}
	return []diagnostic.Diagnostic{controllerNamespaceWarning(controllerNamespaceAmbiguousCode, ambiguousNamespaceMessage(found, chosen, settings), found)}
}

func ambiguousNamespaceMessage(found namedNamespaces, chosen string, settings config.ArgoSettings) string {
	reason := "it is the default; pass --argocd-namespace to name another namespace where Argo CD runs"
	switch {
	case strings.TrimSpace(settings.ArgoCDNamespace) != "":
		reason = "--argocd-namespace names it"
	case len(found.named) == 1:
		reason = "the argocd-cmd-params-cm names it; pass --argocd-namespace to name another namespace where Argo CD runs"
	}
	message := fmt.Sprintf("drydock uses the Argo CD namespace %q because %s", chosen, reason)
	if len(found.named) > 1 {
		message = fmt.Sprintf("argocd-cmd-params-cm ConfigMaps name several Argo CD namespaces (%s); %s", namespaceList(found.named), message)
	}
	return message + otherInstallsNote(found)
}

func unconfirmedNamespaceMessage(found namedNamespaces, chosen string, settings config.ArgoSettings) string {
	if strings.TrimSpace(settings.ArgoCDNamespace) != "" {
		return fmt.Sprintf("--argocd-namespace names %q, but the argocd-cmd-params-cm ConfigMaps name only %s; check that --argocd-namespace names the namespace where Argo CD runs", chosen, namespaceList(found.named)) + otherInstallsNote(found)
	}
	return fmt.Sprintf("argocd-cmd-params-cm ConfigMaps name several Argo CD namespaces (%s) and none of them is %q, so drydock uses the default %q; pass --argocd-namespace to name the namespace where Argo CD runs", namespaceList(found.named), chosen, chosen) + otherInstallsNote(found)
}

// otherInstallsNote names the other Argo CD installs for a warning.
func otherInstallsNote(found namedNamespaces) string {
	if len(found.others) == 0 {
		return ""
	}
	return fmt.Sprintf("; drydock ignores the settings ConfigMaps of the other Argo CD installs in %s, because their own argocd-cmd-params-cm does not reconcile the Application that puts it there", namespaceList(found.others))
}

func namespaceList(namespaces map[string][]string) string {
	return strings.Join(slices.Sorted(maps.Keys(namespaces)), ", ")
}

func controllerNamespaceWarning(code, message string, found namedNamespaces) diagnostic.Diagnostic {
	paths := found.named
	if len(paths) == 0 {
		paths = found.others
	}
	first := slices.Sorted(maps.Keys(paths))[0]
	return diagnostic.Diagnostic{
		Code:       code,
		Severity:   diagnostic.SeverityWarning,
		Category:   "settings",
		Message:    message,
		Provenance: diagnostic.Provenance{Path: paths[first][0]},
	}
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
