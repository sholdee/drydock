package app

import (
	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/appset"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/discovery"
	"github.com/sholdee/drydock/internal/project"
)

// pruneMissingProjectApplications removes the generated Applications whose
// AppProject is missing, with everything they would create. The Argo CD
// v3.6.1 ApplicationSet controller looks up the AppProject of each
// generated Application in the controller namespace, and when it is not
// found it does not create or update that Application. It sets an
// ApplicationValidationError condition, and the other Applications of the
// ApplicationSet still generate
// (applicationset/controllers/applicationset_controller.go:627-633 and
// :297-331).
//
// The caller runs it only when fleet discovery rendered everything that can
// hold an AppProject and project diagnostics are on (applyFinalGates).
// drydock then decides that a project is missing with the rule of the
// project.missing diagnostic: the default project always exists, and when
// the repository declares no AppProject at all, every project may exist
// only on the cluster, so nothing is removed. When settings name the
// controller namespace, only the AppProjects in that namespace count, as in
// Argo CD. An AppProject without a namespace is in the controller
// namespace. An empty spec.project is handled during generation
// (appset.validateGeneratedApplications). A plain Application that names a
// missing project is not removed: the application controller still has the
// Application object, and drydock reports it with project.missing.
func pruneMissingProjectApplications(discovered discovery.Result, settings config.ArgoSettings) (discovery.Result, []diagnostic.Diagnostic) {
	projects := controllerNamespaceProjects(appendDiscoveredProjects(nil, discovered), settings)
	if len(projects) == 0 {
		return discovered, nil
	}
	index := project.ByName(projects)
	removedApps := map[string]struct{}{}
	var diags []diagnostic.Diagnostic
	for _, appFile := range discovered.Applications {
		if appFile.ApplicationSetKey == "" {
			continue
		}
		projectName := appFile.Application.Spec.Project
		if projectName == "" || projectName == argoappv1.DefaultAppProjectName {
			continue
		}
		if _, ok := index[projectName]; ok {
			continue
		}
		removedApps[applicationKey(appFile.Application)] = struct{}{}
		diags = append(diags, appset.MissingProjectDiagnostic(appFile.Path, projectName))
	}
	if len(removedApps) == 0 {
		return discovered, nil
	}
	return withoutRemoved(discovered, removedApps, map[string]struct{}{}), dedupeDiagnostics(diags)
}

// controllerNamespaceProjects keeps the AppProjects that the ApplicationSet
// controller can find: with a known controller namespace, the ones in it
// or without a namespace. Without one, drydock cannot tell, so it keeps
// them all.
func controllerNamespaceProjects(projects []argoappv1.AppProject, settings config.ArgoSettings) []argoappv1.AppProject {
	if !settings.ControllerNamespaceKnown() {
		return projects
	}
	namespace := settings.ControllerNamespace("")
	out := make([]argoappv1.AppProject, 0, len(projects))
	for _, proj := range projects {
		if proj.Namespace == "" || proj.Namespace == namespace {
			out = append(out, proj)
		}
	}
	return out
}
