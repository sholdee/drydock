package app

import (
	"strings"

	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/render"
)

func ociChartRepositoriesFromSettings(settings config.ArgoSettings) map[string]bool {
	repositories := make(map[string]bool)
	for _, repo := range settings.HelmRepositories {
		if !repo.EnableOCI {
			continue
		}
		repoType := strings.TrimSpace(repo.Type)
		if repoType != "" && repoType != "helm" {
			continue
		}
		canonical, ok := render.CanonicalOCIChartRepository(repo.URL)
		if !ok {
			continue
		}
		repositories[canonical] = true
	}
	return repositories
}
