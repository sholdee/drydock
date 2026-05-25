package appset

import (
	"slices"
	"strings"
	"testing"
)

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
