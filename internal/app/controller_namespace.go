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

// landingFunc returns the namespaces where a committed object at path with
// namespace own lands (committedFileWalker.committedLandings). A nil one
// returns own.
type landingFunc func(path, own string) []string

// of returns the namespaces of an object: the one that the render gives a
// rendered object, and where a committed one lands. It is empty for an
// object that has no namespace after it lands.
func (landing landingFunc) of(tier discovery.SourceTier, path, own string) []string {
	if landing == nil || tier != discovery.SourceTierStatic {
		if own == "" {
			return nil
		}
		return []string{own}
	}
	return landing(path, own)
}

// inNamespace reports whether an object in namespaces is in namespace. An
// object that has no namespace after it lands is in every namespace.
func inNamespace(namespaces []string, namespace string) bool {
	return len(namespaces) == 0 || slices.Contains(namespaces, namespace)
}

// settingsCandidatesInNamespace drops the settings candidates that Argo CD
// never reads because no Application puts them in namespace, the one that
// --argocd-namespace names. Argo CD reads its settings ConfigMaps and its
// repository and cluster Secrets only in the namespace that it runs in
// (util/settings, util/db at v3.6.1). Without the flag, namespace is empty
// and every candidate stays. A rendered candidate is in its namespace as
// rendered, and a committed one in each namespace where an Application on
// this cluster puts it (landing). drydock never skips an object that has
// no namespace after it lands. loadErrors holds why a candidate does not
// load; the warning for a skipped one names it.
func settingsCandidatesInNamespace(candidates []discovery.SettingsCandidate, namespace string, landing landingFunc, loadErrors map[string]error) ([]discovery.SettingsCandidate, []diagnostic.Diagnostic) {
	if namespace == "" {
		return candidates, nil
	}
	kept := candidates[:0:0]
	var diags []diagnostic.Diagnostic
	for _, candidate := range candidates {
		if !controllerNamespaceScoped(candidate.Kind) {
			kept = append(kept, candidate)
			continue
		}
		namespaces := landing.of(candidate.Tier, candidate.Path, candidateNamespace(candidate))
		if inNamespace(namespaces, namespace) {
			kept = append(kept, candidate)
			continue
		}
		diag := outsideControllerNamespaceDiagnostic(settingsCandidateDisplayKind(candidate.Kind), strings.Join(namespaces, ","), candidateName(candidate), namespace, "reads its settings", candidate.Path)
		if err := loadErrors[settingsCandidateLoadKey(candidate)]; err != nil {
			diag.Message += fmt.Sprintf("; drydock does not read it, so it does not fail on it: %v", err)
		}
		diags = append(diags, diag)
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
// an AppProject. They look an AppProject up only in their own namespace
// (util/argo GetAppProject, projLister.AppProjects(ns) at v3.6.1). drydock
// applies that only when --argocd-namespace names the namespace. A
// committed AppProject is in each namespace where it lands (landing). An
// AppProject that has no namespace after it lands stays.
func projectInControllerNamespace(projectFile discovery.ProjectFile, settings config.ArgoSettings, landing landingFunc) (string, bool) {
	namespace := strings.TrimSpace(settings.ArgoCDNamespace)
	if namespace == "" {
		return projectFile.Project.Namespace, true
	}
	namespaces := landing.of(projectFile.Tier, projectFile.Path, projectFile.Project.Namespace)
	return strings.Join(namespaces, ","), inNamespace(namespaces, namespace)
}

// projectNamespaceDiagnostics warns once for each discovered AppProject
// that projectInControllerNamespace skips.
func projectNamespaceDiagnostics(projects []discovery.ProjectFile, settings config.ArgoSettings, landing landingFunc) []diagnostic.Diagnostic {
	var diags []diagnostic.Diagnostic
	for _, projectFile := range projects {
		carried, found := projectInControllerNamespace(projectFile, settings, landing)
		if found {
			continue
		}
		diags = append(diags, outsideControllerNamespaceDiagnostic("AppProject", carried, projectFile.Project.Name, settings.ArgoCDNamespace, "looks AppProjects up", projectFile.Path))
	}
	return diags
}

// appendDiscoveredProjects appends the discovered AppProjects that the Argo
// CD controllers find with settings (projectInControllerNamespace).
// loadSettingsFromDiscovery warns about the others.
func appendDiscoveredProjects(projects []argoappv1.AppProject, discovered discovery.Result, settings config.ArgoSettings, landing landingFunc) []argoappv1.AppProject {
	for _, projectFile := range discovered.Projects {
		if _, found := projectInControllerNamespace(projectFile, settings, landing); found {
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
func addDiscoveredProjects(result *BuildResult, discovered discovery.Result, landing landingFunc) {
	result.projectsDeclared = result.projectsDeclared || len(discovered.Projects) > 0
	result.Projects = appendDiscoveredProjects(result.Projects, discovered, result.Settings, landing)
}
