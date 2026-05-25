package appset

import (
	"slices"
	"testing"
)

func TestProviderGeneratorsWorkAsMatrixChildren(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: provider-matrix
spec:
  goTemplate: true
  generators:
    - matrix:
        generators:
          - clusters:
              selector:
                matchLabels:
                  environment: prod
          - list:
              elements:
                - app: api
                - app: worker
  template:
    metadata:
      name: '{{.app}}-{{.nameNormalized}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: apps/{{.app}}
        targetRevision: main
      destination:
        server: '{{.server}}'
        namespace: '{{.app}}'
`)

	apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", data, Options{Provider: ProviderOptions{Data: ProviderData{
		Clusters: []ClusterInput{{Name: "prod-a", Server: "https://prod-a.example.invalid", Labels: map[string]string{"environment": "prod"}}},
	}}})
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"api-prod-a", "worker-prod-a"}) {
		t.Fatalf("generated names = %#v, want provider matrix children", got)
	}
}

func TestProviderGeneratorMatrixSelectorUnsupportedFailsClosed(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: provider-matrix
spec:
  goTemplate: true
  generators:
    - matrix:
        generators:
          - clusters: {}
          - list:
              elements:
                - app: api
      selector:
        matchExpressions:
          - key: name
            operator: Sometimes
            values: ["prod-a"]
  template:
    metadata:
      name: '{{.app}}-{{.name}}'
`)

	apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", data, Options{Provider: ProviderOptions{Data: ProviderData{
		Clusters: []ClusterInput{{Name: "prod-a", Server: "https://prod-a.example.invalid"}},
	}}})
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
	}
	if len(apps) != 0 {
		t.Fatalf("generated apps = %#v, want none", apps)
	}
	if len(diags) != 1 || diags[0].Code != "appset.provider-unsupported-filter" {
		t.Fatalf("diagnostics = %#v, want unsupported selector diagnostic", diags)
	}
}

func TestProviderGeneratorsWorkAsMergeChildren(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: provider-merge
spec:
  generators:
    - merge:
        mergeKeys: ["name"]
        generators:
          - clusters: {}
          - clusterDecisionResource:
              configMapRef: placement-config
              name: placement-a
  template:
    metadata:
      name: '{{name}}-{{placement}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: apps/{{placement}}
        targetRevision: main
      destination:
        server: '{{server}}'
        namespace: default
`)

	apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", data, Options{Provider: ProviderOptions{Data: ProviderData{
		Clusters: []ClusterInput{
			{Name: "prod-a", Server: "https://prod-a.example.invalid"},
			{Name: "prod-b", Server: "https://prod-b.example.invalid"},
		},
		ClusterDecisions: []ClusterDecisionInput{{
			ConfigMapRef:  "placement-config",
			ResourceName:  "placement-a",
			MatchKey:      "clusterName",
			StatusListKey: "clusters",
			Decisions:     []map[string]any{{"clusterName": "prod-b", "placement": "edge"}},
		}},
	}}})
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"prod-a-{{placement}}", "prod-b-edge"}) {
		t.Fatalf("generated names = %#v, want merge overlay from provider child", got)
	}
}

func TestProviderGeneratorMergeSelectorUnsupportedFailsClosed(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: provider-merge
spec:
  generators:
    - merge:
        mergeKeys: ["name"]
        generators:
          - clusters: {}
          - list:
              elements:
                - name: prod-a
                  placement: edge
      selector:
        matchExpressions:
          - key: name
            operator: Sometimes
            values: ["prod-a"]
  template:
    metadata:
      name: '{{name}}-{{placement}}'
`)

	apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", data, Options{Provider: ProviderOptions{Data: ProviderData{
		Clusters: []ClusterInput{{Name: "prod-a", Server: "https://prod-a.example.invalid"}},
	}}})
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
	}
	if len(apps) != 0 {
		t.Fatalf("generated apps = %#v, want none", apps)
	}
	if len(diags) != 1 || diags[0].Code != "appset.provider-unsupported-filter" {
		t.Fatalf("diagnostics = %#v, want unsupported selector diagnostic", diags)
	}
}

func TestGeneratePluginGeneratorWorksAsMatrixChild(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: plugin-matrix
spec:
  generators:
    - matrix:
        generators:
          - list:
              elements:
                - environment: prod
          - plugin:
              configMapRef:
                name: generator-plugin
  template:
    metadata:
      name: '{{environment}}-{{name}}'
    spec:
      project: default
      source:
        repoURL: https://example.invalid/repo.git
        path: apps/{{environment}}/{{name}}
        targetRevision: HEAD
      destination:
        name: in-cluster
        namespace: default
`)

	apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", data, Options{Provider: ProviderOptions{Data: ProviderData{
		Plugins: []PluginInput{{
			ConfigMapRef: "generator-plugin",
			Outputs:      []any{map[string]any{"name": "edge-a"}},
		}},
	}}})
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"prod-edge-a"}) {
		t.Fatalf("generated names = %#v, want matrix plugin app", got)
	}
}

func TestGeneratePluginGeneratorWorksAsMergeChild(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: plugin-merge
spec:
  generators:
    - merge:
        mergeKeys: ["name"]
        generators:
          - list:
              elements:
                - name: edge-a
                  environment: prod
          - plugin:
              configMapRef:
                name: generator-plugin
  template:
    metadata:
      name: '{{environment}}-{{name}}'
      annotations:
        region: '{{region}}'
    spec:
      project: default
      source:
        repoURL: https://example.invalid/repo.git
        path: apps/{{environment}}/{{name}}
        targetRevision: HEAD
      destination:
        name: in-cluster
        namespace: default
`)

	apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", data, Options{Provider: ProviderOptions{Data: ProviderData{
		Plugins: []PluginInput{{
			ConfigMapRef: "generator-plugin",
			Outputs:      []any{map[string]any{"name": "edge-a", "region": "east"}},
		}},
	}}})
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"prod-edge-a"}) {
		t.Fatalf("generated names = %#v, want merge plugin app", got)
	}
	if got := apps[0].Application.Annotations["region"]; got != "east" {
		t.Fatalf("region annotation = %q, want plugin overlay", got)
	}
}
