package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	argosettings "github.com/argoproj/argo-cd/v3/util/settings"
	"github.com/sholdee/drydock/internal/diagnostic"
	"k8s.io/apimachinery/pkg/labels"
	sigsyaml "sigs.k8s.io/yaml"
)

func writeArgoCMSelectors(t *testing.T, selectors string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "argocd-cm.yaml")
	indented := "    " + strings.ReplaceAll(strings.TrimRight(selectors, "\n"), "\n", "\n    ")
	if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
data:
  resource.selectors: |
`+indented+"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

func TestLoadConfigMapResourceSelectors(t *testing.T) {
	path := writeArgoCMSelectors(t, `- apiGroups: [""]
  kinds: ["Pod"]
  clusters: ["https://192.168.0.20"]
  selector: "!argocd.argoproj.io/instance"
- selector: tier in (backend,frontend)
`)

	settings, diags, err := LoadFromConfigMap(path)
	if err != nil {
		t.Fatalf("LoadFromConfigMap() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	want := []ResourceFilterRule{
		{
			APIGroups:  []string{""},
			Kinds:      []string{"Pod"},
			Clusters:   []string{"https://192.168.0.20"},
			Selector:   "!argocd.argoproj.io/instance",
			Provenance: Provenance{Path: path, Pointer: "data.resource.selectors"},
		},
		{
			Selector:   "tier in (backend,frontend)",
			Provenance: Provenance{Path: path, Pointer: "data.resource.selectors"},
		},
	}
	if !reflect.DeepEqual(settings.ResourceSelectors, want) {
		t.Fatalf("ResourceSelectors = %#v, want %#v", settings.ResourceSelectors, want)
	}
	if len(settings.ResourceExclusions) != 0 || len(settings.ResourceInclusions) != 0 {
		t.Fatalf("selectors leaked into exclusions/inclusions: %#v %#v", settings.ResourceExclusions, settings.ResourceInclusions)
	}
}

func TestLoadConfigMapResourceSelectorsInvalidSelectorIsError(t *testing.T) {
	path := writeArgoCMSelectors(t, `- kinds: ["Pod"]
  selector: "tier in (backend"
- selector: "app=web"
`)

	settings, diags, err := LoadFromConfigMap(path)
	if err != nil {
		t.Fatalf("LoadFromConfigMap() error = %v", err)
	}
	if len(diags) != 1 {
		t.Fatalf("diagnostics = %#v, want one", diags)
	}
	diag := diags[0]
	if diag.Severity != diagnostic.SeverityError || diag.Category != "settings" || diag.Code != diagnostic.CodeSettingsResourceSelectorInvalid {
		t.Fatalf("diagnostic = %#v", diag)
	}
	if !strings.Contains(diag.Message, `invalid resource.selectors selector "tier in (backend"`) {
		t.Fatalf("message = %q", diag.Message)
	}
	if diag.Provenance.Path != path || diag.Provenance.Pointer != "data.resource.selectors" {
		t.Fatalf("provenance = %#v", diag.Provenance)
	}
	if len(settings.ResourceSelectors) != 0 {
		t.Fatalf("ResourceSelectors = %#v, want none kept when one selector is invalid", settings.ResourceSelectors)
	}
	assertDiagnosticStableCode(t, diags, "invalid resource.selectors", diagnostic.CodeSettingsResourceSelectorInvalid)
}

func TestLoadConfigMapResourceSelectorsInvalidYAMLIsError(t *testing.T) {
	path := writeArgoCMSelectors(t, `selector: app=web`)

	settings, diags, err := LoadFromConfigMap(path)
	if err != nil {
		t.Fatalf("LoadFromConfigMap() error = %v", err)
	}
	if len(diags) != 1 || diags[0].Severity != diagnostic.SeverityError || diags[0].Code != diagnostic.CodeSettingsResourceSelectorInvalid {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(settings.ResourceSelectors) != 0 {
		t.Fatalf("ResourceSelectors = %#v", settings.ResourceSelectors)
	}
}

func TestLoadHelmValuesResourceSelectors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "values.yaml")
	if err := os.WriteFile(path, []byte(`configs:
  cm:
    resource.selectors: |
      - kinds: ["ConfigMap"]
        selector: team=platform
`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	settings, diags, err := LoadFromHelmValues(path)
	if err != nil {
		t.Fatalf("LoadFromHelmValues() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(settings.ResourceSelectors) != 1 || settings.ResourceSelectors[0].Selector != "team=platform" {
		t.Fatalf("ResourceSelectors = %#v", settings.ResourceSelectors)
	}
}

func TestMergeDiscoveredAppendsResourceSelectors(t *testing.T) {
	left := DefaultSettings()
	left.ResourceSelectors = []ResourceFilterRule{{Selector: "a=b"}}
	right := DefaultSettings()
	right.ResourceSelectors = []ResourceFilterRule{{Selector: "c=d"}}

	merged, diags := MergeDiscovered([]ArgoSettings{left, right})
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(merged.ResourceSelectors) != 2 || merged.ResourceSelectors[0].Selector != "a=b" || merged.ResourceSelectors[1].Selector != "c=d" {
		t.Fatalf("ResourceSelectors = %#v", merged.ResourceSelectors)
	}
}

// upstreamResourceSelectors repeats the resource.selectors branch of Argo
// CD's SettingsManager.GetResourcesFilter (util/settings/settings.go at
// v3.6.1) over the upstream FilteredResource type. The SettingsManager itself
// needs a Kubernetes clientset, which drydock does not depend on.
func upstreamResourceSelectors(value string) ([]argosettings.FilteredResource, error) {
	resourceSelectors := make([]argosettings.FilteredResource, 0)
	if err := sigsyaml.Unmarshal([]byte(value), &resourceSelectors); err != nil {
		return nil, err
	}
	for _, resourceSelector := range resourceSelectors {
		if _, err := labels.Parse(resourceSelector.Selector); err != nil {
			return nil, err
		}
	}
	return resourceSelectors, nil
}

// TestResourceSelectorsParseParityWithArgoCD decodes the same argocd-cm
// value the way Argo CD does and through drydock's provider: a value Argo CD
// rejects is an error diagnostic in drydock, and an accepted value yields
// the same rules.
func TestResourceSelectorsParseParityWithArgoCD(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
	}{
		{name: "full rule", value: "- apiGroups: [\"\"]\n  kinds: [\"Pod\"]\n  clusters: [\"https://192.168.0.20\"]\n  selector: \"!argocd.argoproj.io/instance\"\n"},
		{name: "selector only", value: "- selector: tier in (backend,frontend)\n"},
		{name: "empty selector", value: "- kinds: [\"Pod\"]\n"},
		{name: "set based", value: "- selector: environment notin (dev),tier\n"},
		{name: "unknown field", value: "- selector: a=b\n  namespaces: [x]\n"},
		{name: "numeric selector", value: "- selector: 123\n"},
		{name: "unbalanced set", value: "- selector: tier in (backend\n"},
		{name: "invalid key", value: "- selector: \"-bad=x\"\n"},
		{name: "invalid operator", value: "- selector: a=b=c\n"},
		{name: "not a list", value: "selector: app=web\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream, upstreamErr := upstreamResourceSelectors(tc.value)

			settings := DefaultSettings()
			diags := applyCMMap(&settings, map[string]string{"resource.selectors": tc.value}, "argocd-cm.yaml", "data")
			hasError := false
			for _, diag := range diags {
				if diag.Severity == diagnostic.SeverityError {
					hasError = true
				}
			}
			if (upstreamErr != nil) != hasError {
				t.Fatalf("upstream error = %v, drydock diagnostics = %#v", upstreamErr, diags)
			}
			if upstreamErr != nil {
				return
			}
			if len(upstream) != len(settings.ResourceSelectors) {
				t.Fatalf("upstream rules = %#v, drydock = %#v", upstream, settings.ResourceSelectors)
			}
			for i, rule := range upstream {
				got := settings.ResourceSelectors[i]
				if !reflect.DeepEqual(rule.APIGroups, got.APIGroups) || !reflect.DeepEqual(rule.Kinds, got.Kinds) || !reflect.DeepEqual(rule.Clusters, got.Clusters) || rule.Selector != got.Selector {
					t.Fatalf("rule %d: upstream %#v, drydock %#v", i, rule, got)
				}
			}
		})
	}
}
