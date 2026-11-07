package project

import (
	"errors"
	"fmt"
	"regexp"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
)

// syncSourcePathPattern is the CRD pattern of
// spec.sourceHydrator.syncSource.path
// (pkg/apis/application/v1alpha1/types.go:510-512 at v3.6.1). The API
// server searches for it, so a path matches with two or more characters, or
// with a last character that is neither "." nor "/".
const syncSourcePathPattern = `^.{2,}|[^./]$`

var syncSourcePathRegexp = regexp.MustCompile(syncSourcePathPattern)

// ValidateSpecSources mirrors the source checks that the Argo CD v3.6.1
// controller runs before it compares an Application (ValidatePermissions,
// util/argo/argo.go:644-737). A failed check sets an InvalidSpecError
// condition, and the controller then skips the comparison
// (controller/appcontroller.go:2016-2019). The messages keep the upstream
// text. It first runs the one CRD check that the API server applies to
// these fields and that a parsed manifest can fail: the schema of
// spec.sourceHydrator.syncSource.path. Argo CD never holds an Application
// that fails it.
func ValidateSpecSources(spec argoappv1.ApplicationSpec) error {
	switch {
	case spec.SourceHydrator != nil:
		if err := validateSyncSourcePathSchema(spec.SourceHydrator.SyncSource.Path); err != nil {
			return err
		}
		return validateSourceHydrator(*spec.SourceHydrator)
	case spec.HasMultipleSources():
		for i, source := range spec.Sources {
			if err := validateSpecSource(source, true); err != nil {
				return fmt.Errorf("sources[%d]: %w", i, err)
			}
		}
		return nil
	default:
		return validateSpecSource(spec.GetSource(), false)
	}
}

// validateSyncSourcePathSchema applies the CRD schema of
// spec.sourceHydrator.syncSource.path (minLength 1 and
// syncSourcePathPattern), with the API server's message.
func validateSyncSourcePathSchema(path string) error {
	const field = "spec.sourceHydrator.syncSource.path"
	var errs []error
	if path == "" {
		errs = append(errs, fmt.Errorf("invalid spec: %s: Invalid value: %q: %s in body should be at least 1 chars long", field, path, field))
	}
	if !syncSourcePathRegexp.MatchString(path) {
		errs = append(errs, fmt.Errorf("invalid spec: %s: Invalid value: %q: %s in body should match '%s'", field, path, field, syncSourcePathPattern))
	}
	return errors.Join(errs...)
}

// validateSpecSource is validateSourcePermissions (util/argo/argo.go:644-672
// at v3.6.1). A source of a multi-source Application may set only ref; a
// single source must set path or chart. A chart needs a targetRevision.
func validateSpecSource(source argoappv1.ApplicationSource, hasMultipleSources bool) error {
	if hasMultipleSources {
		if source.RepoURL == "" || (source.Path == "" && source.Chart == "" && source.Ref == "") {
			return errors.New("invalid spec: spec.source.repoURL and either source.path, source.chart, or source.ref are required")
		}
	} else if source.RepoURL == "" || (source.Path == "" && source.Chart == "") {
		return errors.New("invalid spec: spec.source.repoURL and either spec.source.path or spec.source.chart are required")
	}
	if source.Chart != "" && source.TargetRevision == "" {
		return errors.New("invalid spec: spec.source.targetRevision is required if the manifest source is a helm chart")
	}
	return nil
}

// validateSourceHydrator is validateSourceHydrator (util/argo/argo.go:674-697
// at v3.6.1). The controller reports every failed check; drydock joins them.
func validateSourceHydrator(hydrator argoappv1.SourceHydrator) error {
	var errs []error
	if hydrator.DrySource.RepoURL == "" {
		errs = append(errs, errors.New("invalid spec: spec.sourceHydrator.drySource.repoURL is required"))
	}
	if hydrator.SyncSource.TargetBranch == "" {
		errs = append(errs, errors.New("invalid spec: spec.sourceHydrator.syncSource.targetBranch is required"))
	}
	if hydrator.HydrateTo != nil && hydrator.HydrateTo.TargetBranch == "" {
		errs = append(errs, errors.New("invalid spec: when spec.sourceHydrator.hydrateTo is set, spec.sourceHydrator.hydrateTo.targetBranch is required"))
	}
	return errors.Join(errs...)
}

// policyCheckedSources returns the sources whose repositories the AppProject
// must permit, and whether the destination is checked, in the order of
// ValidatePermissions (util/argo/argo.go:698-737 at v3.6.1). A source
// hydrator that fails its checks stops everything. In a multi-source
// Application, the first source that fails its checks stops the loop: the
// sources before it are still checked, and the destination is not. A
// single source that fails its checks stops everything.
func policyCheckedSources(spec argoappv1.ApplicationSpec) (argoappv1.ApplicationSources, bool) {
	switch {
	case spec.SourceHydrator != nil:
		if validateSyncSourcePathSchema(spec.SourceHydrator.SyncSource.Path) != nil || validateSourceHydrator(*spec.SourceHydrator) != nil {
			return nil, false
		}
		return projectCheckedSources(spec), true
	case spec.HasMultipleSources():
		for i, source := range spec.Sources {
			if validateSpecSource(source, true) != nil {
				return spec.Sources[:i], false
			}
		}
		return spec.Sources, true
	default:
		if validateSpecSource(spec.GetSource(), false) != nil {
			return nil, false
		}
		return argoappv1.ApplicationSources{spec.GetSource()}, true
	}
}
