package app

import (
	"fmt"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/manifest"
	"github.com/sholdee/drydock/internal/project"
)

// resourceSelectorMismatchDiagnostics reports every rendered resource and
// hook that survives the resource exclusions and inclusions but whose labels
// do not match the argocd-cm resource.selectors selector for its group,
// kind, and cluster. Argo CD keeps such a target object managed and adds an
// ExcludedResourceWarning condition (controller/state.go CompareAppState at
// v3.6.1); the message embeds that condition's text. The resource stays in
// the render and diff output.
//
// Argo CD matches the rule cluster globs against the resolved destination
// server (GetLabelSelector(..., destCluster.Server)), so the destination is
// resolved first (project.ResolveDestinationServer); when it cannot be
// resolved offline, or Argo CD would fail the comparison before matching
// selectors (an unknown or ambiguous name, a disabled in-cluster cluster),
// no selector warning is reported.
func resourceSelectorMismatchDiagnostics(application argoappv1.Application, resources []project.RenderedResource, filter manifest.SettingsResourceFilter, settings config.ArgoSettings) []diagnostic.Diagnostic {
	if len(filter.Selectors) == 0 {
		return nil
	}
	cluster, ok := project.ResolveDestinationServer(application.Spec.Destination, settings)
	if !ok {
		return nil
	}
	var diags []diagnostic.Diagnostic
	for _, resource := range resources {
		selector, mismatch := filter.SelectorMismatch(resource.Object, cluster)
		if !mismatch {
			continue
		}
		gvk := resource.Object.GroupVersionKind()
		diags = append(diags, diagnostic.Diagnostic{
			Code:     diagnostic.CodeSettingsResourceSelectorMismatch,
			Severity: diagnostic.SeverityWarning,
			Category: "settings",
			Message: fmt.Sprintf("Application %s: Resource %s/%s %s does not match the resource selector %q in the settings and will not be watched; it will appear as OutOfSync",
				applicationName(application), gvk.Group, gvk.Kind, resource.Object.GetName(), selector),
		})
	}
	return diags
}
