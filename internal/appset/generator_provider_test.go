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
func TestGenerateClusterDecisionResourceFromProviderData(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: decisions
spec:
  generators:
    - clusterDecisionResource:
        configMapRef: placement-config
        name: placement-a
        values:
          region: '{{region}}'
  template:
    metadata:
      name: '{{name}}-{{placement}}-{{values.region}}'
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
		Clusters: []ClusterInput{{Name: "prod-a", Server: "https://prod-a.example.invalid"}},
		ClusterDecisions: []ClusterDecisionInput{{
			ConfigMapRef:  "placement-config",
			ResourceName:  "placement-a",
			MatchKey:      "clusterName",
			StatusListKey: "clusters",
			Decisions:     []map[string]any{{"clusterName": "prod-a", "placement": "edge", "region": "east"}},
		}},
	}}})
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"prod-a-edge-east"}) {
		t.Fatalf("generated names = %#v, want rendered decision value", got)
	}
	if apps[0].Application.Spec.Destination.Server != "https://prod-a.example.invalid" {
		t.Fatalf("destination server = %q, want matched cluster server", apps[0].Application.Spec.Destination.Server)
	}
}
func TestGenerateClusterDecisionResourceMissingDataReturnsDiagnostic(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: decisions
spec:
  generators:
    - clusterDecisionResource:
        configMapRef: placement-config
        name: placement-a
  template:
    metadata:
      name: '{{name}}'
`)

	apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", data, Options{Provider: ProviderOptions{FixturePaths: []string{"fixture.yaml"}}})
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
func TestGenerateClusterDecisionResourceRequiresMatchingStatusListKey(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: decisions
spec:
  generators:
    - clusterDecisionResource:
        configMapRef: placement-config
        name: placement-a
  template:
    metadata:
      name: '{{name}}'
`)

	for _, tt := range []struct {
		name          string
		statusListKey string
	}{
		{name: "empty", statusListKey: ""},
		{name: "wrong", statusListKey: "placements"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", data, Options{Provider: ProviderOptions{Data: ProviderData{
				Clusters: []ClusterInput{{Name: "prod-a", Server: "https://prod-a.example.invalid"}},
				ClusterDecisions: []ClusterDecisionInput{{
					ConfigMapRef:  "placement-config",
					ResourceName:  "placement-a",
					MatchKey:      "clusterName",
					StatusListKey: tt.statusListKey,
					Decisions:     []map[string]any{{"clusterName": "prod-a"}},
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
		})
	}
}
func TestGenerateClusterDecisionResourceRequiresNameOrLabelSelector(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: decisions
spec:
  generators:
    - clusterDecisionResource:
        configMapRef: placement-config
  template:
    metadata:
      name: '{{name}}'
`)

	apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", data, Options{Provider: ProviderOptions{Data: ProviderData{
		Clusters: []ClusterInput{{Name: "prod-a", Server: "https://prod-a.example.invalid"}},
		ClusterDecisions: []ClusterDecisionInput{{
			ConfigMapRef:  "placement-config",
			ResourceName:  "placement-a",
			MatchKey:      "clusterName",
			StatusListKey: "clusters",
			Decisions:     []map[string]any{{"clusterName": "prod-a"}},
		}},
	}}})
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
	}
	if len(apps) != 0 {
		t.Fatalf("generated apps = %#v, want none", apps)
	}
	if len(diags) != 1 || diags[0].Code != "appset.provider-unsupported-filter" {
		t.Fatalf("diagnostics = %#v, want unsupported filter diagnostic", diags)
	}
}
func TestGenerateClusterDecisionResourceAppliesLabelSelector(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: decisions
spec:
  generators:
    - clusterDecisionResource:
        configMapRef: placement-config
        labelSelector:
          matchExpressions:
            - key: placement
              operator: In
              values: ["edge"]
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
		Clusters: []ClusterInput{{Name: "prod-a", Server: "https://prod-a.example.invalid"}},
		ClusterDecisions: []ClusterDecisionInput{
			{
				ConfigMapRef:  "placement-config",
				ResourceName:  "edge-placement",
				Labels:        map[string]string{"placement": "edge"},
				MatchKey:      "clusterName",
				StatusListKey: "clusters",
				Decisions:     []map[string]any{{"clusterName": "prod-a", "placement": "edge"}},
			},
			{
				ConfigMapRef:  "placement-config",
				ResourceName:  "core-placement",
				Labels:        map[string]string{"placement": "core"},
				MatchKey:      "clusterName",
				StatusListKey: "clusters",
				Decisions:     []map[string]any{{"clusterName": "prod-a", "placement": "core"}},
			},
		},
	}}})
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"prod-a-edge"}) {
		t.Fatalf("generated names = %#v, want only label-selected decision", got)
	}
}
func TestGenerateClusterDecisionResourceUnsupportedFilterFailsClosed(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: decisions
spec:
  generators:
    - clusterDecisionResource:
        configMapRef: placement-config
        name: edge-placement
        labelSelector:
          matchLabels:
            placement: edge
  template:
    metadata:
      name: '{{name}}'
`)

	apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", data, Options{Provider: ProviderOptions{Data: ProviderData{
		Clusters: []ClusterInput{{Name: "prod-a", Server: "https://prod-a.example.invalid"}},
		ClusterDecisions: []ClusterDecisionInput{{
			ConfigMapRef:  "placement-config",
			ResourceName:  "edge-placement",
			Labels:        map[string]string{"placement": "edge"},
			MatchKey:      "clusterName",
			StatusListKey: "clusters",
			Decisions:     []map[string]any{{"clusterName": "prod-a"}},
		}},
	}}})
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
	}
	if len(apps) != 0 {
		t.Fatalf("generated apps = %#v, want none", apps)
	}
	if len(diags) != 1 || diags[0].Code != "appset.provider-unsupported-filter" {
		t.Fatalf("diagnostics = %#v, want unsupported filter diagnostic", diags)
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
func TestGenerateSCMProviderFromFixture(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: repos
spec:
  goTemplate: true
  generators:
    - scmProvider:
        github:
          organization: example-org
        values:
          summary: '{{.organization}}/{{.repository}}@{{.branch}}'
  template:
    metadata:
      name: '{{.repository}}-{{.branchNormalized}}'
      labels:
        repo-labels: '{{.labels}}'
      annotations:
        repo-id: '{{.repository_id}}'
        short: '{{.short_sha}}'
        short7: '{{.short_sha_7}}'
        summary: '{{.values.summary}}'
    spec:
      project: default
      source:
        repoURL: '{{.url}}'
        path: apps/{{.repository}}
        targetRevision: '{{.branch}}'
      destination:
        name: in-cluster
        namespace: default
`)

	apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", data, Options{Provider: ProviderOptions{Data: ProviderData{
		SCMRepositories: []SCMRepositoryInput{{
			Provider:     "github",
			Organization: "example-org",
			Repository:   "platform-api",
			RepositoryID: "repo-123",
			Branch:       "feature/Add_Login",
			SHA:          "abcdef1234567890",
			URL:          "https://github.com/example-org/platform-api",
			Labels:       []string{"deploy", "team-a"},
		}},
	}}})
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"platform-api-feature-add-login"}) {
		t.Fatalf("generated names = %#v, want scm app", got)
	}
	app := apps[0].Application
	if app.Spec.GetSource().RepoURL != "https://github.com/example-org/platform-api" || app.Spec.GetSource().TargetRevision != "feature/Add_Login" {
		t.Fatalf("source = %#v, want repo URL and branch from fixture", app.Spec.GetSource())
	}
	if app.Labels["repo-labels"] != "deploy,team-a" || app.Annotations["repo-id"] != "repo-123" {
		t.Fatalf("metadata = labels %#v annotations %#v, want SCM params", app.Labels, app.Annotations)
	}
	if app.Annotations["short"] != "abcdef12" || app.Annotations["short7"] != "abcdef1" {
		t.Fatalf("short shas = %q/%q, want 8 and 7 chars", app.Annotations["short"], app.Annotations["short7"])
	}
	if app.Annotations["summary"] != "example-org/platform-api@feature/Add_Login" {
		t.Fatalf("summary = %q, want templated values", app.Annotations["summary"])
	}
}
func TestGenerateSCMProviderFiltersRepositoryBranchAndLabels(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: repos
spec:
  generators:
    - scmProvider:
        gitlab:
          group: platform
        filters:
          - repositoryMatch: '^api-'
            labelMatch: '^deploy$'
          - branchMatch: '^release/'
  template:
    metadata:
      name: '{{repository}}-{{branchNormalized}}'
    spec:
      project: default
      source:
        repoURL: '{{url}}'
        path: apps/{{repository}}
        targetRevision: '{{branch}}'
      destination:
        name: in-cluster
        namespace: default
`)

	apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", data, Options{Provider: ProviderOptions{Data: ProviderData{
		SCMRepositories: []SCMRepositoryInput{
			{Provider: "gitlab", Organization: "platform", Repository: "api-service", Branch: "release/2026.05", URL: "https://gitlab.com/platform/api-service", Labels: []string{"deploy"}},
			{Provider: "gitlab", Organization: "platform", Repository: "worker", Branch: "release/2026.05", URL: "https://gitlab.com/platform/worker", Labels: []string{"deploy"}},
			{Provider: "gitlab", Organization: "platform", Repository: "api-admin", Branch: "main", URL: "https://gitlab.com/platform/api-admin", Labels: []string{"deploy"}},
			{Provider: "gitlab", Organization: "platform", Repository: "api-docs", Branch: "release/2026.05", URL: "https://gitlab.com/platform/api-docs", Labels: []string{"docs"}},
		},
	}}})
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"api-service-release-2026.05"}) {
		t.Fatalf("generated names = %#v, want filtered SCM repo", got)
	}
}
func TestGenerateSCMProviderMatchesProviderScopeWithoutCorruptingParams(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: repos
spec:
  generators:
    - scmProvider:
        azureDevOps:
          organization: ado-org
          teamProject: platform
    - scmProvider:
        awsCodeCommit:
          region: us-east-1
  template:
    metadata:
      name: '{{repository}}'
      annotations:
        org: '{{organization}}'
    spec:
      project: default
      source:
        repoURL: '{{url}}'
        path: apps/{{repository}}
        targetRevision: '{{branch}}'
      destination:
        name: in-cluster
        namespace: default
`)

	apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", data, Options{Provider: ProviderOptions{Data: ProviderData{
		SCMRepositories: []SCMRepositoryInput{
			{Provider: "azureDevOps", Organization: "ado-org", Project: "platform", Repository: "ado-api", Branch: "main", URL: "https://dev.azure.com/ado-org/platform/_git/ado-api"},
			{Provider: "azureDevOps", Organization: "ado-org", Project: "other", Repository: "other-api", Branch: "main", URL: "https://dev.azure.com/ado-org/other/_git/other-api"},
			{Provider: "awsCodeCommit", Organization: "123456789012", Region: "us-east-1", Repository: "commit-repo", Branch: "main", URL: "https://git-codecommit.us-east-1.amazonaws.com/v1/repos/commit-repo"},
			{Provider: "awsCodeCommit", Organization: "123456789012", Region: "us-west-2", Repository: "west-repo", Branch: "main", URL: "https://git-codecommit.us-west-2.amazonaws.com/v1/repos/west-repo"},
		},
	}}})
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"ado-api", "commit-repo"}) {
		t.Fatalf("generated names = %#v, want provider-scoped repos", got)
	}
	if apps[0].Application.Annotations["org"] != "ado-org" {
		t.Fatalf("azure organization annotation = %q, want emitted organization", apps[0].Application.Annotations["org"])
	}
	if apps[1].Application.Annotations["org"] != "123456789012" {
		t.Fatalf("aws organization annotation = %q, want emitted account/organization", apps[1].Application.Annotations["org"])
	}
}
func TestGenerateSCMProviderAWSCodeCommitTagFilters(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: repos
spec:
  generators:
    - scmProvider:
        awsCodeCommit:
          region: us-east-1
          tagFilters:
            - key: Environment
              value: prod
            - key: Team
  template:
    metadata:
      name: '{{repository}}'
    spec:
      project: default
      source:
        repoURL: '{{url}}'
        path: apps/{{repository}}
        targetRevision: '{{branch}}'
      destination:
        name: in-cluster
        namespace: default
`)

	apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", data, Options{Provider: ProviderOptions{Data: ProviderData{
		SCMRepositories: []SCMRepositoryInput{
			{Provider: "awsCodeCommit", Region: "us-east-1", Repository: "prod-api", Branch: "main", URL: "https://git-codecommit.us-east-1.amazonaws.com/v1/repos/prod-api", Tags: map[string]string{"Environment": "prod", "Team": "platform"}},
			{Provider: "awsCodeCommit", Region: "us-east-1", Repository: "dev-api", Branch: "main", URL: "https://git-codecommit.us-east-1.amazonaws.com/v1/repos/dev-api", Tags: map[string]string{"Environment": "dev", "Team": "platform"}},
			{Provider: "awsCodeCommit", Region: "us-east-1", Repository: "missing-team", Branch: "main", URL: "https://git-codecommit.us-east-1.amazonaws.com/v1/repos/missing-team", Tags: map[string]string{"Environment": "prod"}},
		},
	}}})
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"prod-api"}) {
		t.Fatalf("generated names = %#v, want tag-filtered CodeCommit repo", got)
	}
}
func TestGenerateSCMProviderAWSCodeCommitTagFiltersWithoutFixtureTagsFailsClosed(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: repos
spec:
  generators:
    - scmProvider:
        awsCodeCommit:
          region: us-east-1
          tagFilters:
            - key: Environment
              value: prod
  template:
    metadata:
      name: '{{repository}}'
`)

	apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", data, Options{Provider: ProviderOptions{Data: ProviderData{
		SCMRepositories: []SCMRepositoryInput{{Provider: "awsCodeCommit", Region: "us-east-1", Repository: "repo-a", Branch: "main"}},
	}}})
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
	}
	if len(apps) != 0 {
		t.Fatalf("generated apps = %#v, want none", apps)
	}
	if len(diags) != 1 || diags[0].Code != "appset.provider-unsupported-filter" {
		t.Fatalf("diagnostics = %#v, want unsupported tag filter diagnostic", diags)
	}
}
func TestGenerateSCMProviderAWSCodeCommitWithoutRegionFailsClosed(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: repos
spec:
  generators:
    - scmProvider:
        awsCodeCommit: {}
  template:
    metadata:
      name: '{{repository}}'
`)

	apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", data, Options{Provider: ProviderOptions{Data: ProviderData{
		SCMRepositories: []SCMRepositoryInput{{Provider: "awsCodeCommit", Region: "us-east-1", Repository: "repo-a", Branch: "main"}},
	}}})
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
	}
	if len(apps) != 0 {
		t.Fatalf("generated apps = %#v, want none", apps)
	}
	if len(diags) != 1 || diags[0].Code != "appset.provider-unsupported-filter" {
		t.Fatalf("diagnostics = %#v, want unsupported ambient region diagnostic", diags)
	}
}
func TestGenerateSCMProviderGitLabTopic(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: repos
spec:
  generators:
    - scmProvider:
        gitlab:
          group: platform
          topic: prod
  template:
    metadata:
      name: '{{repository}}'
    spec:
      project: default
      source:
        repoURL: '{{url}}'
        path: apps/{{repository}}
        targetRevision: '{{branch}}'
      destination:
        name: in-cluster
        namespace: default
`)

	apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", data, Options{Provider: ProviderOptions{Data: ProviderData{
		SCMRepositories: []SCMRepositoryInput{
			{Provider: "gitlab", Organization: "platform", Repository: "api", Branch: "main", URL: "https://gitlab.com/platform/api", Labels: []string{"prod", "deploy"}},
			{Provider: "gitlab", Organization: "platform", Repository: "worker", Branch: "main", URL: "https://gitlab.com/platform/worker", Labels: []string{"dev", "deploy"}},
		},
	}}})
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"api"}) {
		t.Fatalf("generated names = %#v, want topic-filtered GitLab repo", got)
	}
}
func TestGenerateSCMProviderGitLabTopicWithoutFixtureLabelsFailsClosed(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: repos
spec:
  generators:
    - scmProvider:
        gitlab:
          group: platform
          topic: prod
  template:
    metadata:
      name: '{{repository}}'
`)

	apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", data, Options{Provider: ProviderOptions{Data: ProviderData{
		SCMRepositories: []SCMRepositoryInput{{Provider: "gitlab", Organization: "platform", Repository: "api", Branch: "main"}},
	}}})
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
	}
	if len(apps) != 0 {
		t.Fatalf("generated apps = %#v, want none", apps)
	}
	if len(diags) != 1 || diags[0].Code != "appset.provider-unsupported-filter" {
		t.Fatalf("diagnostics = %#v, want unsupported topic filter diagnostic", diags)
	}
}
func TestGenerateSCMProviderGitLabIncludeSubgroups(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: repos
spec:
  generators:
    - scmProvider:
        gitlab:
          group: platform
          includeSubgroups: true
  template:
    metadata:
      name: '{{repository}}'
      annotations:
        org: '{{organization}}'
    spec:
      project: default
      source:
        repoURL: '{{url}}'
        path: apps/{{repository}}
        targetRevision: '{{branch}}'
      destination:
        name: in-cluster
        namespace: default
`)

	apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", data, Options{Provider: ProviderOptions{Data: ProviderData{
		SCMRepositories: []SCMRepositoryInput{
			{Provider: "gitlab", Organization: "platform/team", Repository: "api", Branch: "main", URL: "https://gitlab.com/platform/team/api"},
			{Provider: "gitlab", Organization: "other/team", Repository: "worker", Branch: "main", URL: "https://gitlab.com/other/team/worker"},
		},
	}}})
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"api"}) {
		t.Fatalf("generated names = %#v, want subgroup repo", got)
	}
	if got := apps[0].Application.Annotations["org"]; got != "platform/team" {
		t.Fatalf("organization annotation = %q, want subgroup namespace", got)
	}
}
func TestGenerateSCMProviderUnsupportedPathFilterFailsClosed(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: repos
spec:
  generators:
    - scmProvider:
        github:
          organization: example-org
        filters:
          - pathsExist:
              - deploy/app.yaml
  template:
    metadata:
      name: '{{repository}}'
`)

	apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", data, Options{Provider: ProviderOptions{Data: ProviderData{
		SCMRepositories: []SCMRepositoryInput{{Provider: "github", Organization: "example-org", Repository: "repo-a", Branch: "main"}},
	}}})
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
	}
	if len(apps) != 0 {
		t.Fatalf("generated apps = %#v, want none", apps)
	}
	if len(diags) != 1 || diags[0].Code != "appset.provider-unsupported-filter" {
		t.Fatalf("diagnostics = %#v, want unsupported path filter diagnostic", diags)
	}
}
func TestGeneratePullRequestFromFixture(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: prs
spec:
  generators:
    - pullRequest:
        github:
          owner: example-org
          repo: platform-api
        values:
          summary: '{{branch}}->{{target_branch}}'
  template:
    metadata:
      name: 'pr-{{number}}-{{branch_slug}}'
      annotations:
        title: '{{title}}'
        author: '{{author}}'
        head: '{{head_sha}}'
        short: '{{head_short_sha}}'
        short7: '{{head_short_sha_7}}'
        target: '{{target_branch_slug}}'
        summary: '{{values.summary}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example-org/platform-api
        path: apps/platform-api
        targetRevision: '{{branch}}'
      destination:
        name: in-cluster
        namespace: default
`)

	apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", data, Options{Provider: ProviderOptions{Data: ProviderData{
		PullRequests: []PullRequestInput{{
			Provider:     "github",
			Organization: "example-org",
			Repository:   "platform-api",
			Number:       42,
			Title:        "Add login",
			Branch:       "feature/Add_Login",
			TargetBranch: "main",
			HeadSHA:      "1234567890abcdef",
			Author:       "octocat",
		}},
	}}})
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"pr-42-feature-add-login"}) {
		t.Fatalf("generated names = %#v, want PR app", got)
	}
	app := apps[0].Application
	if app.Annotations["title"] != "Add login" || app.Annotations["author"] != "octocat" {
		t.Fatalf("annotations = %#v, want title and author", app.Annotations)
	}
	if app.Annotations["short"] != "12345678" || app.Annotations["short7"] != "1234567" {
		t.Fatalf("short shas = %q/%q, want 8 and 7 chars", app.Annotations["short"], app.Annotations["short7"])
	}
	if app.Annotations["target"] != "main" || app.Annotations["summary"] != "feature/Add_Login->main" {
		t.Fatalf("target/summary annotations = %#v, want PR params", app.Annotations)
	}
}
func TestGeneratePullRequestGoTemplateLabels(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: prs
spec:
  goTemplate: true
  generators:
    - pullRequest:
        github:
          owner: example-org
          repo: platform-api
  template:
    metadata:
      name: 'pr-{{.number}}'
      annotations:
        labels: '{{join "," .labels}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example-org/platform-api
        path: apps/platform-api
        targetRevision: '{{.branch}}'
      destination:
        name: in-cluster
        namespace: default
`)

	apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", data, Options{Provider: ProviderOptions{Data: ProviderData{
		PullRequests: []PullRequestInput{{
			Provider:     "github",
			Organization: "example-org",
			Repository:   "platform-api",
			Number:       42,
			Branch:       "feature/add-login",
			TargetBranch: "main",
			Labels:       []string{"enhancement", "ready"},
		}},
	}}})
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := apps[0].Application.Annotations["labels"]; got != "enhancement,ready" {
		t.Fatalf("labels annotation = %q, want Go-template PR labels", got)
	}
}
func TestGeneratePullRequestFiltersBranchTargetAndTitle(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: prs
spec:
  generators:
    - pullRequest:
        gitlab:
          project: platform/api
          labels: ["ready"]
        filters:
          - branchMatch: '^feature/'
            targetBranchMatch: '^main$'
            titleMatch: 'login'
  template:
    metadata:
      name: 'pr-{{number}}'
    spec:
      project: default
      source:
        repoURL: https://gitlab.com/platform/api
        path: apps/api
        targetRevision: '{{branch}}'
      destination:
        name: in-cluster
        namespace: default
`)

	apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", data, Options{Provider: ProviderOptions{Data: ProviderData{
		PullRequests: []PullRequestInput{
			{Provider: "gitlab", Organization: "platform", Repository: "api", Number: 1, Title: "add login", Branch: "feature/login", TargetBranch: "main", Labels: []string{"ready"}},
			{Provider: "gitlab", Organization: "platform", Repository: "api", Number: 2, Title: "add login", Branch: "bugfix/login", TargetBranch: "main", Labels: []string{"ready"}},
			{Provider: "gitlab", Organization: "platform", Repository: "api", Number: 3, Title: "add login", Branch: "feature/login", TargetBranch: "release", Labels: []string{"ready"}},
			{Provider: "gitlab", Organization: "platform", Repository: "api", Number: 4, Title: "add docs", Branch: "feature/docs", TargetBranch: "main", Labels: []string{"ready"}},
			{Provider: "gitlab", Organization: "platform", Repository: "api", Number: 5, Title: "add login", Branch: "feature/login", TargetBranch: "main", Labels: []string{"wip"}},
		},
	}}})
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"pr-1"}) {
		t.Fatalf("generated names = %#v, want filtered PR", got)
	}
}
func TestGeneratePullRequestGitLabStateFilter(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: prs
spec:
  generators:
    - pullRequest:
        gitlab:
          project: platform/team/api
          pullRequestState: merged
  template:
    metadata:
      name: 'pr-{{number}}'
    spec:
      project: default
      source:
        repoURL: https://gitlab.com/platform/team/api
        path: apps/api
        targetRevision: '{{branch}}'
      destination:
        name: in-cluster
        namespace: default
`)

	apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", data, Options{Provider: ProviderOptions{Data: ProviderData{
		PullRequests: []PullRequestInput{
			{Provider: "gitlab", Organization: "platform/team", Repository: "api", Number: 1, Branch: "feature/open", TargetBranch: "main", State: "opened"},
			{Provider: "gitlab", Organization: "platform/team", Repository: "api", Number: 2, Branch: "feature/merged", TargetBranch: "main", State: "merged"},
		},
	}}})
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"pr-2"}) {
		t.Fatalf("generated names = %#v, want merged PR only", got)
	}
}
func TestGeneratePullRequestGitLabStateFilterWithoutFixtureStateFailsClosed(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: prs
spec:
  generators:
    - pullRequest:
        gitlab:
          project: platform/api
          pullRequestState: merged
  template:
    metadata:
      name: 'pr-{{number}}'
`)

	apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", data, Options{Provider: ProviderOptions{Data: ProviderData{
		PullRequests: []PullRequestInput{{Provider: "gitlab", Organization: "platform", Repository: "api", Number: 1, Branch: "feature/login", TargetBranch: "main"}},
	}}})
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
	}
	if len(apps) != 0 {
		t.Fatalf("generated apps = %#v, want none", apps)
	}
	if len(diags) != 1 || diags[0].Code != "appset.provider-unsupported-filter" {
		t.Fatalf("diagnostics = %#v, want unsupported state filter diagnostic", diags)
	}
}
func TestGeneratePullRequestWithoutFixtureStaysUnsupported(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: prs
spec:
  generators:
    - pullRequest:
        github:
          owner: example-org
          repo: platform-api
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
