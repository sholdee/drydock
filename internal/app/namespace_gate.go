package app

import (
	"fmt"

	appsetutils "github.com/argoproj/argo-cd/v3/applicationset/utils"
	"github.com/argoproj/argo-cd/v3/util/security"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/discovery"
)

// namespaceNotEnabledCode identifies the warning for an Application or an
// ApplicationSet in a namespace that Argo CD does not reconcile.
const namespaceNotEnabledCode = "discovery.namespace-not-enabled"

// namespaceGate decides which Applications and ApplicationSets Argo CD
// reconciles by namespace. The Argo CD v3.6.1 application controller
// reconciles an Application only in its own namespace or in a namespace
// that argocd-cmd-params-cm application.namespaces matches
// (isAppNamespaceAllowed, controller/appcontroller.go:2852-2854), and the
// ApplicationSet controller does the same for an ApplicationSet with
// applicationsetcontroller.namespaces
// (cmd/argocd-applicationset-controller/commands/applicationset_controller.go:106
// and applicationset/controllers/applicationset_controller.go:676-680). Argo
// CD never shows such an object: it sets no status on it, and the API server
// refuses it with "namespace '<namespace>' is not permitted".
//
// The controller namespace is the metadata.namespace of the
// argocd-cmd-params-cm ConfigMap, which lives there. When the ConfigMap names
// none, drydock assumes argocd, as it does for Application instance names.
// An object without a namespace is in the controller namespace. The gate applies only when drydock read an
// argocd-cmd-params-cm ConfigMap: otherwise drydock cannot tell how the
// cluster sets the lists, and it keeps every object.
type namespaceGate struct {
	active                   bool
	controllerNamespace      string
	applicationNamespaces    []string
	applicationSetNamespaces []string
}

func namespaceGateFromSettings(settings config.ArgoSettings) namespaceGate {
	controllerNamespace := settings.CommandParametersNamespace
	if controllerNamespace == "" {
		controllerNamespace = defaultArgoCDControllerNamespace
	}
	return namespaceGate{
		active:                   settings.CommandParametersFound,
		controllerNamespace:      controllerNamespace,
		applicationNamespaces:    config.ParseNamespaceList(settings.ApplicationNamespaces.Value),
		applicationSetNamespaces: config.ParseNamespaceList(settings.ApplicationSetNamespaces.Value),
	}
}

// applicationAllowed reports whether the application controller reconciles
// an Application in namespace (security.IsNamespaceEnabled).
func (g namespaceGate) applicationAllowed(namespace string) bool {
	if !g.active || namespace == "" {
		return true
	}
	return security.IsNamespaceEnabled(namespace, g.controllerNamespace, g.applicationNamespaces)
}

// applicationSetAllowed reports whether the ApplicationSet controller
// reconciles an ApplicationSet in namespace. The controller adds its own
// namespace to the list (applicationset_controller.go:106) and matches with
// utils.IsNamespaceAllowed.
func (g namespaceGate) applicationSetAllowed(namespace string) bool {
	if !g.active || namespace == "" {
		return true
	}
	namespaces := append(append([]string(nil), g.applicationSetNamespaces...), g.controllerNamespace)
	return appsetutils.IsNamespaceAllowed(namespaces, namespace)
}

// discoveryOrigin is where a discovered object came from. An Application
// that an ApplicationSet generated has the origin of that ApplicationSet.
type discoveryOrigin struct {
	path          string
	documentIndex int
	parentKey     string
	renderedDir   string
}

func applicationSetOrigin(appSetFile discovery.ApplicationSetFile) discoveryOrigin {
	return discoveryOrigin{appSetFile.Path, appSetFile.DocumentIndex, appSetFile.ParentKey, appSetFile.RenderedDir}
}

func applicationOrigin(appFile discovery.ApplicationFile) discoveryOrigin {
	return discoveryOrigin{appFile.Path, appFile.DocumentIndex, appFile.ParentKey, appFile.RenderedDir}
}

// pruneNotEnabledNamespaces removes from discovered the Applications and
// ApplicationSets that Argo CD does not reconcile, with everything they
// would create: the Applications an ApplicationSet generates, and the
// Applications and ApplicationSets that a removed Application renders.
// It reports one warning for each Application and ApplicationSet that the
// gate itself removes.
func pruneNotEnabledNamespaces(discovered discovery.Result, gate namespaceGate) (discovery.Result, []diagnostic.Diagnostic) {
	if !gate.active {
		return discovered, nil
	}
	removedAppSets, diags := notEnabledApplicationSets(discovered, gate)
	removedApps, appDiags := notEnabledApplications(discovered, gate, removedAppSets)
	diags = append(diags, appDiags...)
	if len(removedApps) == 0 && len(removedAppSets) == 0 {
		return discovered, nil
	}
	addRenderedDescendants(discovered, removedApps)

	out := discovered
	out.Applications = make([]discovery.ApplicationFile, 0, len(discovered.Applications))
	for _, appFile := range discovered.Applications {
		if _, removed := removedApps[applicationKey(appFile.Application)]; !removed {
			out.Applications = append(out.Applications, appFile)
		}
	}
	out.ApplicationSets = make([]discovery.ApplicationSetFile, 0, len(discovered.ApplicationSets))
	for _, appSetFile := range discovered.ApplicationSets {
		_, removed := removedAppSets[applicationSetOrigin(appSetFile)]
		_, parentRemoved := removedApps[appSetFile.ParentKey]
		if removed || (appSetFile.ParentKey != "" && parentRemoved) {
			continue
		}
		out.ApplicationSets = append(out.ApplicationSets, appSetFile)
	}
	return out, diags
}

// notEnabledApplicationSets returns the origins of the ApplicationSets that
// the gate removes, with a warning for each.
func notEnabledApplicationSets(discovered discovery.Result, gate namespaceGate) (map[discoveryOrigin]struct{}, []diagnostic.Diagnostic) {
	removed := map[discoveryOrigin]struct{}{}
	var diags []diagnostic.Diagnostic
	for _, appSetFile := range discovered.ApplicationSets {
		appSet := appSetFile.ApplicationSet
		if gate.applicationSetAllowed(appSet.Namespace) {
			continue
		}
		removed[applicationSetOrigin(appSetFile)] = struct{}{}
		diags = append(diags, namespaceNotEnabledDiagnostic("ApplicationSet", appSet.Namespace, appSet.Name, "applicationsetcontroller.namespaces", gate.controllerNamespace, appSetFile.Path))
	}
	return removed, diags
}

// notEnabledApplications returns the keys of the Applications that the gate
// removes or that a removed ApplicationSet generated. Only the first kind
// gets a warning.
func notEnabledApplications(discovered discovery.Result, gate namespaceGate, removedAppSets map[discoveryOrigin]struct{}) (map[string]struct{}, []diagnostic.Diagnostic) {
	removed := map[string]struct{}{}
	var diags []diagnostic.Diagnostic
	for _, appFile := range discovered.Applications {
		application := appFile.Application
		if _, generated := removedAppSets[applicationOrigin(appFile)]; generated {
			removed[applicationKey(application)] = struct{}{}
			continue
		}
		if gate.applicationAllowed(application.Namespace) {
			continue
		}
		removed[applicationKey(application)] = struct{}{}
		diags = append(diags, namespaceNotEnabledDiagnostic("Application", application.Namespace, application.Name, "application.namespaces", gate.controllerNamespace, appFile.Path))
	}
	return removed, diags
}

// addRenderedDescendants adds to removed every Application that a removed
// Application renders, down to any depth: a removed Application never
// renders, so nothing it would declare exists.
func addRenderedDescendants(discovered discovery.Result, removed map[string]struct{}) {
	for {
		grew := false
		for _, appFile := range discovered.Applications {
			if appFile.ParentKey == "" {
				continue
			}
			key := applicationKey(appFile.Application)
			if _, done := removed[key]; done {
				continue
			}
			if _, parentRemoved := removed[appFile.ParentKey]; parentRemoved {
				removed[key] = struct{}{}
				grew = true
			}
		}
		if !grew {
			return
		}
	}
}

func namespaceNotEnabledDiagnostic(kind, namespace, name, key, controllerNamespace, path string) diagnostic.Diagnostic {
	return diagnostic.Diagnostic{
		Code:     namespaceNotEnabledCode,
		Severity: diagnostic.SeverityWarning,
		Category: "discovery",
		Message: fmt.Sprintf("%s %s/%s is skipped: namespace %q is not enabled for Argo CD, so Argo CD never reconciles it; add the namespace to argocd-cmd-params-cm %s to enable it (Argo CD namespace %s)",
			kind, namespace, name, namespace, key, controllerNamespace),
		Provenance: diagnostic.Provenance{Path: path},
	}
}
