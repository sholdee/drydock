package config

import (
	"fmt"
	"reflect"

	"github.com/sholdee/drydock/internal/diagnostic"
)

func MergeDiscovered(candidates []ArgoSettings) (ArgoSettings, []diagnostic.Diagnostic) {
	merged := DefaultSettings()
	diags := make([]diagnostic.Diagnostic, 0, len(candidates))

	for _, candidate := range candidates {
		diags = append(diags, mergeKustomizeBuildOptions(&merged, candidate)...)
		diags = append(diags, mergeTrackingMethod(&merged, candidate)...)
		diags = append(diags, mergeInstanceLabelKey(&merged, candidate)...)
		diags = append(diags, mergeHelmRepositories(&merged, candidate)...)
		diags = append(diags, mergeCompareOptions(&merged, candidate)...)
		diags = append(diags, mergeIgnoreResourceUpdatesEnabled(&merged, candidate)...)
		merged.ResourceExclusions = append(merged.ResourceExclusions, candidate.ResourceExclusions...)
		merged.ResourceInclusions = append(merged.ResourceInclusions, candidate.ResourceInclusions...)
		diags = append(diags, mergeResourceCustomizations(&merged, candidate)...)
	}

	return merged, diags
}

func mergeKustomizeBuildOptions(merged *ArgoSettings, candidate ArgoSettings) []diagnostic.Diagnostic {
	if len(candidate.KustomizeBuildOptions) == 0 {
		return nil
	}
	if len(merged.KustomizeBuildOptions) > 0 && !reflect.DeepEqual(valuesOnly(merged.KustomizeBuildOptions), valuesOnly(candidate.KustomizeBuildOptions)) {
		return []diagnostic.Diagnostic{conflictDiagnostic(
			"conflicting kustomize.buildOptions discovered; pass --argocd-cm or --argocd-values",
			firstValueProvenance(candidate.KustomizeBuildOptions),
		)}
	}
	merged.KustomizeBuildOptions = candidate.KustomizeBuildOptions
	return nil
}

func mergeTrackingMethod(merged *ArgoSettings, candidate ArgoSettings) []diagnostic.Diagnostic {
	if !hasProvenance(candidate.TrackingMethod) {
		return nil
	}
	if hasProvenance(merged.TrackingMethod) && merged.TrackingMethod.Value != candidate.TrackingMethod.Value {
		return []diagnostic.Diagnostic{conflictDiagnostic(
			fmt.Sprintf("conflicting application.resourceTrackingMethod values %q and %q", merged.TrackingMethod.Value, candidate.TrackingMethod.Value),
			candidate.TrackingMethod.Provenance,
		)}
	}
	merged.TrackingMethod = candidate.TrackingMethod
	return nil
}

func mergeInstanceLabelKey(merged *ArgoSettings, candidate ArgoSettings) []diagnostic.Diagnostic {
	if !hasProvenance(candidate.InstanceLabelKey) {
		return nil
	}
	if hasProvenance(merged.InstanceLabelKey) && merged.InstanceLabelKey.Value != candidate.InstanceLabelKey.Value {
		return []diagnostic.Diagnostic{conflictDiagnostic(
			fmt.Sprintf("conflicting application.instanceLabelKey values %q and %q", merged.InstanceLabelKey.Value, candidate.InstanceLabelKey.Value),
			candidate.InstanceLabelKey.Provenance,
		)}
	}
	merged.InstanceLabelKey = candidate.InstanceLabelKey
	return nil
}

func mergeHelmRepositories(merged *ArgoSettings, candidate ArgoSettings) []diagnostic.Diagnostic {
	var diags []diagnostic.Diagnostic
	for url, repo := range candidate.HelmRepositories {
		existing, ok := merged.HelmRepositories[url]
		if ok && !sameRepositorySettings(existing, repo) {
			diags = append(diags, conflictDiagnostic(
				fmt.Sprintf("conflicting repository settings discovered for %q", url),
				repo.Provenance,
			))
			continue
		}
		merged.HelmRepositories[url] = repo
	}
	return diags
}

func mergeCompareOptions(merged *ArgoSettings, candidate ArgoSettings) []diagnostic.Diagnostic {
	if !hasProvenanceCompareOptions(candidate.CompareOptions) {
		return nil
	}
	if hasProvenanceCompareOptions(merged.CompareOptions) && !sameCompareOptions(merged.CompareOptions, candidate.CompareOptions) {
		return []diagnostic.Diagnostic{conflictDiagnostic(
			"conflicting resource.compareoptions settings discovered",
			candidate.CompareOptions.Provenance,
		)}
	}
	if !hasProvenanceCompareOptions(merged.CompareOptions) {
		merged.CompareOptions = candidate.CompareOptions
	}
	return nil
}

func mergeIgnoreResourceUpdatesEnabled(merged *ArgoSettings, candidate ArgoSettings) []diagnostic.Diagnostic {
	if !hasProvenanceBool(candidate.IgnoreResourceUpdatesEnabled) {
		return nil
	}
	if hasProvenanceBool(merged.IgnoreResourceUpdatesEnabled) && merged.IgnoreResourceUpdatesEnabled.Value != candidate.IgnoreResourceUpdatesEnabled.Value {
		return []diagnostic.Diagnostic{conflictDiagnostic(
			fmt.Sprintf("conflicting resource.ignoreResourceUpdatesEnabled values %t and %t", merged.IgnoreResourceUpdatesEnabled.Value, candidate.IgnoreResourceUpdatesEnabled.Value),
			candidate.IgnoreResourceUpdatesEnabled.Provenance,
		)}
	}
	if !hasProvenanceBool(merged.IgnoreResourceUpdatesEnabled) {
		merged.IgnoreResourceUpdatesEnabled = candidate.IgnoreResourceUpdatesEnabled
	}
	return nil
}

func mergeResourceCustomizations(merged *ArgoSettings, candidate ArgoSettings) []diagnostic.Diagnostic {
	var diags []diagnostic.Diagnostic
	for key, customization := range candidate.ResourceCustomizations {
		existing, ok := merged.ResourceCustomizations[key]
		if !ok {
			merged.ResourceCustomizations[key] = customization
			continue
		}
		next, ok := mergeResourceCustomizationSections(existing, customization)
		if !ok {
			diags = append(diags, conflictDiagnostic(
				fmt.Sprintf("conflicting resource customization settings discovered for %q", key),
				customization.Provenance,
			))
			continue
		}
		merged.ResourceCustomizations[key] = next
	}
	return diags
}

func valuesOnly(values []Value[string]) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, value.Value)
	}
	return out
}

func firstValueProvenance(values []Value[string]) diagnostic.Provenance {
	if len(values) == 0 {
		return diagnostic.Provenance{}
	}
	return values[0].Provenance
}

func hasProvenance(value Value[string]) bool {
	return value.Provenance.Path != "" || value.Provenance.Pointer != ""
}

func hasProvenanceBool(value Value[bool]) bool {
	return value.Provenance.Path != "" || value.Provenance.Pointer != ""
}

func hasProvenanceCompareOptions(value ResourceCompareOptions) bool {
	return value.Provenance.Path != "" || value.Provenance.Pointer != ""
}

func sameRepositorySettings(left, right RepositorySettings) bool {
	return left.Name == right.Name &&
		left.Type == right.Type &&
		left.URL == right.URL &&
		left.EnableOCI == right.EnableOCI &&
		left.Project == right.Project
}

func sameCompareOptions(left, right ResourceCompareOptions) bool {
	return left.IgnoreAggregatedRoles == right.IgnoreAggregatedRoles &&
		left.IgnoreResourceStatusField == right.IgnoreResourceStatusField
}

func conflictDiagnostic(message string, provenance diagnostic.Provenance) diagnostic.Diagnostic {
	return diagnostic.Diagnostic{
		Severity:   diagnostic.SeverityError,
		Category:   "settings",
		Message:    message,
		Provenance: provenance,
	}
}
