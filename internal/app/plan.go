package app

import (
	"fmt"
	"regexp"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/ociartifact"
	"github.com/sholdee/drydock/internal/project"
)

var validRef = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

type SourcePlan struct {
	Index        int
	Name         string
	RefKey       string
	Source       argoappv1.ApplicationSource
	SourceRoot   string
	RefOnly      bool
	ExplicitType argoappv1.ApplicationSourceType
	// HydratorSync marks the sync source of an Application with
	// spec.sourceHydrator (render.ResolvedSource.HydratorSync).
	HydratorSync bool
	// AppRevision is the revision the Argo CD repo-server would report for
	// this source as ARGOCD_APP_REVISION: a commit SHA for a Git source, a
	// manifest digest for an OCI source. PrepareSource sets it. Empty means
	// drydock could not resolve one, and the build environment keeps the
	// spec targetRevision.
	AppRevision string
}

type PlanResult struct {
	Application argoappv1.Application
	Sources     []SourcePlan
	Refs        map[string]SourcePlan
}

func Plan(application argoappv1.Application) (PlanResult, error) {
	result := PlanResult{
		Application: application,
		Refs:        map[string]SourcePlan{},
	}
	sources := applicationRenderSources(application.Spec)
	for i, source := range sources {
		sourcePlan := SourcePlan{
			Index:        i,
			Name:         source.Name,
			Source:       source,
			RefOnly:      source.Ref != "" && source.Path == "" && source.Chart == "",
			HydratorSync: application.Spec.SourceHydrator != nil,
		}

		if source.Ref != "" {
			if !validRef.MatchString(source.Ref) {
				return result, fmt.Errorf("sources[%d].ref %q contains unsupported characters", i, source.Ref)
			}
			// An oci:// entry that sets both ref and chart still dispatches as
			// an OCI artifact source upstream (repository.go:358-360 at
			// v3.5.4) and renders; Argo CD rejects the combination only when
			// another source consumes the ref (repository.go:595-597). Only
			// the schemeless/HTTP chart shape is rejected here; consuming an
			// oci:// entry's $ref stays rejected by the OCI gate in
			// resolveRefRoots.
			if source.Chart != "" && !ociartifact.IsOCIURL(source.RepoURL) {
				return result, fmt.Errorf("sources[%d] cannot set both ref and chart", i)
			}
			sourcePlan.RefKey = "$" + source.Ref
			if _, exists := result.Refs[sourcePlan.RefKey]; exists {
				return result, fmt.Errorf("duplicate source ref %s", sourcePlan.RefKey)
			}
		}

		if !sourcePlan.RefOnly {
			explicitType, err := source.ExplicitType()
			if err != nil {
				return result, fmt.Errorf("sources[%d]: %w", i, err)
			}
			if explicitType != nil {
				sourcePlan.ExplicitType = *explicitType
			}
		}

		if sourcePlan.RefKey != "" {
			result.Refs[sourcePlan.RefKey] = sourcePlan
		}

		result.Sources = append(result.Sources, sourcePlan)
	}

	return result, nil
}

// applicationRenderSources returns the sources the Argo CD v3.6.1 controller
// compares (controller/appcontroller.go:2050-2072 and
// ApplicationSpec.GetSources, pkg/apis/application/v1alpha1/types.go:278).
// With spec.sourceHydrator set, that is only the sync source: the hydrated
// branch and path, with the repository of the dry source when the sync
// source names none. spec.source and spec.sources are then ignored.
// Otherwise spec.sources wins over spec.source.
func applicationRenderSources(spec argoappv1.ApplicationSpec) argoappv1.ApplicationSources {
	if spec.SourceHydrator != nil {
		return argoappv1.ApplicationSources{spec.SourceHydrator.GetSyncSource()}
	}
	if len(spec.Sources) > 0 {
		return spec.Sources
	}
	if spec.Source != nil {
		return argoappv1.ApplicationSources{*spec.Source}
	}
	return nil
}

// validateApplicationSpecSources runs the Argo CD spec checks of
// project.ValidateSpecSources. A failure makes the Argo CD controller skip
// the comparison, so drydock fails the render (renderApplicationCached).
// Plan does not run these checks: drydock plans the Applications it makes
// itself, such as a plugin policy bootstrap entrypoint, with an empty
// repoURL that names the local tree.
func validateApplicationSpecSources(spec argoappv1.ApplicationSpec) error {
	return project.ValidateSpecSources(spec)
}
