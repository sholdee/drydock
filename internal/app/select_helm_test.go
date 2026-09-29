package app

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/discovery"
	"github.com/sholdee/drydock/internal/ociartifact"
	"github.com/sholdee/drydock/internal/render"
	sourcepkg "github.com/sholdee/drydock/internal/source"
)

func TestHelmLocalInputSelectionPath(t *testing.T) {
	for _, tc := range []struct {
		name       string
		sourcePath string
		ref        string
		glob       bool
		want       string // empty: owns nothing
	}{
		{name: "escaped value file", ref: "../../envs/prod/v.yaml", glob: true, want: "envs/prod/v.yaml"},
		{name: "escaped file parameter", ref: "../../envs/prod/v.txt", want: "envs/prod/v.txt"},
		{name: "glob under source", ref: "values/*.yaml", glob: true, want: "charts/web/values"},
		{name: "glob at source", ref: "*.yaml", glob: true, want: "charts/web"},
		{name: "escaped glob", ref: "../../envs/*/v.yaml", glob: true, want: "envs"},
		{name: "backslash escaped value file", ref: `..\..\envs\prod\v.yaml`, glob: true, want: "envs/prod/v.yaml"},
		{name: "dotted source path", sourcePath: "./charts/web/", ref: "values/*.yaml", glob: true, want: "charts/web/values"},
		{name: "doublestar alternation", ref: "values/{a,b}/*.yaml", glob: true, want: "charts/web/values"},
		// { alone does not trigger globbing at render: the path stays literal.
		{name: "brace only", ref: "values/{a,b}.yaml", glob: true, want: "charts/web/values/{a,b}.yaml"},
		{name: "glob cleaned away", ref: "*/../v.yaml", glob: true, want: "charts/web/v.yaml"},
		{name: "file parameter never globs", ref: "values/*.txt", want: "charts/web/values/*.txt"},
		{name: "root source", sourcePath: ".", ref: "values.yaml", glob: true, want: "values.yaml"},
		// An empty glob prefix would intersect every changed path.
		{name: "glob at repository root", ref: "../../*/v.yaml", glob: true},
		{name: "root source glob", sourcePath: ".", ref: "*.yaml", glob: true},
		{name: "escapes repository root", ref: "../../../x.yaml", glob: true},
		{name: "escaped glob escapes repository root", ref: "../../../*/v.yaml", glob: true},
		{name: "absolute", ref: "/abs.yaml", glob: true},
		{name: "backslash absolute", ref: `\abs.yaml`, glob: true},
		{name: "env templated", ref: "$ARGOCD_ENV_X/v.yaml", glob: true},
		{name: "ref value file", ref: "$values/v.yaml", glob: true},
		{name: "remote", ref: "https://example.com/v.yaml", glob: true},
		{name: "empty", ref: "", glob: true},
		{name: "blank", ref: "  ", glob: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sourcePath := tc.sourcePath
			if sourcePath == "" {
				sourcePath = "charts/web"
			}
			got, ok := helmLocalInputSelectionPath(sourcePath, tc.ref, tc.glob)
			if ok != (tc.want != "") || got != tc.want {
				t.Fatalf("helmLocalInputSelectionPath(%q, %q, %v) = %q, %v; want %q", sourcePath, tc.ref, tc.glob, got, ok, tc.want)
			}
		})
	}
	// The empty-prefix rule relies on this.
	if got, ok := cleanSelectionRelativePath(""); ok {
		t.Fatalf("cleanSelectionRelativePath(\"\") = %q, true; want false", got)
	}
}

func TestWithSelectionOnlyPathsOwnsLocalHelmInputs(t *testing.T) {
	root := t.TempDir()
	writeHelmEscapedValueFileApps(t, root, "v")
	const valueFile = "envs/prod/web-values.yaml"
	const fileParameter = "envs/prod/web-value.txt"

	helmApp := func(name string, source argoappv1.ApplicationSource) ApplicationSelectionInput {
		source.Helm = &argoappv1.ApplicationSourceHelm{
			ValueFiles:     []string{"../../" + valueFile},
			FileParameters: []argoappv1.HelmFileParameter{{Name: "value", Path: "../../" + fileParameter}},
		}
		app := argoappv1.Application{Name: name, Spec: argoappv1.ApplicationSpec{Source: &source}}
		return ApplicationSelectionInput{Application: app, Paths: []string{"apps/" + name + ".yaml"}}
	}
	inputs := []ApplicationSelectionInput{
		helmApp("local", argoappv1.ApplicationSource{RepoURL: "https://github.com/example/repo", Path: "charts/web"}),
		// Split-repo: the chart path lives in another repository, not this tree.
		helmApp("foreign", argoappv1.ApplicationSource{RepoURL: "https://github.com/example/other", Path: "charts/api"}),
		// Repo-mapped to a different checkout: renders from there, not root.
		helmApp("mapped", argoappv1.ApplicationSource{RepoURL: "https://github.com/example/mapped", Path: "charts/web"}),
		helmApp("oci", argoappv1.ApplicationSource{RepoURL: "oci://registry.example.com/web", Path: "charts/web"}),
		// Chart repository source: value files resolve inside the fetched chart.
		helmApp("chart", argoappv1.ApplicationSource{RepoURL: "https://charts.example.com", Chart: "web", Path: "charts/web", TargetRevision: "1.0.0"}),
	}
	repoMaps := []sourcepkg.RepoMap{{URL: "https://github.com/example/mapped", Path: t.TempDir()}}

	got := withSelectionOnlyPaths(context.Background(), root, repoMaps, selfRepoRefs{}, inputs).inputs

	for _, input := range got {
		wantOwns := input.Application.Name == "local"
		for _, inputPath := range []string{valueFile, fileParameter} {
			if owns := slices.Contains(input.Paths, inputPath); owns != wantOwns {
				t.Errorf("%s owns %s = %v, want %v", input.Application.Name, inputPath, owns, wantOwns)
			}
		}
	}
	for i := range inputs {
		if len(inputs[i].Paths) != 1 {
			t.Fatalf("input %s Paths mutated to %v; listed inputs also key the render cache", inputs[i].Application.Name, inputs[i].Paths)
		}
	}
}

// TestApplicationInputsByKeyOmitsHelmSelectionOnlyPaths pins that Helm
// inputs outside spec.source.path stay selection-only: the per-Application
// input paths key the persistent render cache as required inputs, and an
// escaped, glob, or optional Helm input there would make the Application and
// its rendered children persistence-ineligible.
func TestApplicationInputsByKeyOmitsHelmSelectionOnlyPaths(t *testing.T) {
	application := func(name string) argoappv1.Application {
		return argoappv1.Application{Namespace: "argocd", Name: name}
	}
	want := map[string][]string{
		applicationDiscoveryKey(application("web-prod")):     {"apps/web-prod.yaml", "charts/web"},
		applicationDiscoveryKey(application("cluster-prod")): {"apps/cluster-prod.yaml", "clusters/prod"},
		applicationDiscoveryKey(application("other")):        {"apps/other.yaml", "manifests/other"},
	}
	for name, write := range map[string]func(*testing.T, string, string){
		"escaped value file":     writeHelmEscapedValueFileApps,
		"escaped file parameter": writeHelmEscapedFileParameterApps,
		"value file glob":        writeHelmValueFileGlobApps,
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, "v")
			discovered, err := discovery.Scan(root, discovery.Options{})
			if err != nil {
				t.Fatalf("discovery.Scan() error = %v", err)
			}

			got := map[string][]string{}
			for key, inputs := range applicationInputsByKey(discovered) {
				if inputs.Duplicate {
					t.Fatalf("%s marked duplicate", key)
				}
				got[key] = inputs.Paths
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("applicationInputsByKey() = %v, want %v", got, want)
			}
		})
	}
}

// erroringOCIArtifactAcquirer fails every acquisition, so a source the
// provider sends to a registry never resolves to a root.
type erroringOCIArtifactAcquirer struct{}

func (erroringOCIArtifactAcquirer) Resolve(context.Context, string, string, ociartifact.Options) (string, error) {
	return "", errors.New("OCI ref root must not be acquired")
}

func (erroringOCIArtifactAcquirer) Extract(context.Context, string, string, ociartifact.Options) (string, func(), error) {
	return "", nil, errors.New("OCI ref root must not be acquired")
}

// TestRefSourceRendersFromRootMatchesProvider is the drift guard between
// changed-only $ref ownership and source resolution: for every ref root
// shape, refSourceRendersFromRoot must report exactly whether a provider
// built from the same root, repo maps, and self-repo facts resolves the
// source to root without acquiring it.
func TestRefSourceRendersFromRootMatchesProvider(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "values", "v.yaml"), "value: v\n")
	const (
		foreignURL         = "https://github.com/example/other"
		mappedRootURL      = "https://github.com/example/mapped-root"
		mappedElsewhereURL = "https://github.com/example/mapped-elsewhere"
	)
	repoMaps := []sourcepkg.RepoMap{
		{URL: mappedRootURL, Path: root},
		{URL: mappedElsewhereURL, Path: t.TempDir()},
	}
	selfRepo := selfRepoRefs{
		urlKeys:   []string{sourcepkg.CanonicalGitURLKey(selfRepoRemoteURL)},
		revisions: []string{"feature"},
	}

	for _, tc := range []struct {
		name   string
		source render.ResolvedSource
		want   bool
		// divergent marks the documented row where the provider returns
		// root but refSourceRendersFromRoot does not.
		divergent bool
	}{
		{name: "self repo at HEAD", source: render.ResolvedSource{RepoURL: selfRepoSpecURL, TargetRevision: "HEAD"}, want: true},
		{name: "self repo without revision", source: render.ResolvedSource{RepoURL: selfRepoRemoteURL}, want: true},
		{name: "self repo at tracked branch", source: render.ResolvedSource{RepoURL: selfRepoSpecURL, TargetRevision: "feature"}, want: true},
		{name: "self repo over scp syntax", source: render.ResolvedSource{RepoURL: "git@github.com:example/repo.git", TargetRevision: "HEAD"}, want: true},
		{name: "self repo with missing path", source: render.ResolvedSource{RepoURL: selfRepoSpecURL, TargetRevision: "HEAD", Path: "missing"}, want: true},
		{name: "self repo at untracked branch", source: render.ResolvedSource{RepoURL: selfRepoSpecURL, TargetRevision: "release-1.x"}},
		{name: "self repo at pinned SHA", source: render.ResolvedSource{RepoURL: selfRepoSpecURL, TargetRevision: "1111111111111111111111111111111111111111"}},
		{name: "foreign repo", source: render.ResolvedSource{RepoURL: foreignURL, TargetRevision: "HEAD"}},
		{name: "foreign repo with path under root", source: render.ResolvedSource{RepoURL: foreignURL, TargetRevision: "HEAD", Path: "values"}, want: true},
		{name: "foreign repo with missing path", source: render.ResolvedSource{RepoURL: foreignURL, TargetRevision: "HEAD", Path: "missing"}},
		{name: "foreign repo with escaping path", source: render.ResolvedSource{RepoURL: foreignURL, TargetRevision: "HEAD", Path: "../outside"}},
		{name: "repo-mapped to root", source: render.ResolvedSource{RepoURL: mappedRootURL, TargetRevision: "HEAD"}, want: true},
		{name: "repo-mapped to root with missing path", source: render.ResolvedSource{RepoURL: mappedRootURL, Path: "missing"}, want: true},
		{name: "repo-mapped elsewhere", source: render.ResolvedSource{RepoURL: mappedElsewhereURL, TargetRevision: "HEAD"}},
		{name: "repo-mapped elsewhere with path under root", source: render.ResolvedSource{RepoURL: mappedElsewhereURL, Path: "values"}},
		{name: "OCI", source: render.ResolvedSource{RepoURL: "oci://registry.example.com/values", TargetRevision: "1.0.0"}},
		{name: "empty repoURL", source: render.ResolvedSource{}, want: true},
		{name: "chart-only", source: render.ResolvedSource{RepoURL: "https://charts.example.test", Chart: "demo", TargetRevision: "1.2.3"}, divergent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gitAcquirer := &countingGitAcquirer{err: errors.New("ref root must not be acquired")}
			provider, cleanup, err := newLocalProvider(context.Background(), Orchestrator{
				GitAcquirer:         gitAcquirer,
				OCIArtifactAcquirer: erroringOCIArtifactAcquirer{},
			}, root, config.ArgoSettings{}, BuildRequest{Path: root, RepoMaps: repoMaps, selfRepo: selfRepo}, nil, "drydock-ref-root-drift-*")
			if err != nil {
				t.Fatalf("newLocalProvider() error = %v", err)
			}
			defer cleanup()
			resolved, err := provider.resolveSourceRoot(context.Background(), tc.source)
			providerRoot := err == nil && sameLocalPath(resolved, root) && gitAcquirer.calls() == 0

			got := refSourceRendersFromRoot(newSelectionTree(root, repoMaps, selfRepo), tc.source)
			if got != tc.want {
				t.Fatalf("refSourceRendersFromRoot() = %v, want %v", got, tc.want)
			}
			if providerRoot != (tc.want != tc.divergent) {
				t.Fatalf("provider resolves root = %v (resolved %q, err %v, git calls %d); refSourceRendersFromRoot() = %v, divergent = %v",
					providerRoot, resolved, err, gitAcquirer.calls(), got, tc.divergent)
			}
		})
	}
}

func TestWithSelectionOnlyPathsOwnsLocalRefInputs(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "values", "web.yaml"), "value: v\n")
	writeTestFile(t, filepath.Join(root, "values", "web.txt"), "v")
	writeTestFile(t, filepath.Join(root, "envs", "prod", "web.yaml"), "value: v\n")
	writeTestFile(t, filepath.Join(root, "charts", "web", "Chart.yaml"), "apiVersion: v2\nname: web\nversion: 0.1.0\n")
	const (
		foreignURL         = "https://github.com/example/other"
		mappedRootURL      = "https://github.com/example/mapped-root"
		mappedElsewhereURL = "https://github.com/example/mapped-elsewhere"
	)
	repoMaps := []sourcepkg.RepoMap{
		{URL: mappedRootURL, Path: root},
		{URL: mappedElsewhereURL, Path: t.TempDir()},
	}
	selfRepo := selfRepoRefs{urlKeys: []string{sourcepkg.CanonicalGitURLKey(selfRepoRemoteURL)}}

	// consumer is the standard shape: the chart comes from a Helm repository
	// and never renders from root itself.
	consumer := func(valueFiles []string, fileParameters ...string) argoappv1.ApplicationSource {
		helm := &argoappv1.ApplicationSourceHelm{ValueFiles: valueFiles}
		for _, parameter := range fileParameters {
			helm.FileParameters = append(helm.FileParameters, argoappv1.HelmFileParameter{Name: "value", Path: parameter})
		}
		return argoappv1.ApplicationSource{RepoURL: "https://charts.example.test", Chart: "web", TargetRevision: "1.0.0", Helm: helm}
	}
	ref := func(repoURL, revision string) argoappv1.ApplicationSource {
		return argoappv1.ApplicationSource{RepoURL: repoURL, TargetRevision: revision, Ref: "values"}
	}
	selfRef := ref(selfRepoSpecURL, "HEAD")
	values := []string{"$values/values/web.yaml"}

	for _, tc := range []struct {
		name    string
		sources argoappv1.ApplicationSources
		want    []string
	}{
		{name: "self repo value file", sources: argoappv1.ApplicationSources{selfRef, consumer(values)}, want: []string{"values/web.yaml"}},
		{name: "self repo file parameter", sources: argoappv1.ApplicationSources{selfRef, consumer(nil, "$values/values/web.txt")}, want: []string{"values/web.txt"}},
		{name: "self repo glob", sources: argoappv1.ApplicationSources{selfRef, consumer([]string{"$values/envs/*/web.yaml"})}, want: []string{"envs"}},
		// An empty glob prefix would intersect every changed path.
		{name: "self repo glob at ref root", sources: argoappv1.ApplicationSources{selfRef, consumer([]string{"$values/*.yaml"})}},
		{name: "self repo escaping ref root", sources: argoappv1.ApplicationSources{selfRef, consumer([]string{"$values/../web.yaml"})}},
		{name: "self repo env templated", sources: argoappv1.ApplicationSources{selfRef, consumer([]string{"$values/$ARGOCD_ENV_X/web.yaml"})}},
		{name: "self repo at pinned SHA", sources: argoappv1.ApplicationSources{ref(selfRepoSpecURL, "1111111111111111111111111111111111111111"), consumer(values)}},
		{name: "unknown ref", sources: argoappv1.ApplicationSources{selfRef, consumer([]string{"$other/values/web.yaml"})}},
		{name: "foreign repo", sources: argoappv1.ApplicationSources{ref(foreignURL, "HEAD"), consumer(values)}},
		{name: "repo-mapped to root", sources: argoappv1.ApplicationSources{ref(mappedRootURL, "HEAD"), consumer(values)}, want: []string{"values/web.yaml"}},
		{name: "repo-mapped elsewhere", sources: argoappv1.ApplicationSources{ref(mappedElsewhereURL, "HEAD"), consumer(values)}},
		{name: "OCI", sources: argoappv1.ApplicationSources{ref("oci://registry.example.com/values", "1.0.0"), consumer(values)}},
		{name: "empty repoURL", sources: argoappv1.ApplicationSources{ref("", ""), consumer(values)}, want: []string{"values/web.yaml"}},
		// Rendering resolves the ref with a same-revision path source's path,
		// which is present under root.
		{
			name: "foreign repo borrowing a local path",
			sources: argoappv1.ApplicationSources{
				ref(foreignURL, "HEAD"),
				{RepoURL: foreignURL, TargetRevision: "HEAD", Path: "charts/web"},
				consumer(values),
			},
			want: []string{"values/web.yaml"},
		},
		// Rendering resolves the ref without its own path, so it is fetched.
		{
			name: "foreign repo with its own local path",
			sources: argoappv1.ApplicationSources{
				{RepoURL: foreignURL, TargetRevision: "HEAD", Path: "charts/web", Ref: "values"},
				consumer(values),
			},
		},
		// A ref naming the consumer's own repository revision reads the
		// consumer's root; the glob is beyond the listed same-repo rule.
		{
			name: "same revision as a local consumer",
			sources: argoappv1.ApplicationSources{
				ref(foreignURL, "HEAD"),
				{RepoURL: foreignURL, TargetRevision: "HEAD", Path: "charts/web", Helm: &argoappv1.ApplicationSourceHelm{ValueFiles: []string{"$values/envs/*/web.yaml"}}},
			},
			want: []string{"envs"},
		},
		// Each entry is judged by its own ref root: the local one owns its
		// path, the foreign one does not.
		{
			name: "local and foreign refs in one app",
			sources: argoappv1.ApplicationSources{
				selfRef,
				{RepoURL: foreignURL, TargetRevision: "HEAD", Ref: "other"},
				consumer([]string{"$values/values/web.yaml", "$other/envs/prod/web.yaml"}, "$other/values/web.txt", "$values/values/web.txt"),
			},
			want: []string{"values/web.yaml", "values/web.txt"},
		},
		// Duplicate refs fail to plan, and so to render.
		{name: "duplicate ref", sources: argoappv1.ApplicationSources{selfRef, selfRef, consumer(values)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := ApplicationSelectionInput{
				Application: argoappv1.Application{Name: "web", Spec: argoappv1.ApplicationSpec{Sources: tc.sources}},
				Paths:       []string{"apps/web.yaml"},
			}
			got := withSelectionOnlyPaths(context.Background(), root, repoMaps, selfRepo, []ApplicationSelectionInput{input}).inputs
			if added := got[0].Paths[len(input.Paths):]; !slices.Equal(added, tc.want) {
				t.Fatalf("selection-only paths = %v, want %v", added, tc.want)
			}
			if len(input.Paths) != 1 {
				t.Fatalf("input Paths mutated to %v; listed inputs also key the render cache", input.Paths)
			}
		})
	}
}
