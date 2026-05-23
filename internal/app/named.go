package app

import (
	"fmt"
	"sort"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
)

func SelectApplicationByName(apps []argoappv1.Application, target string) (argoappv1.Application, error) {
	selected, ok, err := SelectOptionalApplicationByName(apps, target)
	if err != nil {
		return argoappv1.Application{}, err
	}
	if !ok {
		return argoappv1.Application{}, fmt.Errorf("application %q not found", strings.TrimSpace(target))
	}
	return selected, nil
}

func SelectOptionalApplicationByName(apps []argoappv1.Application, target string) (argoappv1.Application, bool, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return argoappv1.Application{}, false, fmt.Errorf("application name is required")
	}

	var matches []argoappv1.Application
	for _, candidate := range apps {
		if applicationMatchesName(candidate, target) {
			matches = append(matches, candidate)
		}
	}
	switch len(matches) {
	case 0:
		return argoappv1.Application{}, false, nil
	case 1:
		return matches[0], true, nil
	default:
		names := make([]string, 0, len(matches))
		for _, candidate := range matches {
			names = append(names, applicationDisplayName(candidate))
		}
		sort.Strings(names)
		return argoappv1.Application{}, false, fmt.Errorf("application %q matched multiple Applications; use namespace/name: %s", target, strings.Join(names, ", "))
	}
}

func applicationMatchesName(application argoappv1.Application, target string) bool {
	namespace, name, qualified := strings.Cut(target, "/")
	if qualified {
		return application.Namespace == namespace && application.Name == name
	}
	return application.Name == target
}

func applicationDisplayName(application argoappv1.Application) string {
	if application.Namespace == "" {
		return application.Name
	}
	return application.Namespace + "/" + application.Name
}
