package project

import (
	"fmt"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/manifest"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	projectResourceDeniedCode            = "project.resource-denied"
	projectResourceDestinationDeniedCode = "project.resource-destination-denied"
	projectResourceScopeDeferredCode     = "project.resource-scope-deferred"
)

type renderedResourceScope struct {
	namespaced bool
	namespace  string
	deferred   bool
}

type resourcePolicyOutcome int

const (
	resourcePolicyOutcomeAllowed resourcePolicyOutcome = iota
	resourcePolicyOutcomeDenied
	resourcePolicyOutcomeDeferred
)

// RenderedResource carries rendered object metadata that can be lost during Argo-style normalization.
type RenderedResource struct {
	Object                       *unstructured.Unstructured
	NamespaceBeforeNormalization string
	// Hook marks a hook-annotated object. Hooks are validated like any other
	// rendered object and only named differently in diagnostics.
	Hook bool
}

type renderedResourcePolicyValidator struct {
	app                           argoappv1.Application
	proj                          argoappv1.AppProject
	resourcePolicyProject         argoappv1.AppProject
	dest                          argoappv1.ApplicationDestination
	destCluster                   *argoappv1.Cluster
	projectClusters               []*argoappv1.Cluster
	projectScopedCheckDeferred    bool
	projectScopedDeferralReported bool
	// offline is the destination's offline view when its cluster is
	// unresolved (unresolvedOfflineDestination) and no project-scoped cluster
	// check applies; offlineApplies reports whether it is set.
	offline                    offlineDestination
	offlineApplies             bool
	offlineDeferralReported    bool
	appDestinationChecked      bool
	appDestinationPermitted    bool
	appDestinationPermittedErr error
	crdScope                   manifest.CRDScopeRegistry
}

// ValidateRenderedResourcePolicy validates rendered Kubernetes objects for one Application against its effective AppProject resource policy.
func ValidateRenderedResourcePolicy(app argoappv1.Application, objects []*unstructured.Unstructured, projects []argoappv1.AppProject, settings config.ArgoSettings) []diagnostic.Diagnostic {
	resources := make([]RenderedResource, 0, len(objects))
	for _, obj := range objects {
		if obj == nil {
			resources = append(resources, RenderedResource{})
			continue
		}
		resources = append(resources, RenderedResource{
			Object:                       obj,
			NamespaceBeforeNormalization: strings.TrimSpace(obj.GetNamespace()),
		})
	}
	return ValidateRenderedResourcePolicyResources(app, resources, projects, settings)
}

// ValidateRenderedResourcePolicyResources validates rendered Kubernetes objects with pre-normalization metadata.
func ValidateRenderedResourcePolicyResources(app argoappv1.Application, resources []RenderedResource, projects []argoappv1.AppProject, settings config.ArgoSettings) []diagnostic.Diagnostic {
	return ValidateRenderedResourcePolicyResourcesWithRegistry(app, resources, projects, settings, nil, nil)
}

// ValidateRenderedResourcePolicyResourcesWithRegistry validates rendered Kubernetes objects with pre-normalization
// metadata, consulting the provided CRDScopeRegistry to resolve CR scope before falling back to deferral.
// A nil registry is safe and preserves the existing deferral behavior.
// generatedBy is the lookup of ValidateGeneratedApplications, so a missing
// AppProject gives the same project.missing text on both paths and the
// two warnings merge into one; nil means no Application is generated.
func ValidateRenderedResourcePolicyResourcesWithRegistry(app argoappv1.Application, resources []RenderedResource, projects []argoappv1.AppProject, settings config.ArgoSettings, registry manifest.CRDScopeRegistry, generatedBy func(argoappv1.Application) string) []diagnostic.Diagnostic {
	if len(resources) == 0 {
		return nil
	}

	validator, diags := newRenderedResourcePolicyValidator(app, projects, settings, generatedBy)
	if len(diags) > 0 {
		return diags
	}
	if destinationInClusterDisabled(app.Spec.Destination, settings) {
		// Argo CD rejects the Application before it evaluates any resource;
		// validateDestination reports it.
		return nil
	}
	validator.crdScope = registry
	return validator.validate(resources)
}

func newRenderedResourcePolicyValidator(app argoappv1.Application, projects []argoappv1.AppProject, settings config.ArgoSettings, generatedBy func(argoappv1.Application) string) (*renderedResourcePolicyValidator, []diagnostic.Diagnostic) {
	proj, ok := renderedResourcePolicyProject(app, projects, settings)
	if !ok {
		return nil, []diagnostic.Diagnostic{projectWarning(app, missingProjectMessage(app, applicationProject(app), generatedBy))}
	}

	dest := app.Spec.Destination
	destCluster, resolution := destinationCluster(dest, settings)
	projectClusters, projectClusterMetadataAvailable := projectScopedClusters(proj.Name, settings)
	projectScopedCheckAvailable := projectClusterMetadataAvailable || resolution == destinationFromClusterSecret

	resourcePolicyProject := proj
	projectScopedCheckDeferred := resourcePolicyProject.Spec.PermitOnlyProjectScopedClusters && !projectScopedCheckAvailable
	if projectScopedCheckDeferred {
		resourcePolicyProject.Spec.PermitOnlyProjectScopedClusters = false
	}
	offline, offlineApplies := unresolvedOfflineDestination(dest, resolution)
	offlineApplies = offlineApplies && !resourcePolicyProject.Spec.PermitOnlyProjectScopedClusters

	return &renderedResourcePolicyValidator{
		app:                        app,
		proj:                       proj,
		resourcePolicyProject:      resourcePolicyProject,
		dest:                       dest,
		destCluster:                destCluster,
		projectClusters:            projectClusters,
		projectScopedCheckDeferred: projectScopedCheckDeferred,
		offline:                    offline,
		offlineApplies:             offlineApplies,
	}, nil
}

func (v *renderedResourcePolicyValidator) validate(resources []RenderedResource) []diagnostic.Diagnostic {
	diags := make([]diagnostic.Diagnostic, 0)
	for _, resource := range resources {
		if diag, ok := v.validateResource(resource); ok {
			diags = append(diags, diag)
		}
	}
	return dedupeDiagnostics(diags)
}

func (v *renderedResourcePolicyValidator) validateResource(resource RenderedResource) (diagnostic.Diagnostic, bool) {
	obj := resource.Object
	if obj == nil {
		return diagnostic.Diagnostic{}, false
	}

	groupKind := obj.GroupVersionKind().GroupKind()
	name := obj.GetName()
	scope := v.renderedResourcePolicyScope(resource)
	if scope.deferred {
		return v.validateUnknownScopeResource(resource)
	}

	if !v.resourcePolicyProject.IsGroupKindNamePermitted(groupKind, name, scope.namespaced) {
		return resourcePolicyDeniedWarning(v.app, resource, v.resourcePolicyProject), true
	}

	if !scope.namespaced {
		return diagnostic.Diagnostic{}, false
	}

	return v.validateNamespacedResource(resource, scope)
}

func (v *renderedResourcePolicyValidator) validateUnknownScopeResource(resource RenderedResource) (diagnostic.Diagnostic, bool) {
	obj := resource.Object
	namespace := renderedResourceNamespace(v.app, obj)
	namespaced := v.evaluateResourceScope(obj, true, namespace)
	clusterScoped := v.evaluateResourceScope(obj, false, "")

	switch {
	case namespaced == resourcePolicyOutcomeAllowed && clusterScoped == resourcePolicyOutcomeAllowed:
		return diagnostic.Diagnostic{}, false
	case namespaced == resourcePolicyOutcomeDenied && clusterScoped == resourcePolicyOutcomeDenied:
		return resourcePolicyDeniedWarning(v.app, resource, v.resourcePolicyProject), true
	default:
		return resourcePolicyDeferredWarning(v.app, fmt.Sprintf("Application %s %s %s has unknown scope offline; AppProject resource policy validation is deferred", applicationName(v.app), renderedResourceNoun(resource), renderedResourceDescription(obj))), true
	}
}

func (v *renderedResourcePolicyValidator) evaluateResourceScope(obj *unstructured.Unstructured, namespaced bool, namespace string) resourcePolicyOutcome {
	groupKind := obj.GroupVersionKind().GroupKind()
	name := obj.GetName()
	if !v.resourcePolicyProject.IsGroupKindNamePermitted(groupKind, name, namespaced) {
		return resourcePolicyOutcomeDenied
	}
	if !namespaced {
		return resourcePolicyOutcomeAllowed
	}

	if v.offlineApplies {
		return v.offlineResourceScopeOutcome(namespace)
	}
	if namespace == strings.TrimSpace(v.dest.Namespace) {
		return v.projectScopedOutcome()
	}

	permitted, err := v.applicationDestinationPermitted()
	if err != nil {
		return resourcePolicyOutcomeDeferred
	}
	if !permitted {
		return resourcePolicyOutcomeDenied
	}

	permitted, err = v.resourcePolicyProject.IsResourcePermitted(groupKind, name, namespace, v.destCluster, v.projectClustersForResourcePolicy)
	if err != nil {
		return resourcePolicyOutcomeDeferred
	}
	if !permitted {
		return resourcePolicyOutcomeDenied
	}
	return resourcePolicyOutcomeAllowed
}

func (v *renderedResourcePolicyValidator) projectScopedOutcome() resourcePolicyOutcome {
	if v.projectScopedCheckDeferred {
		return resourcePolicyOutcomeDeferred
	}
	return resourcePolicyOutcomeAllowed
}

func (v *renderedResourcePolicyValidator) validateNamespacedResource(resource RenderedResource, scope renderedResourceScope) (diagnostic.Diagnostic, bool) {
	obj := resource.Object
	if v.offlineApplies {
		return v.validateOfflineNamespacedResource(resource, scope.namespace)
	}
	if scope.namespace == strings.TrimSpace(v.dest.Namespace) {
		return v.projectScopedDeferralDiagnostic()
	}

	permitted, err := v.applicationDestinationPermitted()
	if err != nil {
		return v.validationErrorDiagnostic(obj, err), true
	}
	if !permitted {
		return diagnostic.Diagnostic{}, false
	}

	groupKind := obj.GroupVersionKind().GroupKind()
	permitted, err = v.resourcePolicyProject.IsResourcePermitted(groupKind, obj.GetName(), scope.namespace, v.destCluster, v.projectClustersForResourcePolicy)
	if err != nil {
		return v.validationErrorDiagnostic(obj, err), true
	}
	if !permitted {
		return resourcePolicyDestinationDeniedWarning(v.app, resource, scope.namespace, v.resourcePolicyProject), true
	}
	return diagnostic.Diagnostic{}, false
}

func (v *renderedResourcePolicyValidator) projectClustersForResourcePolicy(project string) ([]*argoappv1.Cluster, error) {
	if project != v.resourcePolicyProject.Name {
		return nil, nil
	}
	return v.projectClusters, nil
}

func (v *renderedResourcePolicyValidator) applicationDestinationPermitted() (bool, error) {
	if v.appDestinationChecked {
		return v.appDestinationPermitted, v.appDestinationPermittedErr
	}
	v.appDestinationChecked = true
	v.appDestinationPermitted, v.appDestinationPermittedErr = v.resourcePolicyProject.IsDestinationPermitted(v.destCluster, v.dest.Namespace, v.projectClustersForResourcePolicy)
	return v.appDestinationPermitted, v.appDestinationPermittedErr
}

// offlineNamespacePermitted evaluates the unresolved destination in
// namespace offline (offlineDestinationPermitted).
func (v *renderedResourcePolicyValidator) offlineNamespacePermitted(namespace string) tristate {
	return offlineDestinationPermitted(v.resourcePolicyProject, v.offline, namespace)
}

// offlineResourceScopeOutcome is evaluateResourceScope's namespaced outcome
// in namespace for an unresolved destination: a denied rendered namespace is
// denied, or deferred while the Application destination itself is.
func (v *renderedResourcePolicyValidator) offlineResourceScopeOutcome(namespace string) resourcePolicyOutcome {
	switch v.offlineNamespacePermitted(namespace) {
	case tristateTrue:
		return v.projectScopedOutcome()
	case tristateUnknown:
		return resourcePolicyOutcomeDeferred
	case tristateFalse:
	}
	if v.offlineNamespacePermitted(v.dest.Namespace) == tristateUnknown {
		return resourcePolicyOutcomeDeferred
	}
	return resourcePolicyOutcomeDenied
}

// validateOfflineNamespacedResource is validateNamespacedResource for an
// unresolved destination. Argo CD reports the rendered object's namespace
// only when the Application destination itself is permitted;
// validateDestination reports a denied or deferred Application destination.
func (v *renderedResourcePolicyValidator) validateOfflineNamespacedResource(resource RenderedResource, namespace string) (diagnostic.Diagnostic, bool) {
	switch v.offlineNamespacePermitted(namespace) {
	case tristateTrue:
		return v.projectScopedDeferralDiagnostic()
	case tristateUnknown:
		return v.offlineDeferralDiagnostic()
	case tristateFalse:
	}
	if v.offlineNamespacePermitted(v.dest.Namespace) != tristateTrue {
		return diagnostic.Diagnostic{}, false
	}
	return resourcePolicyDestinationDeniedWarning(v.app, resource, namespace, v.resourcePolicyProject), true
}

func (v *renderedResourcePolicyValidator) offlineDeferralDiagnostic() (diagnostic.Diagnostic, bool) {
	if v.offlineDeferralReported {
		return diagnostic.Diagnostic{}, false
	}
	v.offlineDeferralReported = true
	return projectWarning(v.app, unresolvedDestinationDeferredMessage(v.app, v.offline)), true
}

func (v *renderedResourcePolicyValidator) projectScopedDeferralDiagnostic() (diagnostic.Diagnostic, bool) {
	if !v.projectScopedCheckDeferred || v.projectScopedDeferralReported {
		return diagnostic.Diagnostic{}, false
	}
	v.projectScopedDeferralReported = true
	return projectWarning(v.app, fmt.Sprintf("AppProject %q enables permitOnlyProjectScopedClusters; project-scoped cluster Secrets enforcement is deferred offline", v.resourcePolicyProject.Name)), true
}

func (v *renderedResourcePolicyValidator) validationErrorDiagnostic(obj *unstructured.Unstructured, err error) diagnostic.Diagnostic {
	return projectWarning(v.app, fmt.Sprintf("Application %s rendered resource %s could not be validated against AppProject %q: %v", applicationName(v.app), renderedResourceDescription(obj), v.resourcePolicyProject.Name, err))
}

func renderedResourcePolicyProject(app argoappv1.Application, projects []argoappv1.AppProject, settings config.ArgoSettings) (argoappv1.AppProject, bool) {
	projectName := applicationProject(app)
	index := ByName(projects)
	proj, ok := index[projectName]
	if !ok {
		if projectName == argoappv1.DefaultAppProjectName || len(projects) == 0 {
			return effectiveProject(implicitDefaultProject(), settings), true
		}
		return argoappv1.AppProject{}, false
	}
	return effectiveProject(proj, settings), true
}

func (v *renderedResourcePolicyValidator) renderedResourcePolicyScope(resource RenderedResource) renderedResourceScope {
	obj := resource.Object
	gvk := obj.GroupVersionKind()
	if manifest.IsBuiltInClusterScoped(gvk) {
		return renderedResourceScope{}
	}
	if manifest.IsKnownNamespacedBuiltIn(gvk) {
		return renderedResourceScope{namespaced: true, namespace: renderedResourceNamespace(v.app, obj)}
	}
	if scope, ok := v.crdScope.Scope(gvk); ok {
		if scope == manifest.CRDScopeCluster {
			return renderedResourceScope{}
		}
		return renderedResourceScope{namespaced: true, namespace: renderedResourceNamespace(v.app, obj)}
	}
	if strings.TrimSpace(resource.NamespaceBeforeNormalization) != "" {
		namespace := strings.TrimSpace(obj.GetNamespace())
		if namespace == "" {
			namespace = strings.TrimSpace(resource.NamespaceBeforeNormalization)
		}
		return renderedResourceScope{namespaced: true, namespace: namespace}
	}
	return renderedResourceScope{deferred: true}
}

func renderedResourceNamespace(app argoappv1.Application, obj *unstructured.Unstructured) string {
	if namespace := strings.TrimSpace(obj.GetNamespace()); namespace != "" {
		return namespace
	}
	if namespace := strings.TrimSpace(app.Spec.Destination.Namespace); namespace != "" {
		return namespace
	}
	return ""
}

func resourcePolicyDeniedWarning(app argoappv1.Application, resource RenderedResource, proj argoappv1.AppProject) diagnostic.Diagnostic {
	diag := projectWarning(app, fmt.Sprintf("Application %s %s %s is not permitted by AppProject %q", applicationName(app), renderedResourceNoun(resource), renderedResourceDescription(resource.Object), proj.Name))
	diag.Code = projectResourceDeniedCode
	return diag
}

func resourcePolicyDestinationDeniedWarning(app argoappv1.Application, resource RenderedResource, namespace string, proj argoappv1.AppProject) diagnostic.Diagnostic {
	diag := projectWarning(app, fmt.Sprintf("Application %s %s %s namespace %q is not permitted by AppProject %q", applicationName(app), renderedResourceNoun(resource), renderedResourceDescription(resource.Object), namespace, proj.Name))
	diag.Code = projectResourceDestinationDeniedCode
	return diag
}

// renderedResourceNoun names a rendered object in diagnostics. Hooks are
// called out so a denial reads the way Argo CD reports one.
func renderedResourceNoun(resource RenderedResource) string {
	if resource.Hook {
		return "rendered hook"
	}
	return "rendered resource"
}

func resourcePolicyDeferredWarning(app argoappv1.Application, message string) diagnostic.Diagnostic {
	diag := projectWarning(app, message)
	diag.Code = projectResourceScopeDeferredCode
	return diag
}

func renderedResourceDescription(obj *unstructured.Unstructured) string {
	groupKind := obj.GetKind()
	if group := obj.GroupVersionKind().Group; group != "" {
		groupKind = group + "/" + obj.GetKind()
	}
	name := strings.TrimSpace(obj.GetName())
	if name == "" {
		name = "<unnamed>"
	}
	namespace := strings.TrimSpace(obj.GetNamespace())
	if namespace != "" {
		return fmt.Sprintf("%s %s/%s", groupKind, namespace, name)
	}
	return fmt.Sprintf("%s %s", groupKind, name)
}
