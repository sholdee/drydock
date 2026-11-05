package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
)

func writeResourceSelectorSettings(t *testing.T, root, extra, selectors string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "settings", "argocd-cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
data:
`+extra+`  resource.selectors: |
`+selectors)
}

func resourceSelectorDiagnostics(diags []diagnostic.Diagnostic) []diagnostic.Diagnostic {
	var out []diagnostic.Diagnostic
	for _, diag := range diags {
		if diag.Code == diagnostic.CodeSettingsResourceSelectorMismatch {
			out = append(out, diag)
		}
	}
	return out
}

func TestOrchestratorBuildWarnsOnResourceSelectorMismatch(t *testing.T) {
	root := t.TempDir()
	writeBuildApplication(t, root, "demo", "demo-cm")
	writeResourceSelectorSettings(t, root, "", `    - kinds: ["ConfigMap"]
      selector: team=platform
`)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(result.Manifests) != 1 || result.Manifests[0].Object.GetName() != "demo-cm" {
		t.Fatalf("Manifests = %#v, want the non-matching ConfigMap kept", result.Manifests)
	}
	diags := resourceSelectorDiagnostics(result.Diagnostics)
	if len(diags) != 1 {
		t.Fatalf("selector diagnostics = %#v, want one", result.Diagnostics)
	}
	want := `Application argocd/demo: Resource /ConfigMap demo-cm does not match the resource selector "team=platform" in the settings and will not be watched; it will appear as OutOfSync`
	if diags[0].Message != want || diags[0].Severity != diagnostic.SeverityWarning || diags[0].Category != "settings" {
		t.Fatalf("diagnostic = %#v, want message %q", diags[0], want)
	}
	if got := diagnostic.StableCode(diagnostic.Diagnostic{Category: "settings", Message: diags[0].Message}); got != diagnostic.CodeSettingsResourceSelectorMismatch {
		t.Fatalf("StableCode() = %q", got)
	}
	assertApplicationStatuses(t, result.Statuses, []ApplicationStatus{
		{Namespace: "argocd", Name: "demo", Status: ApplicationStatusPass},
	})
}

func TestOrchestratorBuildResourceSelectorMatchIsSilent(t *testing.T) {
	root := t.TempDir()
	writeBuildApplication(t, root, "demo", "demo-cm")
	// Label tracking puts the instance label on every rendered resource
	// before Argo CD compares it with the selector, so a selector on the
	// tracking label matches.
	writeResourceSelectorSettings(t, root, "  application.resourceTrackingMethod: label\n", `    - selector: app.kubernetes.io/instance
`)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if diags := resourceSelectorDiagnostics(result.Diagnostics); len(diags) != 0 {
		t.Fatalf("selector diagnostics = %#v, want none", diags)
	}
}

func TestOrchestratorBuildResourceSelectorSkipsExcludedResources(t *testing.T) {
	root := t.TempDir()
	writeBuildApplication(t, root, "demo", "demo-cm")
	writeResourceSelectorSettings(t, root, `  resource.exclusions: |
    - kinds: ["ConfigMap"]
`, `    - selector: team=platform
`)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(result.Manifests) != 0 {
		t.Fatalf("Manifests = %#v, want the excluded ConfigMap dropped", result.Manifests)
	}
	if diags := resourceSelectorDiagnostics(result.Diagnostics); len(diags) != 0 {
		t.Fatalf("selector diagnostics = %#v, want none for an excluded resource", diags)
	}
}

func writeResourceSelectorClusterSecret(t *testing.T, root, name, server string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "settings", "cluster-"+name+".yaml"), `apiVersion: v1
kind: Secret
metadata:
  name: cluster-`+name+`
  namespace: argocd
  labels:
    argocd.argoproj.io/secret-type: cluster
stringData:
  name: `+name+`
  server: `+server+`
`)
}

// Argo CD matches rule cluster globs against the destination server it
// resolves a destination name to, never against the name itself.
func TestOrchestratorBuildResourceSelectorClusterRule(t *testing.T) {
	root := t.TempDir()
	writeBuildApplicationWithDestination(t, root, "prod", "prod-cm", "prod-west")
	writeBuildApplicationWithDestination(t, root, "dev", "dev-cm", "dev")
	writeResourceSelectorClusterSecret(t, root, "prod-west", "https://prod-west.example.com")
	writeResourceSelectorClusterSecret(t, root, "dev", "https://dev.example.com")
	writeResourceSelectorSettings(t, root, "", `    - clusters: ["https://prod-*"]
      selector: team=platform
`)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(result.Manifests) != 2 {
		t.Fatalf("Manifests = %#v, want both ConfigMaps kept", result.Manifests)
	}
	diags := resourceSelectorDiagnostics(result.Diagnostics)
	if len(diags) != 1 || !strings.Contains(diags[0].Message, "argocd/prod: Resource /ConfigMap prod-cm") {
		t.Fatalf("selector diagnostics = %#v, want one for the prod Application", diags)
	}
}

func TestOrchestratorBuildResourceSelectorClusterRuleResolvesInCluster(t *testing.T) {
	tests := []struct {
		name      string
		clusters  string
		wantDiags int
	}{
		{name: "server glob matches the implicit in-cluster server", clusters: `["https://kubernetes.default.svc"]`, wantDiags: 1},
		{name: "name glob does not match", clusters: `["in-*"]`, wantDiags: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeBuildApplication(t, root, "demo", "demo-cm")
			writeResourceSelectorSettings(t, root, "", `    - clusters: `+tt.clusters+`
      selector: team=platform
`)

			result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root, Strict: true})
			if got := len(resourceSelectorDiagnostics(result.Diagnostics)); got != tt.wantDiags {
				t.Fatalf("selector diagnostics = %#v, want %d", result.Diagnostics, tt.wantDiags)
			}
			if (err != nil) != (tt.wantDiags > 0) {
				t.Fatalf("Build() error = %v, want strict failure only with a selector warning", err)
			}
		})
	}
}

func TestResourceSelectorDestinationServer(t *testing.T) {
	settings := config.DefaultSettings()
	settings.Clusters["https://prod.example.com"] = config.ClusterSettings{Name: "prod", Server: "https://prod.example.com"}
	settings.Clusters["https://dup-a.example.com"] = config.ClusterSettings{Name: "dup", Server: "https://dup-a.example.com"}
	settings.Clusters["https://dup-b.example.com"] = config.ClusterSettings{Name: "dup", Server: "https://dup-b.example.com"}
	settings.Clusters["https://local.example.com"] = config.ClusterSettings{Name: "in-cluster", Server: "https://local.example.com"}
	noInCluster := config.DefaultSettings()

	tests := []struct {
		name       string
		dest       argoappv1.ApplicationDestination
		settings   config.ArgoSettings
		wantServer string
		wantOK     bool
	}{
		{name: "server trims trailing slash", dest: argoappv1.ApplicationDestination{Server: "https://other.example.com/"}, settings: settings, wantServer: "https://other.example.com", wantOK: true},
		{name: "name resolves through cluster Secret", dest: argoappv1.ApplicationDestination{Name: "prod"}, settings: settings, wantServer: "https://prod.example.com", wantOK: true},
		{name: "in-cluster Secret wins over the implicit server", dest: argoappv1.ApplicationDestination{Name: "in-cluster"}, settings: settings, wantServer: "https://local.example.com", wantOK: true},
		{name: "implicit in-cluster", dest: argoappv1.ApplicationDestination{Name: "in-cluster"}, settings: noInCluster, wantServer: "https://kubernetes.default.svc", wantOK: true},
		{name: "unknown name", dest: argoappv1.ApplicationDestination{Name: "missing"}, settings: settings},
		{name: "ambiguous name", dest: argoappv1.ApplicationDestination{Name: "dup"}, settings: settings},
		{name: "both name and server", dest: argoappv1.ApplicationDestination{Name: "prod", Server: "https://prod.example.com"}, settings: settings},
		{name: "neither", settings: settings},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, ok := resourceSelectorDestinationServer(tt.dest, tt.settings)
			if server != tt.wantServer || ok != tt.wantOK {
				t.Fatalf("resourceSelectorDestinationServer() = %q, %v; want %q, %v", server, ok, tt.wantServer, tt.wantOK)
			}
		})
	}
}

func TestOrchestratorBuildResourceSelectorSkipsUnresolvedDestination(t *testing.T) {
	root := t.TempDir()
	writeBuildApplicationWithDestination(t, root, "demo", "demo-cm", "unknown-cluster")
	writeResourceSelectorSettings(t, root, "", `    - selector: team=platform
`)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if diags := resourceSelectorDiagnostics(result.Diagnostics); len(diags) != 0 {
		t.Fatalf("selector diagnostics = %#v, want none for an unresolved destination name", diags)
	}
}

func TestOrchestratorBuildStrictFailsResourceSelectorMismatch(t *testing.T) {
	root := t.TempDir()
	writeBuildApplication(t, root, "demo", "demo-cm")
	writeResourceSelectorSettings(t, root, "", `    - selector: team=platform
`)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root, Strict: true})
	if err == nil {
		t.Fatal("Build() error = nil, want strict failure")
	}
	diags := resourceSelectorDiagnostics(result.Diagnostics)
	if len(diags) != 1 || diags[0].Severity != diagnostic.SeverityError {
		t.Fatalf("selector diagnostics = %#v, want one error under --strict", diags)
	}
	assertApplicationStatuses(t, result.Statuses, []ApplicationStatus{
		{Namespace: "argocd", Name: "demo", Status: ApplicationStatusFail},
	})
}

func TestOrchestratorBuildInvalidResourceSelectorFailsSettings(t *testing.T) {
	root := t.TempDir()
	writeBuildApplication(t, root, "demo", "demo-cm")
	writeResourceSelectorSettings(t, root, "", `    - selector: "tier in (backend"
`)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root})
	if err == nil {
		t.Fatal("Build() error = nil, want settings failure")
	}
	found := false
	for _, diag := range diagnostic.WithStableCodes(result.Diagnostics) {
		if diag.Code == diagnostic.CodeSettingsResourceSelectorInvalid && diag.Severity == diagnostic.SeverityError {
			found = true
		}
	}
	if !found {
		t.Fatalf("diagnostics = %#v, want %s error", result.Diagnostics, diagnostic.CodeSettingsResourceSelectorInvalid)
	}
}
