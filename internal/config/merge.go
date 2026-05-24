package config

import (
	"fmt"
	"reflect"

	"github.com/home-operations/argocd-local/internal/diagnostic"
)

func MergeDiscovered(candidates []ArgoSettings) (ArgoSettings, []diagnostic.Diagnostic) {
	merged := DefaultSettings()
	var diags []diagnostic.Diagnostic

	for _, candidate := range candidates {
		if len(candidate.KustomizeBuildOptions) > 0 {
			if len(merged.KustomizeBuildOptions) > 0 && !reflect.DeepEqual(valuesOnly(merged.KustomizeBuildOptions), valuesOnly(candidate.KustomizeBuildOptions)) {
				diags = append(diags, conflictDiagnostic(
					"conflicting kustomize.buildOptions discovered; pass --argocd-cm or --argocd-values",
					firstValueProvenance(candidate.KustomizeBuildOptions),
				))
			} else {
				merged.KustomizeBuildOptions = candidate.KustomizeBuildOptions
			}
		}

		if hasProvenance(candidate.TrackingMethod) {
			if hasProvenance(merged.TrackingMethod) && merged.TrackingMethod.Value != candidate.TrackingMethod.Value {
				diags = append(diags, conflictDiagnostic(
					fmt.Sprintf("conflicting application.resourceTrackingMethod values %q and %q", merged.TrackingMethod.Value, candidate.TrackingMethod.Value),
					candidate.TrackingMethod.Provenance,
				))
			} else {
				merged.TrackingMethod = candidate.TrackingMethod
			}
		}

		if hasProvenance(candidate.InstanceLabelKey) {
			if hasProvenance(merged.InstanceLabelKey) && merged.InstanceLabelKey.Value != candidate.InstanceLabelKey.Value {
				diags = append(diags, conflictDiagnostic(
					fmt.Sprintf("conflicting application.instanceLabelKey values %q and %q", merged.InstanceLabelKey.Value, candidate.InstanceLabelKey.Value),
					candidate.InstanceLabelKey.Provenance,
				))
			} else {
				merged.InstanceLabelKey = candidate.InstanceLabelKey
			}
		}

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

		merged.ResourceExclusions = append(merged.ResourceExclusions, candidate.ResourceExclusions...)
		merged.ResourceInclusions = append(merged.ResourceInclusions, candidate.ResourceInclusions...)
		for key, customization := range candidate.ResourceCustomizations {
			existing, ok := merged.ResourceCustomizations[key]
			if ok && !sameResourceCustomization(existing, customization) {
				diags = append(diags, conflictDiagnostic(
					fmt.Sprintf("conflicting resource customization settings discovered for %q", key),
					customization.Provenance,
				))
				continue
			}
			if !ok {
				merged.ResourceCustomizations[key] = customization
			}
		}
	}

	return merged, diags
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

func sameRepositorySettings(left, right RepositorySettings) bool {
	return left.Name == right.Name &&
		left.Type == right.Type &&
		left.URL == right.URL &&
		left.EnableOCI == right.EnableOCI &&
		left.Project == right.Project
}

func sameResourceCustomization(left, right ResourceCustomization) bool {
	return reflect.DeepEqual(left.IgnoreDifferences, right.IgnoreDifferences)
}

func conflictDiagnostic(message string, provenance diagnostic.Provenance) diagnostic.Diagnostic {
	return diagnostic.Diagnostic{
		Severity:   diagnostic.SeverityError,
		Category:   "settings",
		Message:    message,
		Provenance: provenance,
	}
}
