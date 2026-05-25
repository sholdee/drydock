package appset

import (
	"slices"
	"testing"
)

func TestGeneratePluginGeneratorFromFixture(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: plugin
spec:
  goTemplate: true
  generators:
    - plugin:
        configMapRef:
          name: generator-plugin
        values:
          summary: '{{.environment}}/{{.cluster.name}}'
  template:
    metadata:
      name: '{{.environment}}-{{.cluster.name}}'
      annotations:
        summary: '{{.values.summary}}'
    spec:
      project: default
      source:
        repoURL: https://example.invalid/repo.git
        path: apps/{{.environment}}
        targetRevision: HEAD
      destination:
        name: in-cluster
        namespace: '{{.cluster.name}}'
`)

	apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", data, Options{Provider: ProviderOptions{Data: ProviderData{
		Plugins: []PluginInput{{
			ConfigMapRef: "generator-plugin",
			Outputs: []any{map[string]any{
				"environment": "prod",
				"cluster": map[string]any{
					"name": "edge-a",
				},
			}},
		}},
	}}})
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"prod-edge-a"}) {
		t.Fatalf("generated names = %#v, want plugin app", got)
	}
	app := apps[0].Application
	if app.Spec.Destination.Namespace != "edge-a" || app.Annotations["summary"] != "prod/edge-a" {
		t.Fatalf("app destination/annotations = %#v/%#v, want Go-template plugin params", app.Spec.Destination, app.Annotations)
	}
}

func TestGeneratePluginGeneratorFastTemplateFlattensOutput(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: plugin
spec:
  generators:
    - plugin:
        configMapRef:
          name: generator-plugin
        values:
          summary: '{{environment}}/{{cluster.name}}'
  template:
    metadata:
      name: '{{environment}}-{{cluster.name}}'
      annotations:
        replicas: '{{replicas}}'
        first-item: '{{items.0.name}}'
        summary: '{{values.summary}}'
    spec:
      project: default
      source:
        repoURL: https://example.invalid/repo.git
        path: apps/{{environment}}
        targetRevision: HEAD
      destination:
        name: in-cluster
        namespace: '{{cluster.name}}'
`)

	apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", data, Options{Provider: ProviderOptions{Data: ProviderData{
		Plugins: []PluginInput{{
			ConfigMapRef: "generator-plugin",
			Outputs: []any{map[string]any{
				"environment": "prod",
				"replicas":    3,
				"cluster": map[string]any{
					"name": "edge-a",
				},
				"items": []any{map[string]any{"name": "first"}},
			}},
		}},
	}}})
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	app := apps[0].Application
	if app.Name != "prod-edge-a" || app.Spec.Destination.Namespace != "edge-a" {
		t.Fatalf("generated app = %#v, want flattened plugin params", app)
	}
	if app.Annotations["replicas"] != "3" || app.Annotations["first-item"] != "first" || app.Annotations["summary"] != "prod/edge-a" {
		t.Fatalf("annotations = %#v, want flattened values and templated values", app.Annotations)
	}
}

func TestGeneratePluginGeneratorIncludesInputParameters(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: plugin
spec:
  goTemplate: true
  generators:
    - plugin:
        configMapRef:
          name: generator-plugin
        input:
          parameters:
            environment: prod
            count: 2
  template:
    metadata:
      name: '{{.name}}'
      annotations:
        input-environment: '{{printf "%s" .generator.input.parameters.environment.Raw}}'
        input-count: '{{printf "%s" .generator.input.parameters.count.Raw}}'
    spec:
      project: default
      source:
        repoURL: https://example.invalid/repo.git
        path: apps/{{.name}}
        targetRevision: HEAD
      destination:
        name: in-cluster
        namespace: default
`)

	apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", data, Options{Provider: ProviderOptions{Data: ProviderData{
		Plugins: []PluginInput{{
			ConfigMapRef: "generator-plugin",
			Outputs: []any{map[string]any{
				"name": "from-plugin",
			}},
		}},
	}}})
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := apps[0].Application.Annotations["input-environment"]; got != `"prod"` {
		t.Fatalf("input-environment annotation = %q, want plugin input parameter", got)
	}
	if got := apps[0].Application.Annotations["input-count"]; got != "2" {
		t.Fatalf("input-count annotation = %q, want plugin input parameter", got)
	}
}

func TestGeneratePluginGeneratorMissingConfigMapFixtureReturnsDiagnostic(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: plugin
spec:
  generators:
    - plugin:
        configMapRef:
          name: missing-plugin
  template:
    metadata:
      name: '{{name}}'
`)

	apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", data, Options{Provider: ProviderOptions{Data: ProviderData{
		Plugins: []PluginInput{{
			ConfigMapRef: "other-plugin",
			Outputs: []any{map[string]any{
				"name": "from-plugin",
			}},
		}},
	}}})
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
	}
	if len(apps) != 0 {
		t.Fatalf("generated apps = %#v, want none", apps)
	}
	if len(diags) != 1 || diags[0].Code != "appset.provider-no-match" {
		t.Fatalf("diagnostics = %#v, want provider no-match diagnostic", diags)
	}
}

func TestGeneratePluginGeneratorInvalidOutputShapeReturnsDiagnostic(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: plugin
spec:
  generators:
    - plugin:
        configMapRef:
          name: generator-plugin
  template:
    metadata:
      name: '{{name}}'
`)

	apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", data, Options{Provider: ProviderOptions{Data: ProviderData{
		Plugins: []PluginInput{{
			ConfigMapRef: "generator-plugin",
			Outputs:      []any{"not-a-map"},
		}},
	}}})
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
	}
	if len(apps) != 0 {
		t.Fatalf("generated apps = %#v, want none", apps)
	}
	if len(diags) != 1 || diags[0].Code != "appset.provider-unsupported-filter" {
		t.Fatalf("diagnostics = %#v, want unsupported plugin output diagnostic", diags)
	}
}

func TestGeneratePluginGeneratorEmptyOutputsProduceNoParams(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: plugin
spec:
  generators:
    - plugin:
        configMapRef:
          name: generator-plugin
  template:
    metadata:
      name: '{{name}}'
`)

	apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", data, Options{Provider: ProviderOptions{Data: ProviderData{
		Plugins: []PluginInput{{
			ConfigMapRef: "generator-plugin",
			Outputs:      []any{},
		}},
	}}})
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
	}
	if len(apps) != 0 {
		t.Fatalf("generated apps = %#v, want none", apps)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v, want none for empty plugin outputs", diags)
	}
}
