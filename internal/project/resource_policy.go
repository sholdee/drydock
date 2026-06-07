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
}

type renderedResourcePolicyValidator struct {
	app                           argoappv1.Application
	proj                          argoappv1.AppProject
	resourcePolicyProject         argoappv1.AppProject
	dest                          argoappv1.ApplicationDestination
	destCluster                   *argoappv1.Cluster
	destinationClusterKnown       bool
	projectClusters               []*argoappv1.Cluster
	projectScopedCheckDeferred    bool
	projectScopedDeferralReported bool
	nameOnlyDeferralReported      bool
	appDestinationChecked         bool
	appDestinationPermitted       bool
	appDestinationPermittedErr    error
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
	if len(resources) == 0 {
		return nil
	}

	validator, diags := newRenderedResourcePolicyValidator(app, projects, settings)
	if len(diags) > 0 {
		return diags
	}
	return validator.validate(resources)
}

func newRenderedResourcePolicyValidator(app argoappv1.Application, projects []argoappv1.AppProject, settings config.ArgoSettings) (*renderedResourcePolicyValidator, []diagnostic.Diagnostic) {
	proj, ok := renderedResourcePolicyProject(app, projects, settings)
	if !ok {
		return nil, []diagnostic.Diagnostic{projectWarning(app, fmt.Sprintf("Application %s references missing AppProject %q", applicationName(app), applicationProject(app)))}
	}

	dest := app.Spec.Destination
	destCluster, destinationClusterKnown := destinationCluster(dest, settings)
	projectClusters, projectClusterMetadataAvailable := projectScopedClusters(proj.Name, settings)
	projectScopedCheckAvailable := projectClusterMetadataAvailable || destinationClusterKnown

	resourcePolicyProject := proj
	projectScopedCheckDeferred := resourcePolicyProject.Spec.PermitOnlyProjectScopedClusters && !projectScopedCheckAvailable
	if projectScopedCheckDeferred {
		resourcePolicyProject.Spec.PermitOnlyProjectScopedClusters = false
	}

	return &renderedResourcePolicyValidator{
		app:                           app,
		proj:                          proj,
		resourcePolicyProject:         resourcePolicyProject,
		dest:                          dest,
		destCluster:                   destCluster,
		destinationClusterKnown:       destinationClusterKnown,
		projectClusters:               projectClusters,
		projectScopedCheckDeferred:    projectScopedCheckDeferred,
		projectScopedDeferralReported: false,
		nameOnlyDeferralReported:      false,
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
	scope := renderedResourcePolicyScope(v.app, resource)
	if scope.deferred {
		return v.validateUnknownScopeResource(obj)
	}

	if !v.resourcePolicyProject.IsGroupKindNamePermitted(groupKind, name, scope.namespaced) {
		return resourcePolicyDeniedWarning(v.app, obj, v.resourcePolicyProject), true
	}

	if !scope.namespaced {
		return diagnostic.Diagnostic{}, false
	}

	return v.validateNamespacedResource(obj, scope)
}

func (v *renderedResourcePolicyValidator) validateUnknownScopeResource(obj *unstructured.Unstructured) (diagnostic.Diagnostic, bool) {
	namespace := renderedResourceNamespace(v.app, obj)
	namespaced := v.evaluateResourceScope(obj, true, namespace)
	clusterScoped := v.evaluateResourceScope(obj, false, "")

	switch {
	case namespaced == resourcePolicyOutcomeAllowed && clusterScoped == resourcePolicyOutcomeAllowed:
		return diagnostic.Diagnostic{}, false
	case namespaced == resourcePolicyOutcomeDenied && clusterScoped == resourcePolicyOutcomeDenied:
		return resourcePolicyDeniedWarning(v.app, obj, v.resourcePolicyProject), true
	default:
		return resourcePolicyDeferredWarning(v.app, fmt.Sprintf("Application %s rendered resource %s has unknown scope offline; AppProject resource policy validation is deferred", applicationName(v.app), renderedResourceDescription(obj))), true
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

	if resourcePolicyNameOnlyDestinationDeferred(v.dest, namespace, v.proj, v.destinationClusterKnown) {
		return resourcePolicyOutcomeDeferred
	}
	if resourcePolicyNameOnlyDestinationPermittedByWildcardServer(v.dest, namespace, v.proj, v.destinationClusterKnown) {
		return v.projectScopedOutcome()
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

func (v *renderedResourcePolicyValidator) validateNamespacedResource(obj *unstructured.Unstructured, scope renderedResourceScope) (diagnostic.Diagnostic, bool) {
	if resourcePolicyNameOnlyDestinationDeferred(v.dest, scope.namespace, v.proj, v.destinationClusterKnown) {
		return v.nameOnlyDestinationDeferralDiagnostic()
	}
	if resourcePolicyNameOnlyDestinationPermittedByWildcardServer(v.dest, scope.namespace, v.proj, v.destinationClusterKnown) {
		return v.projectScopedDeferralDiagnostic()
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
		return resourcePolicyDestinationDeniedWarning(v.app, obj, scope.namespace, v.resourcePolicyProject), true
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

func (v *renderedResourcePolicyValidator) nameOnlyDestinationDeferralDiagnostic() (diagnostic.Diagnostic, bool) {
	if v.nameOnlyDeferralReported {
		return diagnostic.Diagnostic{}, false
	}
	v.nameOnlyDeferralReported = true
	return projectWarning(v.app, fmt.Sprintf("Application %s destination name %q cannot be resolved against AppProject server policy offline", applicationName(v.app), v.dest.Name)), true
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
	index := projectIndex(projects)
	proj, ok := index[projectName]
	if !ok {
		if projectName == argoappv1.DefaultAppProjectName || len(projects) == 0 {
			return effectiveProject(implicitDefaultProject(), settings), true
		}
		return argoappv1.AppProject{}, false
	}
	return effectiveProject(proj, settings), true
}

func renderedResourcePolicyScope(app argoappv1.Application, resource RenderedResource) renderedResourceScope {
	obj := resource.Object
	gvk := obj.GroupVersionKind()
	if manifest.IsBuiltInClusterScoped(gvk) {
		return renderedResourceScope{}
	}
	if manifest.IsKnownNamespacedBuiltIn(gvk) {
		return renderedResourceScope{namespaced: true, namespace: renderedResourceNamespace(app, obj)}
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

func resourcePolicyNameOnlyDestinationDeferred(dest argoappv1.ApplicationDestination, namespace string, proj argoappv1.AppProject, destinationClusterKnown bool) bool {
	if destinationClusterKnown || !isNameOnlyDestination(dest) {
		return false
	}
	renderedDest := dest
	renderedDest.Namespace = namespace
	if nameOnlyDestinationExplicitlyDenied(renderedDest, proj) {
		return false
	}
	if nameOnlyDestinationHasServerDenyPolicy(renderedDest, proj) {
		return true
	}
	if nameOnlyDestinationHasServerScopedNamespaceDenyPolicy(renderedDest, proj) {
		return true
	}
	if nameOnlyDestinationPermittedByWildcardServer(renderedDest, proj) {
		return false
	}
	return nameOnlyDestinationHasServerSpecificPolicy(renderedDest, proj)
}

func resourcePolicyNameOnlyDestinationPermittedByWildcardServer(dest argoappv1.ApplicationDestination, namespace string, proj argoappv1.AppProject, destinationClusterKnown bool) bool {
	if destinationClusterKnown || !isNameOnlyDestination(dest) {
		return false
	}
	renderedDest := dest
	renderedDest.Namespace = namespace
	if nameOnlyDestinationExplicitlyDenied(renderedDest, proj) {
		return false
	}
	return nameOnlyDestinationPermittedByWildcardServer(renderedDest, proj)
}

func resourcePolicyDeniedWarning(app argoappv1.Application, obj *unstructured.Unstructured, proj argoappv1.AppProject) diagnostic.Diagnostic {
	diag := projectWarning(app, fmt.Sprintf("Application %s rendered resource %s is not permitted by AppProject %q", applicationName(app), renderedResourceDescription(obj), proj.Name))
	diag.Code = projectResourceDeniedCode
	return diag
}

func resourcePolicyDestinationDeniedWarning(app argoappv1.Application, obj *unstructured.Unstructured, namespace string, proj argoappv1.AppProject) diagnostic.Diagnostic {
	diag := projectWarning(app, fmt.Sprintf("Application %s rendered resource %s namespace %q is not permitted by AppProject %q", applicationName(app), renderedResourceDescription(obj), namespace, proj.Name))
	diag.Code = projectResourceDestinationDeniedCode
	return diag
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
