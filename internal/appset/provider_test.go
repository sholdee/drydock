package appset

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/diagnostic"
)

func TestLoadProviderFixture(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "first.yaml")
	jsonPath := filepath.Join(dir, "second.json")
	writeProviderFixture(t, yamlPath, `
clusters:
  - name: prod-b
    server: https://prod-b.example.invalid
    project: platform
    labels:
      environment: prod
    annotations:
      owner: platform
    values:
      region: east
scmRepositories:
  - provider: github
    organization: example-org
    repository: example-repo
    repositoryID: repo-123
    branch: main
    sha: abcdef1234567890
    url: https://github.com/example-org/example-repo
    labels:
      - ops
    paths:
      - deploy/app.yaml
    values:
      tier: ops
`)
	writeProviderFixture(t, jsonPath, `{
  "clusters": [
    {
      "name": "prod-a",
      "server": "https://prod-a.example.invalid",
      "project": "platform",
      "values": {"region": "west"}
    }
  ],
  "clusterDecisions": [
    {
      "configMapRef": "placement-config",
      "resourceName": "placement-a",
      "matchKey": "clusterName",
      "statusListKey": "clusters",
      "decisions": [{"clusterName": "prod-a", "placement": "edge"}],
      "values": {"tier": "edge"}
    }
  ],
  "pullRequests": [
    {
      "provider": "github",
      "organization": "example-org",
      "repository": "example-repo",
      "number": 42,
      "title": "Update chart",
      "branch": "renovate/chart",
      "targetBranch": "main",
      "headSHA": "abcdef1234567890",
      "author": "renovate",
      "labels": ["dependencies"],
      "values": {"kind": "renovate"}
    }
  ],
  "plugins": [
    {
      "configMapRef": "generator-plugin",
      "outputs": [{"environment": "test", "cluster": {"name": "prod-a"}}],
      "values": {"source": "fixture"}
    }
  ]
}`)

	data, diags, err := LoadProviderFixtures([]string{yamlPath, jsonPath})
	if err != nil {
		t.Fatalf("LoadProviderFixtures() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := len(data.Clusters); got != 2 {
		t.Fatalf("len(Clusters) = %d, want 2", got)
	}
	if data.Clusters[0].Name != "prod-a" || data.Clusters[1].Name != "prod-b" {
		t.Fatalf("clusters were not sorted deterministically: %#v", data.Clusters)
	}
	if data.Clusters[0].FixturePath != jsonPath || data.Clusters[1].FixturePath != yamlPath {
		t.Fatalf("cluster provenance = %q, %q; want fixture paths", data.Clusters[0].FixturePath, data.Clusters[1].FixturePath)
	}
	if got := data.SCMRepositories[0].RepositoryID; got != "repo-123" {
		t.Fatalf("RepositoryID = %q, want repo-123", got)
	}
	if got := data.ClusterDecisions[0].Decisions[0]["clusterName"]; got != "prod-a" {
		t.Fatalf("cluster decision = %#v, want clusterName prod-a", data.ClusterDecisions[0].Decisions[0])
	}
	if got := data.PullRequests[0].Number; got != 42 {
		t.Fatalf("pull request number = %d, want 42", got)
	}
	output, ok := data.Plugins[0].Outputs[0].(map[string]any)
	if !ok {
		t.Fatalf("plugin output = %#v, want mapping object", data.Plugins[0].Outputs[0])
	}
	if got := output["environment"]; got != "test" {
		t.Fatalf("plugin output = %#v, want environment test", data.Plugins[0].Outputs[0])
	}

	_, diags, err = LoadProviderFixtures([]string{"https://example.invalid/provider.yaml"})
	if err == nil {
		t.Fatalf("LoadProviderFixtures() accepted a URL-like fixture path")
	}
	assertProviderFixtureInvalidDiagnostic(t, diags, "https://example.invalid/provider.yaml")
}

func TestLoadProviderFixtureRejectsUnknownTopLevelShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture.yaml")
	writeProviderFixture(t, path, `
clusters: []
clusterz: []
`)

	_, diags, err := LoadProviderFixtures([]string{path})
	if err == nil {
		t.Fatalf("LoadProviderFixtures() succeeded with unknown top-level field")
	}
	assertProviderFixtureInvalidDiagnostic(t, diags, path)
}

func TestLoadProviderFixtureRejectsUnknownItemField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture.yaml")
	writeProviderFixture(t, path, `
clusters:
  - name: prod-a
    server: https://prod-a.example.invalid
    token: should-not-exist
`)

	_, diags, err := LoadProviderFixtures([]string{path})
	if err == nil {
		t.Fatalf("LoadProviderFixtures() succeeded with unknown item field")
	}
	assertProviderFixtureInvalidDiagnostic(t, diags, path)
}

func TestLoadProviderFixtureAllowsPluginOutputShapeForGeneratorDiagnostic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture.yaml")
	writeProviderFixture(t, path, `
plugins:
  - configMapRef: generator-plugin
    outputs:
      - not-a-map
`)

	data, diags, err := LoadProviderFixtures([]string{path})
	if err != nil {
		t.Fatalf("LoadProviderFixtures() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := data.Plugins[0].Outputs[0]; got != "not-a-map" {
		t.Fatalf("plugin output = %#v, want scalar preserved for generator diagnostic", got)
	}
}

func TestLoadProviderFixtureRejectsUnsupportedExtension(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture.txt")
	writeProviderFixture(t, path, `
clusters: []
`)

	_, diags, err := LoadProviderFixtures([]string{path})
	if err == nil {
		t.Fatalf("LoadProviderFixtures() succeeded with unsupported extension")
	}
	assertProviderFixtureInvalidDiagnostic(t, diags, path)
}

func TestLoadProviderFixtureRejectsAdditionalYAMLDocuments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture.yaml")
	writeProviderFixture(t, path, `
clusters: []
---
clusterz: []
`)

	_, diags, err := LoadProviderFixtures([]string{path})
	if err == nil {
		t.Fatalf("LoadProviderFixtures() succeeded with additional YAML document")
	}
	assertProviderFixtureInvalidDiagnostic(t, diags, path)
}

func TestLoadProviderFixtureRejectsDuplicateIdentities(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.yaml")
	second := filepath.Join(dir, "second.yaml")
	writeProviderFixture(t, first, `
clusters:
  - name: prod-a
    server: https://prod-a.example.invalid
`)
	writeProviderFixture(t, second, `
clusters:
  - name: prod-a
    server: https://prod-a.example.invalid
`)

	_, diags, err := LoadProviderFixtures([]string{first, second})
	if err == nil {
		t.Fatalf("LoadProviderFixtures() succeeded with duplicate cluster identity")
	}
	assertProviderFixtureInvalidDiagnostic(t, diags, second)
	if !strings.Contains(diags[0].Message, "duplicate provider fixture cluster") {
		t.Fatalf("diagnostic message = %q, want duplicate cluster", diags[0].Message)
	}
}

func TestLoadProviderFixtureRejectsDuplicatePluginOutputs(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.yaml")
	second := filepath.Join(dir, "second.yaml")
	writeProviderFixture(t, first, `
plugins:
  - configMapRef: generator-plugin
    outputs:
      - environment: prod
`)
	writeProviderFixture(t, second, `
plugins:
  - configMapRef: generator-plugin
    outputs:
      - environment: prod
      - environment: stage
`)

	_, diags, err := LoadProviderFixtures([]string{first, second})
	if err == nil {
		t.Fatalf("LoadProviderFixtures() succeeded with duplicate plugin output identity")
	}
	assertProviderFixtureInvalidDiagnostic(t, diags, second)
	if !strings.Contains(diags[0].Message, "duplicate provider fixture plugin") {
		t.Fatalf("diagnostic message = %q, want duplicate plugin", diags[0].Message)
	}
}

func TestProviderFixtureSortsInputs(t *testing.T) {
	data, diags, err := MergeProviderData(ProviderData{
		Clusters: []ClusterInput{
			{Name: "prod-b", Server: "https://b.example.invalid"},
			{Name: "prod-a", Server: "https://z.example.invalid"},
			{Name: "prod-a", Server: "https://a.example.invalid"},
		},
		ClusterDecisions: []ClusterDecisionInput{
			{ConfigMapRef: "placement-b", ResourceName: "resource-a", Decisions: []map[string]any{{"clusterName": "prod-b"}}},
			{ConfigMapRef: "placement-a", ResourceName: "resource-b", Decisions: []map[string]any{{"clusterName": "prod-c"}}},
			{ConfigMapRef: "placement-a", ResourceName: "resource-a", Decisions: []map[string]any{{"clusterName": "prod-a"}}},
		},
		SCMRepositories: []SCMRepositoryInput{
			{Provider: "github", Organization: "example", Repository: "repo-b", Branch: "main", URL: "https://github.com/example/repo-b"},
			{Provider: "github", Organization: "example", Repository: "repo-a", Branch: "release", URL: "https://github.com/example/repo-a"},
			{Provider: "github", Organization: "example", Repository: "repo-a", Branch: "main", URL: "https://github.com/example/repo-a"},
		},
		PullRequests: []PullRequestInput{
			{Provider: "github", Organization: "example", Repository: "repo", Number: 20},
			{Provider: "github", Organization: "example", Repository: "repo", Number: 10},
			{Provider: "github", Organization: "example", Repository: "repo", Number: 2},
			{Provider: "github", Organization: "another", Repository: "repo", Number: 30},
		},
		Plugins: []PluginInput{
			{ConfigMapRef: "plugin-b", Outputs: []any{map[string]any{"environment": "prod"}}},
			{ConfigMapRef: "plugin-a", Outputs: []any{map[string]any{"environment": "stage"}}},
			{ConfigMapRef: "plugin-a", Outputs: []any{map[string]any{"environment": "dev"}}},
		},
	})
	if err != nil {
		t.Fatalf("MergeProviderData() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}

	if got := []string{data.Clusters[0].Server, data.Clusters[1].Server, data.Clusters[2].Server}; got[0] != "https://a.example.invalid" || got[1] != "https://z.example.invalid" || got[2] != "https://b.example.invalid" {
		t.Fatalf("cluster order = %#v", got)
	}
	if got := data.ClusterDecisions[0].ResourceName; got != "resource-a" {
		t.Fatalf("first cluster decision resource = %q, want resource-a", got)
	}
	if got := data.SCMRepositories[0].Branch; got != "main" {
		t.Fatalf("first SCM branch = %q, want main", got)
	}
	if got := data.PullRequests[0].Organization; got != "another" {
		t.Fatalf("first pull request organization = %q, want another", got)
	}
	if got := []int{data.PullRequests[1].Number, data.PullRequests[2].Number, data.PullRequests[3].Number}; got[0] != 2 || got[1] != 10 || got[2] != 20 {
		t.Fatalf("example pull request order = %#v, want numeric order 2, 10, 20", got)
	}
	output, ok := data.Plugins[0].Outputs[0].(map[string]any)
	if !ok {
		t.Fatalf("first plugin output = %#v, want mapping object", data.Plugins[0].Outputs[0])
	}
	if got := output["environment"]; got != "dev" {
		t.Fatalf("first plugin output = %#v, want dev", data.Plugins[0].Outputs[0])
	}
}

func writeProviderFixture(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.TrimLeft(body, "\n")), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, err)
	}
}

func assertProviderFixtureInvalidDiagnostic(t *testing.T, diags []diagnostic.Diagnostic, path string) {
	t.Helper()
	if len(diags) == 0 {
		t.Fatalf("diagnostics = nil, want provider fixture invalid diagnostic")
	}
	if got := diags[0].Code; got != "appset.provider-fixture-invalid" {
		t.Fatalf("Code = %q, want appset.provider-fixture-invalid", got)
	}
	if got := diags[0].Provenance.Path; got != path {
		t.Fatalf("diagnostic path = %q, want %q", got, path)
	}
}
