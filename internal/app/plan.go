package app

import (
	"fmt"
	"regexp"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
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
}

type PlanResult struct {
	Application argoappv1.Application
	Sources     []SourcePlan
	Refs        map[string]SourcePlan
}

func Plan(application argoappv1.Application) (PlanResult, error) {
	sources := application.Spec.Sources
	if len(sources) == 0 && application.Spec.Source != nil {
		sources = argoappv1.ApplicationSources{*application.Spec.Source}
	}

	result := PlanResult{
		Application: application,
		Refs:        map[string]SourcePlan{},
	}
	for i, source := range sources {
		sourcePlan := SourcePlan{
			Index:   i,
			Name:    source.Name,
			Source:  source,
			RefOnly: source.Ref != "" && source.Path == "" && source.Chart == "",
		}

		if source.Ref != "" {
			if !validRef.MatchString(source.Ref) {
				return result, fmt.Errorf("sources[%d].ref %q contains unsupported characters", i, source.Ref)
			}
			if source.Chart != "" {
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
