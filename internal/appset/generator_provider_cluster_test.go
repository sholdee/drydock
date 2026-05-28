package appset

import (
	"slices"
	"strings"
	"testing"
)

func TestGenerateClusterGeneratorFromProviderData(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: clusters
spec:
  generators:
    - clusters: {}
  template:
    metadata:
      name: '{{nameNormalized}}'
      labels:
        env: '{{metadata.labels.environment}}'
      annotations:
        owner: '{{metadata.annotations.owner}}'
    spec:
      project: '{{project}}'
      source:
        repoURL: https://github.com/example/repo
        path: apps/{{name}}
        targetRevision: main
      destination:
        server: '{{server}}'
        namespace: default
`)

	apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", data, Options{Provider: ProviderOptions{Data: ProviderData{
		Clusters: []ClusterInput{
			{
				Name:        "prod-a",
				Server:      "https://prod-a.example.invalid",
				Project:     "platform",
				Labels:      map[string]string{"environment": "prod"},
				Annotations: map[string]string{"owner": "platform"},
			},
		},
	}}})
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"prod-a"}) {
		t.Fatalf("generated names = %#v, want prod-a", got)
	}
	app := apps[0].Application
	if app.Spec.Project != "platform" || app.Spec.Destination.Server != "https://prod-a.example.invalid" {
		t.Fatalf("generated app = %#v, want project and server from provider cluster", app)
	}
	if app.Labels["env"] != "prod" || app.Annotations["owner"] != "platform" {
		t.Fatalf("metadata = labels %#v annotations %#v, want provider metadata", app.Labels, app.Annotations)
	}
}

func TestGenerateClusterGeneratorGoTemplateMetadata(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: clusters
spec:
  goTemplate: true
  generators:
    - clusters:
        values:
          region: '{{.metadata.labels.region}}'
  template:
    metadata:
      name: '{{.nameNormalized}}'
      labels:
        region: '{{.values.region}}'
      annotations:
        owner: '{{index .metadata.annotations "team.example/owner"}}'
    spec:
      project: '{{.project}}'
      source:
        repoURL: https://github.com/example/repo
        path: apps/{{.name}}
        targetRevision: main
      destination:
        server: '{{.server}}'
        namespace: default
`)

	apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", data, Options{Provider: ProviderOptions{Data: ProviderData{
		Clusters: []ClusterInput{{
			Name:        "prod.us-east",
			Server:      "https://prod-east.example.invalid",
			Project:     "platform",
			Labels:      map[string]string{"region": "us-east"},
			Annotations: map[string]string{"team.example/owner": "platform"},
		}},
	}}})
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(apps) != 1 {
		t.Fatalf("len(apps) = %d, want 1", len(apps))
	}
	if apps[0].Application.Name != "prod.us-east" {
		t.Fatalf("name = %q, want sanitized cluster name", apps[0].Application.Name)
	}
	if apps[0].Application.Labels["region"] != "us-east" || apps[0].Application.Annotations["owner"] != "platform" {
		t.Fatalf("generated metadata = labels %#v annotations %#v", apps[0].Application.Labels, apps[0].Application.Annotations)
	}
}

func TestGenerateClusterGeneratorFlatListFromProviderData(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: cluster-list
spec:
  goTemplate: true
  generators:
    - clusters:
        flatList: true
  template:
    metadata:
      name: cluster-summary
      annotations:
        clusters: '{{range .clusters}}{{.name}}={{.server}};{{end}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: apps/summary
        targetRevision: main
      destination:
        name: in-cluster
        namespace: default
`)

	apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", data, Options{Provider: ProviderOptions{Data: ProviderData{
		Clusters: []ClusterInput{
			{Name: "prod-a", Server: "https://prod-a.example.invalid"},
			{Name: "prod-b", Server: "https://prod-b.example.invalid"},
		},
	}}})
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"cluster-summary"}) {
		t.Fatalf("generated names = %#v, want one summary app", got)
	}
	if got := apps[0].Application.Annotations["clusters"]; got != "prod-a=https://prod-a.example.invalid;prod-b=https://prod-b.example.invalid;" {
		t.Fatalf("clusters annotation = %q, want flat cluster list", got)
	}
}

func TestGenerateClusterGeneratorAppliesSelectorAndValues(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: selected-clusters
spec:
  generators:
    - clusters:
        selector:
          matchLabels:
            environment: prod
        values:
          region: '{{metadata.labels.region}}'
      selector:
        matchLabels:
          values.region: east
  template:
    metadata:
      name: '{{name}}-{{values.region}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: apps/{{name}}
        targetRevision: main
      destination:
        server: '{{server}}'
        namespace: default
`)

	apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", data, Options{Provider: ProviderOptions{Data: ProviderData{
		Clusters: []ClusterInput{
			{Name: "prod-east", Server: "https://east.example.invalid", Labels: map[string]string{"environment": "prod", "region": "east"}},
			{Name: "prod-west", Server: "https://west.example.invalid", Labels: map[string]string{"environment": "prod", "region": "west"}},
			{Name: "dev-east", Server: "https://dev.example.invalid", Labels: map[string]string{"environment": "dev", "region": "east"}},
		},
	}}})
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"prod-east-east"}) {
		t.Fatalf("generated names = %#v, want selected cluster", got)
	}
}

func TestGenerateClusterGeneratorValuesDoNotReferenceSiblings(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: cluster-values
spec:
  generators:
    - clusters:
        values:
          first: '{{name}}'
          second: '{{values.first}}'
  template:
    metadata:
      name: cluster-values
      annotations:
        first: '{{values.first}}'
        second: '{{values.second}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: apps/static
        targetRevision: main
      destination:
        server: '{{server}}'
        namespace: default
`)

	apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", data, Options{Provider: ProviderOptions{Data: ProviderData{
		Clusters: []ClusterInput{{Name: "prod-a", Server: "https://prod-a.example.invalid"}},
	}}})
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(apps) != 1 {
		t.Fatalf("len(apps) = %d, want 1", len(apps))
	}
	if apps[0].Application.Annotations["first"] != "prod-a" {
		t.Fatalf("first annotation = %q, want prod-a", apps[0].Application.Annotations["first"])
	}
	if apps[0].Application.Annotations["second"] != "{{values.first}}" {
		t.Fatalf("second annotation = %q, want unresolved sibling value placeholder", apps[0].Application.Annotations["second"])
	}
}

func TestGenerateClusterGeneratorWithoutFixtureStaysUnsupported(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: clusters
spec:
  generators:
    - clusters: {}
  template:
    metadata:
      name: generated
`)

	_, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", data, Options{})
	if err == nil {
		t.Fatal("GenerateFromYAMLWithOptions() error = nil, want unsupported generator error")
	}
	if len(diags) == 0 || !strings.Contains(diags[0].Message, "unsupported ApplicationSet generator") {
		t.Fatalf("diagnostics = %#v, want unsupported generator diagnostic", diags)
	}
}

func TestGenerateClusterGeneratorUnsupportedSelectorFailsClosed(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: clusters
spec:
  generators:
    - clusters: {}
      selector:
        matchExpressions:
          - key: name
            operator: Sometimes
            values: ["prod-a"]
  template:
    metadata:
      name: '{{name}}'
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
