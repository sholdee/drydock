package app

import (
	"path"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
)

// SelectChangedApplications returns Applications whose declared local source
// paths intersect at least one changed path, plus normalized unowned changes.
func SelectChangedApplications(apps []argoappv1.Application, changedPaths []string) ([]argoappv1.Application, []string) {
	appPaths := make([][]string, len(apps))
	for i, app := range apps {
		appPaths[i] = applicationSourcePaths(app)
	}

	selectedIndexes := make(map[int]struct{})
	var unowned []string

	for _, changedPath := range changedPaths {
		normalizedChanged := normalizeSelectPath(changedPath)
		owned := false
		for appIndex, sourcePaths := range appPaths {
			for _, sourcePath := range sourcePaths {
				if pathIntersects(sourcePath, normalizedChanged) {
					selectedIndexes[appIndex] = struct{}{}
					owned = true
				}
			}
		}
		if !owned {
			unowned = append(unowned, normalizedChanged)
		}
	}

	selected := make([]argoappv1.Application, 0, len(selectedIndexes))
	for i, app := range apps {
		if _, ok := selectedIndexes[i]; ok {
			selected = append(selected, app)
		}
	}

	return selected, unowned
}

func applicationSourcePaths(app argoappv1.Application) []string {
	sources := app.Spec.Sources
	if len(sources) == 0 && app.Spec.Source != nil {
		sources = argoappv1.ApplicationSources{*app.Spec.Source}
	}

	paths := make([]string, 0, len(sources))
	for _, source := range sources {
		sourcePath, ok := localSourcePath(source)
		if ok {
			paths = append(paths, sourcePath)
		}
	}
	return paths
}

func localSourcePath(source argoappv1.ApplicationSource) (string, bool) {
	if source.Path != "" {
		return normalizeSelectPath(source.Path), true
	}
	if source.Chart != "" || source.Ref != "" || source.RepoURL == "" {
		return "", false
	}
	return "", true
}

func pathIntersects(sourcePath, changedPath string) bool {
	if sourcePath == "" {
		return true
	}
	return changedPath == sourcePath || strings.HasPrefix(changedPath, sourcePath+"/")
}

func normalizeSelectPath(p string) string {
	cleaned := path.Clean(strings.Trim(p, "/"))
	if cleaned == "." {
		return ""
	}
	return cleaned
}
