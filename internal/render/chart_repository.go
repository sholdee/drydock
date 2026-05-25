package render

import (
	"strings"

	"github.com/sholdee/drydock/internal/chart"
)

func ChartRepositoryKind(repository string, ociRepositories map[string]bool) chart.RepositoryKind {
	if strings.HasPrefix(strings.TrimSpace(repository), "oci://") {
		return chart.RepositoryOCI
	}
	if OCIChartRepositoryEnabled(repository, ociRepositories) {
		return chart.RepositoryOCI
	}
	return chart.RepositoryHTTP
}

func OCIChartRepositoryEnabled(repository string, repositories map[string]bool) bool {
	canonical, ok := CanonicalOCIChartRepository(repository)
	if !ok {
		return false
	}
	if repositories[canonical] {
		return true
	}
	for candidate, enabled := range repositories {
		if !enabled {
			continue
		}
		candidateCanonical, ok := CanonicalOCIChartRepository(candidate)
		if ok && candidateCanonical == canonical {
			return true
		}
	}
	return false
}

func CanonicalOCIChartRepository(repository string) (string, bool) {
	normalized, err := chart.NormalizeRepository(repository, chart.RepositoryOCI)
	if err != nil {
		return "", false
	}
	return normalized, true
}
