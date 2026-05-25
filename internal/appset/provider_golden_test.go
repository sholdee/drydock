package appset

import (
	"slices"
	"testing"
)

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
