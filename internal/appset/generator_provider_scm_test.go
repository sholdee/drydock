package appset

import (
	"slices"
	"testing"
)

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

func TestSCMProviderFilteringGoldenContract(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: scm
spec:
  generators:
    - scmProvider:
        github:
          organization: example-org
        filters:
          - repositoryMatch: '^api-'
            branchMatch: '^release/'
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
			{Provider: "github", Organization: "example-org", Repository: "api-server", Branch: "release/2026.05", URL: "https://github.com/example-org/api-server", Labels: []string{"deploy"}},
			{Provider: "github", Organization: "example-org", Repository: "api-server", Branch: "main", URL: "https://github.com/example-org/api-server", Labels: []string{"deploy"}},
			{Provider: "github", Organization: "example-org", Repository: "worker", Branch: "release/2026.05", URL: "https://github.com/example-org/worker", Labels: []string{"deploy"}},
		},
	}}})
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"api-server-release-2026.05"}) {
		t.Fatalf("generated names = %#v, want filtered SCM app", got)
	}
	source := apps[0].Application.Spec.Source
	if source.RepoURL != "https://github.com/example-org/api-server" {
		t.Fatalf("RepoURL = %q, want filtered repository URL", source.RepoURL)
	}
	if source.TargetRevision != "release/2026.05" {
		t.Fatalf("TargetRevision = %q, want filtered branch", source.TargetRevision)
	}
}
