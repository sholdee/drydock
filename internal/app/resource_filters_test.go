package app

import (
	"context"
	"path/filepath"
	"testing"
)

func writeResourceExclusionSettings(t *testing.T, root, exclusions string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "settings", "argocd-cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
data:
  resource.exclusions: |
`+exclusions)
}

// Argo CD matches resource.exclusions cluster globs against the resolved
// destination server (controller/state.go CompareAppState calls
// IsExcludedResource with destCluster.Server at v3.6.1), never against the
// destination name. When drydock cannot resolve the server offline, it drops
// a resource only when Argo CD would drop it for every server.
func TestOrchestratorBuildResourceExclusionsMatchResolvedServer(t *testing.T) {
	tests := []struct {
		name        string
		destination string
		secret      bool
		clusters    string
		wantKept    bool
	}{
		{name: "server glob matches the name's server", destination: "prod", secret: true, clusters: `["https://prod.*"]`, wantKept: false},
		{name: "name glob does not match", destination: "prod", secret: true, clusters: `["prod"]`, wantKept: true},
		{name: "implicit in-cluster server matches", destination: "in-cluster", clusters: `["https://kubernetes.default.svc"]`, wantKept: false},
		{name: "implicit in-cluster name does not match", destination: "in-cluster", clusters: `["in-cluster"]`, wantKept: true},
		{name: "unresolved name with a server glob is kept", destination: "missing", clusters: `["https://*"]`, wantKept: true},
		{name: "unresolved name with a name glob is kept", destination: "missing", clusters: `["missing"]`, wantKept: true},
		{name: "unresolved name with a star glob is dropped", destination: "missing", clusters: `["*"]`, wantKept: false},
		{name: "unresolved name with no clusters is dropped", destination: "missing", wantKept: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeBuildApplicationWithDestination(t, root, "demo", "demo-cm", tt.destination)
			if tt.secret {
				writeResourceSelectorClusterSecret(t, root, tt.destination, "https://"+tt.destination+".example.com")
			}
			rule := `    - kinds: ["ConfigMap"]
`
			if tt.clusters != "" {
				rule += `      clusters: ` + tt.clusters + `
`
			}
			writeResourceExclusionSettings(t, root, rule)

			result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root})
			if err != nil {
				t.Fatalf("Build() error = %v", err)
			}
			if kept := len(result.Manifests) == 1; kept != tt.wantKept {
				t.Fatalf("Manifests = %#v, want kept %t", result.Manifests, tt.wantKept)
			}
		})
	}
}
