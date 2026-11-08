package appset

import (
	"slices"
	"testing"
)

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
      name: '{{name}}-{{placement}}'
      annotations:
        decision-region: '{{region}}'
        values-region: '{{values.region}}'
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
	if got := generatedNames(apps); !slices.Equal(got, []string{"prod-a-edge"}) {
		t.Fatalf("generated names = %#v, want rendered decision fields", got)
	}
	// Argo CD v3.6.1 does not render clusterDecisionResource values as
	// templates (applicationset/generators/duck_type.go:170-172 and
	// :222-231), so values.region keeps the raw '{{region}}' string.
	annotations := apps[0].Application.Annotations
	if annotations["decision-region"] != "east" || annotations["values-region"] != "{{region}}" {
		t.Fatalf("annotations = %#v, want decision-region east and literal values-region {{region}}", annotations)
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
    spec:
      project: default
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

func TestGenerateClusterDecisionResourceSupportsFixtureStatusListKeys(t *testing.T) {
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
    spec:
      project: default
`)

	for _, tt := range []struct {
		name          string
		statusListKey string
		want          []string
	}{
		{name: "empty defaults to clusters", statusListKey: "", want: []string{"prod-a"}},
		{name: "explicit clusters", statusListKey: "clusters", want: []string{"prod-a"}},
		{name: "custom key", statusListKey: "placements", want: []string{"prod-a"}},
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
			if len(diags) != 0 {
				t.Fatalf("diagnostics = %#v", diags)
			}
			if got := generatedNames(apps); !slices.Equal(got, tt.want) {
				t.Fatalf("generated names = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestGenerateClusterDecisionResourceCustomStatusListKeyNoMatch(t *testing.T) {
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
    spec:
      project: default
`)

	apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", data, Options{Provider: ProviderOptions{Data: ProviderData{
		Clusters: []ClusterInput{{Name: "prod-a", Server: "https://prod-a.example.invalid"}},
		ClusterDecisions: []ClusterDecisionInput{{
			ConfigMapRef:  "placement-config",
			ResourceName:  "placement-a",
			MatchKey:      "clusterName",
			StatusListKey: "placements",
			Decisions:     []map[string]any{{"name": "prod-a"}},
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

func TestGenerateClusterDecisionResourceWithoutNameOrLabelSelectorFailsApplicationSet(t *testing.T) {
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
    spec:
      project: default
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
	requireGeneratorFailed(t, apps, diags, err, "there is a problem with the definition of the ClusterDecisionResource generator")
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

func TestGenerateClusterDecisionResourceNameAndLabelSelectorFailsApplicationSet(t *testing.T) {
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
    spec:
      project: default
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
	requireGeneratorFailed(t, apps, diags, err, "there is a problem with the definition of the ClusterDecisionResource generator")
}

// Argo CD v3.6.1 counts a labelSelector as set when matchLabels is present,
// even empty (applicationset/generators/duck_type.go:99-104), and fails the
// ApplicationSet when it also sets a name.
func TestGenerateClusterDecisionResourceNameAndEmptyMatchLabelsFailsApplicationSet(t *testing.T) {
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: decisions
spec:
  generators:
    - list:
        elements:
          - name: sibling
    - clusterDecisionResource:
        configMapRef: placement-config
        name: placement-a
        labelSelector:
          matchLabels: {}
  template:
    metadata:
      name: '{{name}}'
    spec:
      project: default
`)
	apps, diags, err := GenerateFromYAMLWithOptions(t.TempDir(), "app-set.yaml", data, Options{Provider: ProviderOptions{Data: ProviderData{
		Clusters: []ClusterInput{{Name: "prod-a", Server: "https://prod-a.example.invalid"}},
		ClusterDecisions: []ClusterDecisionInput{{
			ConfigMapRef: "placement-config", ResourceName: "placement-a", MatchKey: "clusterName", StatusListKey: "clusters",
			Decisions: []map[string]any{{"clusterName": "prod-a"}},
		}},
	}}})
	requireGeneratorFailed(t, apps, diags, err, "there is a problem with the definition of the ClusterDecisionResource generator")
}

// Argo CD v3.6.1 fails the ApplicationSet when the list of decision
// resources is empty (duck_type.go:129-132).
func TestGenerateClusterDecisionResourceNoResourceFailsApplicationSet(t *testing.T) {
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: decisions
spec:
  generators:
    - list:
        elements:
          - name: sibling
    - clusterDecisionResource:
        configMapRef: placement-config
        name: placement-missing
  template:
    metadata:
      name: '{{name}}'
    spec:
      project: default
`)
	apps, diags, err := GenerateFromYAMLWithOptions(t.TempDir(), "app-set.yaml", data, Options{Provider: ProviderOptions{Data: ProviderData{
		Clusters: []ClusterInput{{Name: "prod-a", Server: "https://prod-a.example.invalid"}},
		ClusterDecisions: []ClusterDecisionInput{{
			ConfigMapRef: "placement-config", ResourceName: "placement-a", MatchKey: "clusterName", StatusListKey: "clusters",
			Decisions: []map[string]any{{"clusterName": "prod-a"}},
		}},
	}}})
	requireGeneratorFailed(t, apps, diags, err, "no clusterDecisionResources found")
}
