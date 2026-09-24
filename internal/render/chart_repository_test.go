package render

import (
	"testing"

	"github.com/sholdee/drydock/internal/chart"
)

func TestChartRepositoryKindTreatsBareRepositoriesAsOCI(t *testing.T) {
	for _, repo := range []string{
		"ghcr.io/grafana/helm-charts",
		"mirror.gcr.io/envoyproxy",
		// The Argo CD parity fixture shape: a scheme-less host:port whose
		// chart name is nested (parity/nested/parity-nested-chart).
		"argocd-parity-registry.argocd-parity.svc.cluster.local:5443",
	} {
		if got := ChartRepositoryKind(repo, nil); got != chart.RepositoryOCI {
			t.Fatalf("ChartRepositoryKind(%q) = %q, want OCI", repo, got)
		}
	}
}

func TestChartRepositoryKindKeepsHTTPSRepositoriesHTTP(t *testing.T) {
	if got := ChartRepositoryKind("https://charts.example.test", nil); got != chart.RepositoryHTTP {
		t.Fatalf("ChartRepositoryKind() = %q, want HTTP", got)
	}
}
