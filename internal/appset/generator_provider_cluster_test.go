package appset

import (
	"maps"
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

// reservedClusterAnnotations are the keys Argo CD v3.6.1 drops from cluster
// generator params (applicationset/generators/cluster.go
// excludedClusterAnnotations).
var reservedClusterAnnotations = map[string]string{
	"argocd.argoproj.io/refresh":                       "normal",
	"argocd.argoproj.io/refresh-timestamp":             "2026-01-02T03:04:05Z",
	"argocd.argoproj.io/hydrate":                       "normal",
	"argocd.argoproj.io/hydrate-timestamp":             "2026-01-02T03:04:05Z",
	"argocd.argoproj.io/tracking-id":                   "argocd:v1/Secret:argocd/prod",
	"argocd.argoproj.io/installation-id":               "prod-install",
	"kubectl.kubernetes.io/last-applied-configuration": `{"kind":"Secret"}`,
}

func clusterAnnotationsWithReserved(extra map[string]string) map[string]string {
	out := maps.Clone(reservedClusterAnnotations)
	maps.Copy(out, extra)
	return out
}

func TestGenerateClusterGeneratorGoTemplateExcludesReservedAnnotations(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: clusters
spec:
  goTemplate: true
  generators:
    - clusters: {}
  template:
    metadata:
      name: '{{.nameNormalized}}'
      annotations:
        annotations-json: '{{toJson .metadata.annotations}}'
        has-annotations: '{{hasKey .metadata "annotations"}}'
        has-labels: '{{hasKey .metadata "labels"}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: apps/{{.name}}
        targetRevision: main
      destination:
        server: '{{.server}}'
        namespace: default
`)

	apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", data, Options{Provider: ProviderOptions{Data: ProviderData{
		Clusters: []ClusterInput{
			{
				Name:        "prod",
				Server:      "https://prod.example.invalid",
				Annotations: clusterAnnotationsWithReserved(map[string]string{"team.example/owner": "platform"}),
			},
			{
				Name:        "reserved-only",
				Server:      "https://reserved.example.invalid",
				Annotations: clusterAnnotationsWithReserved(nil),
			},
			{
				Name:   "labels-only",
				Server: "https://labels.example.invalid",
				Labels: map[string]string{"env": "dev"},
			},
		},
	}}})
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"prod", "reserved-only", "labels-only"}) {
		t.Fatalf("generated names = %#v", got)
	}
	want := map[string]map[string]string{
		"prod": {
			"annotations-json": `{"team.example/owner":"platform"}`,
			"has-annotations":  "true",
			"has-labels":       "true",
		},
		"reserved-only": {
			// Argo CD gates the key on the unfiltered annotations, so it
			// exists as an empty map when every annotation is reserved.
			"annotations-json": `{}`,
			"has-annotations":  "true",
			"has-labels":       "true",
		},
		"labels-only": {
			// toJson of a missing key renders null; the key is absent.
			"annotations-json": `null`,
			"has-annotations":  "false",
			"has-labels":       "true",
		},
	}
	for _, app := range apps {
		for key, value := range want[app.Application.Name] {
			if got := app.Application.Annotations[key]; got != value {
				t.Fatalf("%s annotation %s = %q, want %q", app.Application.Name, key, got, value)
			}
		}
	}
}

func TestGenerateClusterGeneratorFlatParamsExcludeReservedAnnotations(t *testing.T) {
	cluster := ClusterInput{
		Name:        "prod",
		Server:      "https://prod.example.invalid",
		Labels:      map[string]string{"env": "prod"},
		Annotations: clusterAnnotationsWithReserved(map[string]string{"team.example/owner": "platform"}),
	}
	params, err := clusterParams(cluster, nil, false, nil)
	if err != nil {
		t.Fatalf("clusterParams() error = %v", err)
	}
	for key := range reservedClusterAnnotations {
		if _, ok := params["metadata.annotations."+key]; ok {
			t.Fatalf("params expose reserved annotation %q: %#v", key, params)
		}
	}
	if params["metadata.annotations.team.example/owner"] != "platform" || params["metadata.labels.env"] != "prod" {
		t.Fatalf("params = %#v, want non-reserved annotation and label", params)
	}

	goParams, err := clusterParams(cluster, nil, true, nil)
	if err != nil {
		t.Fatalf("clusterParams(goTemplate) error = %v", err)
	}
	metadata, ok := goParams["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("metadata param = %#v, want map", goParams["metadata"])
	}
	// Argo CD v3.6.1 passes map[string]string, so the assertion pins it.
	annotations, ok := metadata["annotations"].(map[string]string)
	if !ok || len(annotations) != 1 || annotations["team.example/owner"] != "platform" {
		t.Fatalf("metadata.annotations = %#v, want only the non-reserved annotation", metadata["annotations"])
	}
}

func TestClusterParamsGoTemplateBareClusterMetadata(t *testing.T) {
	bare, err := clusterParams(ClusterInput{Name: "bare", Server: "https://bare.example.invalid"}, nil, true, nil)
	if err != nil {
		t.Fatalf("clusterParams(bare) error = %v", err)
	}
	// Argo CD only lists Secrets carrying the secret-type label, so labels is
	// always present; annotations is absent when the cluster has none.
	metadata, ok := bare["metadata"].(map[string]any)
	if !ok || len(metadata) != 1 {
		t.Fatalf("bare metadata = %#v, want only the labels key", bare["metadata"])
	}
	if labels, ok := metadata["labels"].(map[string]string); !ok || labels == nil || len(labels) != 0 {
		t.Fatalf("bare metadata.labels = %#v, want empty map", metadata["labels"])
	}
}

func TestGenerateClusterGeneratorFlatTemplateIgnoresReservedAnnotation(t *testing.T) {
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
      name: '{{name}}'
      annotations:
        refresh: '{{metadata.annotations.argocd.argoproj.io/refresh}}'
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
		Clusters: []ClusterInput{{
			Name:        "prod",
			Server:      "https://prod.example.invalid",
			Annotations: clusterAnnotationsWithReserved(nil),
		}},
	}}})
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
	}
	if len(diags) != 0 || len(apps) != 1 {
		t.Fatalf("apps = %#v diagnostics = %#v", apps, diags)
	}
	// fasttemplate leaves unknown placeholders in place, matching Argo CD
	// when the reserved key is not a parameter.
	if got := apps[0].Application.Annotations["refresh"]; got != "{{metadata.annotations.argocd.argoproj.io/refresh}}" {
		t.Fatalf("refresh annotation = %q, want unresolved placeholder", got)
	}
}

// Argo CD lists cluster Secrets through the argocd.argoproj.io/secret-type
// selector, so .metadata.labels always exists upstream; a label-less fixture
// cluster must still render templates that index it.
func TestGenerateClusterGeneratorGoTemplateLabelsKeyAlwaysPresent(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: clusters
spec:
  goTemplate: true
  goTemplateOptions: ["missingkey=error"]
  generators:
    - clusters: {}
  template:
    metadata:
      name: '{{.name}}-{{ index .metadata.labels "env" | default "none" }}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: apps/{{.name}}
        targetRevision: main
      destination:
        server: '{{.server}}'
        namespace: default
`)

	apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", data, Options{Provider: ProviderOptions{Data: ProviderData{
		Clusters: []ClusterInput{{Name: "prod", Server: "https://prod.example.invalid"}},
	}}})
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"prod-none"}) {
		t.Fatalf("generated names = %#v, want [prod-none]", got)
	}
}
