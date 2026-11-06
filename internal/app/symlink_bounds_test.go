package app

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	argoenv "github.com/argoproj/argo-cd/v3/util/env"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/sholdee/drydock/internal/chart"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/render"
	sourcepkg "github.com/sholdee/drydock/internal/source"
)

const symlinkBoundsConfigMap = "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cm\n"

func symlinkBoundsLink(t *testing.T, target, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
}

// TestAllowOutOfBoundsSymlinksParsesLikeTheRepoServer compares drydock's
// reading of reposerver.allow.oob.symlinks with the upstream
// env.ParseBoolFromEnv that the Argo CD v3.6.1 repo-server uses for
// ARGOCD_REPO_SERVER_ALLOW_OUT_OF_BOUNDS_SYMLINKS
// (cmd/argocd-repo-server/commands/argocd_repo_server.go:266).
func TestAllowOutOfBoundsSymlinksParsesLikeTheRepoServer(t *testing.T) {
	const envVar = "ARGOCD_REPO_SERVER_ALLOW_OUT_OF_BOUNDS_SYMLINKS"
	for _, value := range []string{"", "true", "TRUE", "True", "false", "FALSE", "yes", "1", " true", "true "} {
		t.Setenv(envVar, value)
		want := argoenv.ParseBoolFromEnv(envVar, false)
		settings := config.DefaultSettings()
		settings.AllowOutOfBoundsSymlinks.Value = value
		if got := allowOutOfBoundsSymlinks(settings); got != want {
			t.Fatalf("allowOutOfBoundsSymlinks(%q) = %t, upstream %t", value, got, want)
		}
	}
}

// TestRenderSettingsSignatureSignsAllowOutOfBoundsSymlinks pins that the
// render cache keys and the changed-only settings signature see the parsed
// flag: spellings of the same value give the same key.
func TestRenderSettingsSignatureSignsAllowOutOfBoundsSymlinks(t *testing.T) {
	signatures := func(value string) (string, string) {
		t.Helper()
		settings := config.DefaultSettings()
		if value != "" {
			settings.AllowOutOfBoundsSymlinks = config.Value[string]{Value: value, Provenance: config.Provenance{Path: "argocd-cmd-params-cm.yaml", Pointer: "data." + value}}
		}
		rendered, err := renderSettingsSignature(settings)
		if err != nil {
			t.Fatal(err)
		}
		changed, err := changedOnlySettingsSignature(settings)
		if err != nil {
			t.Fatal(err)
		}
		return rendered, changed
	}
	offRender, offChanged := signatures("")
	for _, value := range []string{"false", "no", "1"} {
		if rendered, changed := signatures(value); rendered != offRender || changed != offChanged {
			t.Fatalf("signatures for %q differ from the default false", value)
		}
	}
	onRender, onChanged := signatures("true")
	if onRender == offRender || onChanged == offChanged {
		t.Fatal("signatures for true equal the default false")
	}
	if rendered, changed := signatures("TRUE"); rendered != onRender || changed != onChanged {
		t.Fatal("signatures for TRUE differ from true")
	}
}

// TestLocalProviderFailsOnOutOfBoundsSymlinkAnywhere pins the repo-wide
// check of a Git source (reposerver/repository/repository.go:499-514 at
// v3.6.1): an out-of-bounds symlink outside the source path fails the
// source, and reposerver.allow.oob.symlinks turns the check off.
func TestLocalProviderFailsOnOutOfBoundsSymlinkAnywhere(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "cm.yaml"), symlinkBoundsConfigMap)
	symlinkBoundsLink(t, "/etc/hosts", filepath.Join(root, "other", "escape.yaml"))
	source := render.ResolvedSource{RepoRoot: root, Path: "apps"}

	_, _, err := (localProvider{}).RenderSource(context.Background(), source, render.RenderOptions{})
	if err == nil || !strings.Contains(err.Error(), "repository contains out-of-bounds symlinks. file: other/escape.yaml") {
		t.Fatalf("RenderSource() error = %v, want the repo-server out-of-bounds error", err)
	}
	manifests, _, err := (localProvider{allowOutOfBoundsSymlinks: true}).RenderSource(context.Background(), source, render.RenderOptions{})
	if err != nil || len(manifests) != 1 {
		t.Fatalf("RenderSource() with the check off = %d manifests, %v", len(manifests), err)
	}
}

// TestLocalProviderSkipsIgnoredUntrackedOutOfBoundsSymlinks pins that the
// check of a Git worktree leaves out paths that Git ignores and does not
// track, which no Argo CD checkout holds, and keeps a tracked one.
func TestLocalProviderSkipsIgnoredUntrackedOutOfBoundsSymlinks(t *testing.T) {
	root := t.TempDir()
	repo, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(root, ".gitignore"), ".venv/\nresult\n")
	writeTestFile(t, filepath.Join(root, "apps", "cm.yaml"), symlinkBoundsConfigMap)
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if err := worktree.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Commit("fixture", &git.CommitOptions{Author: &object.Signature{Name: "Test", Email: "test@example.invalid"}}); err != nil {
		t.Fatal(err)
	}
	symlinkBoundsLink(t, "/usr/bin/python3", filepath.Join(root, ".venv", "bin", "python"))
	symlinkBoundsLink(t, "/nix/store/x", filepath.Join(root, "result"))
	source := render.ResolvedSource{RepoRoot: root, Path: "apps"}
	if _, _, err := (localProvider{userRoots: userRootSet(root, "", nil)}).RenderSource(context.Background(), source, render.RenderOptions{}); err != nil {
		t.Fatalf("RenderSource() error = %v, want ignored untracked symlinks left out", err)
	}

	// The same symlink, tracked, is in every checkout.
	if _, err := worktree.Add("result"); err != nil {
		t.Fatal(err)
	}
	_, _, err = (localProvider{userRoots: userRootSet(root, "", nil)}).RenderSource(context.Background(), source, render.RenderOptions{})
	if err == nil || !strings.Contains(err.Error(), "repository contains out-of-bounds symlinks. file: result") {
		t.Fatalf("RenderSource() error = %v, want the tracked symlink reported", err)
	}
}

// TestLocalProviderChecksEachScope pins the repo-server message of each tree
// it checks: an OCI artifact (repository.go:429-444 at v3.6.1), a chart of
// a Helm repository (470-485), and a Git repository that a $ref value file
// reads (941-959).
func TestLocalProviderChecksEachScope(t *testing.T) {
	t.Run("OCI artifact", func(t *testing.T) {
		artifact := t.TempDir()
		writeTestFile(t, filepath.Join(artifact, "apps", "cm.yaml"), symlinkBoundsConfigMap)
		symlinkBoundsLink(t, "../../outside", filepath.Join(artifact, "apps", "escape"))
		provider := localProvider{
			repoRoot:       t.TempDir(),
			sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{RepoMaps: []sourcepkg.RepoMap{{URL: "oci://registry.example.test/parity/app", Path: artifact}}}),
		}
		_, _, err := provider.RenderSource(context.Background(), render.ResolvedSource{RepoURL: "oci://registry.example.test/parity/app", TargetRevision: "v1", Path: "apps"}, render.RenderOptions{})
		if err == nil || !strings.Contains(err.Error(), "oci image contains out-of-bounds symlinks. file: apps/escape") {
			t.Fatalf("RenderSource() error = %v, want the OCI out-of-bounds error", err)
		}
	})
	t.Run("Helm repository chart", func(t *testing.T) {
		chartDir := filepath.Join(t.TempDir(), "demo")
		writeTestFile(t, filepath.Join(chartDir, "Chart.yaml"), "apiVersion: v2\nname: demo\nversion: 1.0.0\n")
		symlinkBoundsLink(t, "/etc/hosts", filepath.Join(chartDir, "templates", "escape.yaml"))
		provider := localProvider{
			repoRoot:      t.TempDir(),
			chartAcquirer: fixedChartAcquirer{dir: chartDir},
		}
		_, _, err := provider.RenderSource(context.Background(), render.ResolvedSource{RepoURL: "https://charts.example.test", Chart: "demo", TargetRevision: "1.0.0"}, render.RenderOptions{})
		if err == nil || !strings.Contains(err.Error(), "chart contains out-of-bounds symlinks. file: templates/escape.yaml") {
			t.Fatalf("RenderSource() error = %v, want the chart out-of-bounds error", err)
		}
	})
	refValues := func(t *testing.T) (string, sourcepkg.RepoMap) {
		t.Helper()
		values := t.TempDir()
		writeTestFile(t, filepath.Join(values, "values.yaml"), "a: 1\n")
		symlinkBoundsLink(t, "/etc/hosts", filepath.Join(values, "escape.yaml"))
		return values, sourcepkg.RepoMap{URL: "https://git.example.test/values.git", Path: values}
	}
	refOptions := render.RenderOptions{
		ValueFiles: []string{"$values/values.yaml"},
		RefSources: map[string]render.ResolvedSource{"$values": {RepoURL: "https://git.example.test/values.git", TargetRevision: "main"}},
	}
	t.Run("$ref repository", func(t *testing.T) {
		root := refApplicationRoot(t, "")
		_, valuesMap := refValues(t)
		provider := localProvider{repoRoot: root, sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{RepoMaps: []sourcepkg.RepoMap{valuesMap}})}
		_, err := RenderApplication(context.Background(), refApplication(), provider)
		if err == nil || !strings.Contains(err.Error(), "ref root $values: repository contains out-of-bounds symlinks. file: escape.yaml") {
			t.Fatalf("RenderApplication() error = %v, want the $ref out-of-bounds error", err)
		}
	})
	// The repo-server checks the $ref repositories of the spec before it
	// reads .argocd-source.yaml (reposerver/repository/repository.go:870-967
	// at v3.6.1).
	for name, override := range map[string]string{
		"an override that replaces the value files": "helm:\n  valueFiles:\n    - values.yaml\n",
		"a malformed override":                      "helm: [not, a, map\n",
	} {
		t.Run("$ref repository before "+name, func(t *testing.T) {
			root := refApplicationRoot(t, override)
			_, valuesMap := refValues(t)
			provider := localProvider{repoRoot: root, sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{RepoMaps: []sourcepkg.RepoMap{valuesMap}})}
			_, err := RenderApplication(context.Background(), refApplication(), provider)
			if err == nil || !strings.Contains(err.Error(), "ref root $values: repository contains out-of-bounds symlinks. file: escape.yaml") {
				t.Fatalf("RenderApplication() error = %v, want the $ref out-of-bounds error", err)
			}
		})
	}
	t.Run("a Helm repository chart is checked before its $ref repository", func(t *testing.T) {
		_, valuesMap := refValues(t)
		chartDir := filepath.Join(t.TempDir(), "demo")
		writeTestFile(t, filepath.Join(chartDir, "Chart.yaml"), "apiVersion: v2\nname: demo\nversion: 1.0.0\n")
		symlinkBoundsLink(t, "/etc/hosts", filepath.Join(chartDir, "templates", "escape.yaml"))
		provider := localProvider{
			repoRoot:       t.TempDir(),
			chartAcquirer:  fixedChartAcquirer{dir: chartDir},
			sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{RepoMaps: []sourcepkg.RepoMap{valuesMap}}),
		}
		_, _, err := provider.RenderSource(context.Background(), render.ResolvedSource{RepoURL: "https://charts.example.test", Chart: "demo", TargetRevision: "1.0.0"}, refOptions)
		if err == nil || !strings.Contains(err.Error(), "chart contains out-of-bounds symlinks. file: templates/escape.yaml") {
			t.Fatalf("RenderSource() error = %v, want the chart error first", err)
		}
	})
}

// TestRenderApplicationChecksBeforeOverridesAndOCIRefOnlySources pins two
// repo-server orders. The tree is checked before GenerateManifests reads
// .argocd-source.yaml, and an oci:// source that only sets ref is extracted
// and checked, because the ref-only skip at reposerver/repository/
// repository.go:670 (v3.6.1) leaves out OCI sources.
func TestRenderApplicationChecksBeforeOverridesAndOCIRefOnlySources(t *testing.T) {
	t.Run("override", func(t *testing.T) {
		root := t.TempDir()
		writeTestFile(t, filepath.Join(root, "app", "cm.yaml"), symlinkBoundsConfigMap)
		writeTestFile(t, filepath.Join(root, "app", ".argocd-source.yaml"), "directory: [not, a, map\n")
		symlinkBoundsLink(t, "/etc/hosts", filepath.Join(root, "other", "escape.yaml"))
		application := argoappv1.Application{
			Name: "demo",
			Spec: argoappv1.ApplicationSpec{Source: &argoappv1.ApplicationSource{RepoURL: "https://git.example.test/repo.git", TargetRevision: "main", Path: "app"}},
		}
		provider := localProvider{repoRoot: root, sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{})}
		_, err := RenderApplication(context.Background(), application, provider)
		if err == nil || !strings.Contains(err.Error(), "repository contains out-of-bounds symlinks. file: other/escape.yaml") {
			t.Fatalf("RenderApplication() error = %v, want the out-of-bounds error before the override error", err)
		}
	})
	t.Run("OCI ref-only source", func(t *testing.T) {
		root := t.TempDir()
		writeTestFile(t, filepath.Join(root, "app", "cm.yaml"), symlinkBoundsConfigMap)
		artifact := t.TempDir()
		writeTestFile(t, filepath.Join(artifact, "values.yaml"), "a: 1\n")
		symlinkBoundsLink(t, "/etc/hosts", filepath.Join(artifact, "escape.yaml"))
		application := argoappv1.Application{
			Name: "demo",
			Spec: argoappv1.ApplicationSpec{Sources: argoappv1.ApplicationSources{
				{RepoURL: "https://git.example.test/repo.git", TargetRevision: "main", Path: "app"},
				{RepoURL: "oci://registry.example.test/parity/values", TargetRevision: "v1", Ref: "values"},
			}},
		}
		provider := localProvider{
			repoRoot:       root,
			sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{RepoMaps: []sourcepkg.RepoMap{{URL: "oci://registry.example.test/parity/values", Path: artifact}}}),
		}
		_, err := RenderApplication(context.Background(), application, provider)
		if err == nil || !strings.Contains(err.Error(), "source[1]") || !strings.Contains(err.Error(), "oci image contains out-of-bounds symlinks. file: escape.yaml") {
			t.Fatalf("RenderApplication() error = %v, want the OCI ref-only source checked", err)
		}
		// With the check off nothing renders from the source and nothing
		// is checked, so drydock does not acquire it: the repo-map now
		// points nowhere, and the render still succeeds.
		provider.allowOutOfBoundsSymlinks = true
		provider.sourceResolver = sourcepkg.NewResolver(sourcepkg.Options{RepoMaps: []sourcepkg.RepoMap{{URL: "oci://registry.example.test/parity/values", Path: filepath.Join(t.TempDir(), "missing")}}})
		if _, err := RenderApplication(context.Background(), application, provider); err != nil {
			t.Fatalf("RenderApplication() with the check off error = %v, want the OCI ref-only source left alone", err)
		}
	})
}

// refApplicationRoot writes a local Helm chart at chart/ with a values file
// and, when override is set, a .argocd-source.yaml next to it.
func refApplicationRoot(t *testing.T, override string) string {
	t.Helper()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "chart", "Chart.yaml"), "apiVersion: v2\nname: demo\nversion: 1.0.0\n")
	writeTestFile(t, filepath.Join(root, "chart", "values.yaml"), "a: 1\n")
	if override != "" {
		writeTestFile(t, filepath.Join(root, "chart", ".argocd-source.yaml"), override)
	}
	return root
}

// refApplication renders chart/ with the value file $values/values.yaml
// from a second repository.
func refApplication() argoappv1.Application {
	return argoappv1.Application{
		Name: "demo",
		Spec: argoappv1.ApplicationSpec{Sources: argoappv1.ApplicationSources{
			{RepoURL: "https://git.example.test/repo.git", TargetRevision: "main", Path: "chart", Helm: &argoappv1.ApplicationSourceHelm{ValueFiles: []string{"$values/values.yaml"}}},
			{RepoURL: "https://git.example.test/values.git", TargetRevision: "main", Ref: "values"},
		}},
	}
}

type fixedChartAcquirer struct{ dir string }

func (a fixedChartAcquirer) Acquire(_ context.Context, request chart.Request, _ chart.Options) (chart.Result, error) {
	return chart.Result{ChartDir: a.dir, Repository: request.Repository, Name: request.Name, Version: request.Version, Kind: request.Kind}, nil
}

// TestPersistentRenderIdentityCarriesOutOfBoundsSymlink pins that the
// persistent render cache key of a source that renders from the provider
// root changes when an out-of-bounds symlink appears anywhere in the root,
// even outside every digested input path, and does not when the check is
// off.
func TestPersistentRenderIdentityCarriesOutOfBoundsSymlink(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "cm.yaml"), symlinkBoundsConfigMap)
	application := argoappv1.Application{
		Name: "demo",
		Spec: argoappv1.ApplicationSpec{Source: &argoappv1.ApplicationSource{RepoURL: "https://git.example.test/repo.git", TargetRevision: "main", Path: "apps"}},
	}
	plan := mustPlan(t, application)
	identities := func(allow bool) []SourceIdentity {
		t.Helper()
		provider := localProvider{
			repoRoot:                 root,
			sourceResolver:           sourcepkg.NewResolver(sourcepkg.Options{}),
			rootIdentity:             SourceIdentity{Kind: sourceIdentityKindRoot, Revision: "0123456789abcdef"},
			rootInputMode:            rootInputModeSnapshot,
			allowOutOfBoundsSymlinks: allow,
		}
		vector, reason, ok := collectSourceIdentities(context.Background(), nil, provider, plan)
		if !ok {
			t.Fatalf("collectSourceIdentities() not eligible: %s", reason)
		}
		return vector
	}
	before := identities(false)
	symlinkBoundsLink(t, "/etc/hosts", filepath.Join(root, "docs", "escape.yaml"))
	after := identities(false)
	if slices.Equal(before, after) || after[0].OutOfBoundsSymlink != "docs/escape.yaml" {
		t.Fatalf("identities before = %#v, after = %#v, want the symlink in the key", before, after)
	}
	if allowed := identities(true); !slices.Equal(before, allowed) {
		t.Fatalf("identities with the check off = %#v, want %#v", allowed, before)
	}
}

// TestOrchestratorDiffAppsChangedOnlyRendersAllOnOutOfBoundsSymlink pins
// that a changed-only diff renders every Application when an out-of-bounds
// symlink appears. The changed path belongs to alpha alone, but the symlink
// fails beta in Argo CD too.
func TestOrchestratorDiffAppsChangedOnlyRendersAllOnOutOfBoundsSymlink(t *testing.T) {
	root := t.TempDir()
	for _, side := range []string{"left", "right"} {
		for _, name := range []string{"alpha", "beta"} {
			writeTestFile(t, filepath.Join(root, side, "apps", name+".yaml"), "apiVersion: argoproj.io/v1alpha1\nkind: Application\nmetadata:\n  name: "+name+"\n  namespace: argocd\nspec:\n  source:\n    repoURL: https://github.com/example/repo\n    targetRevision: main\n    path: "+name+"\n  destination:\n    server: https://kubernetes.default.svc\n    namespace: default\n")
			writeTestFile(t, filepath.Join(root, side, name, "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: "+name+"\n")
		}
	}
	symlinkBoundsLink(t, "/etc/hosts", filepath.Join(root, "right", "alpha", "escape.yaml"))
	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		LeftPath:    filepath.Join(root, "left"),
		RightPath:   filepath.Join(root, "right"),
		ChangedOnly: true,
		Unified:     3,
	})
	for _, name := range []string{"alpha", "beta"} {
		want := "argocd/" + name + ": Application argocd/" + name + " source[0] path=\"" + name + "\": repository contains out-of-bounds symlinks. file: alpha/escape.yaml"
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("DiffApps() error = %v, want %q", err, want)
		}
	}
	want := changedOnlySymlinksDiagnostic("", "alpha/escape.yaml")
	if !slices.Contains(result.Diagnostics, want) {
		t.Fatalf("diagnostics = %#v, want %#v", result.Diagnostics, want)
	}
	if err := diagnosticFailure(normalizeDiagnostics([]diagnostic.Diagnostic{want}, true, false), true); err != nil {
		t.Fatalf("strict diagnosticFailure() = %v, want the symlink render-all exempt", err)
	}
}

// TestChangedOnlySymlinksRenderAllWhenTheCheckFails pins that a tree that
// drydock cannot walk does not stop a changed-only diff: every Application
// renders, and each source then fails on its own, as in a full diff.
// change.Detect walks the same trees first, so this needs a tree that fails
// only the symlink walk, such as one that is gone.
func TestChangedOnlySymlinksRenderAllWhenTheCheckFails(t *testing.T) {
	left := t.TempDir()
	missing := filepath.Join(t.TempDir(), "gone")
	settings := config.DefaultSettings()
	renderAll, diag, err := changedOnlySymlinksRenderAll(context.Background(), newSymlinkBoundsMemo(), BuildRequest{Path: left}, BuildRequest{Path: missing}, settings, settings)
	if err != nil || !renderAll || diag.Code != changedOnlySymlinksCode || !strings.Contains(diag.Message, "could not check the repository for out-of-bounds symlinks") {
		t.Fatalf("changedOnlySymlinksRenderAll() = %t, %#v, %v, want a render-all warning", renderAll, diag, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := changedOnlySymlinksRenderAll(ctx, newSymlinkBoundsMemo(), BuildRequest{Path: left}, BuildRequest{Path: missing}, settings, settings); err == nil {
		t.Fatal("changedOnlySymlinksRenderAll() with a canceled context = nil error")
	}
	if renderAll, _, err := changedOnlySymlinksRenderAll(context.Background(), newSymlinkBoundsMemo(), BuildRequest{Path: left}, BuildRequest{Path: left}, settings, settings); err != nil || renderAll {
		t.Fatalf("changedOnlySymlinksRenderAll() for equal trees = %t, %v", renderAll, err)
	}
}

// TestSymlinkBoundsMemoWalksEachTreeOnce pins that the run memo walks a tree
// once and keeps different trees apart, also when callers ask at the same
// time.
func TestSymlinkBoundsMemoWalksEachTreeOnce(t *testing.T) {
	bad := t.TempDir()
	symlinkBoundsLink(t, "/etc/hosts", filepath.Join(bad, "escape.yaml"))
	good := t.TempDir()
	memo := newSymlinkBoundsMemo()
	var wg sync.WaitGroup
	results := make([]string, 8)
	for i := range results {
		wg.Go(func() {
			root := good
			if i%2 == 0 {
				root = bad
			}
			results[i], _ = memo.findOutOfBoundsSymlink(context.Background(), root, render.OutOfBoundsSymlinkScopeRepository, true)
		})
	}
	wg.Wait()
	for i, got := range results {
		want := ""
		if i%2 == 0 {
			want = "escape.yaml"
		}
		if got != want {
			t.Fatalf("result %d = %q, want %q", i, got, want)
		}
	}
	if err := os.Remove(filepath.Join(bad, "escape.yaml")); err != nil {
		t.Fatal(err)
	}
	if got, _ := memo.findOutOfBoundsSymlink(context.Background(), bad, render.OutOfBoundsSymlinkScopeRepository, true); got != "escape.yaml" {
		t.Fatalf("second check = %q, want the kept answer", got)
	}
}

// TestLocalProviderSkipsIgnoredPathsUnderASubdirectoryRoot pins the review
// repro: the root is a subdirectory of a Git worktree, and an ignored,
// untracked virtual environment under it holds an absolute symlink. Git
// ignores it, so the check leaves it out, as for a root at the top level.
func TestLocalProviderSkipsIgnoredPathsUnderASubdirectoryRoot(t *testing.T) {
	top := t.TempDir()
	repo, err := git.PlainInit(top, false)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(top, ".gitignore"), ".venv/\n")
	writeTestFile(t, filepath.Join(top, "gitops", "app", "cm.yaml"), symlinkBoundsConfigMap)
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if err := worktree.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Commit("fixture", &git.CommitOptions{Author: &object.Signature{Name: "Test", Email: "test@example.invalid"}}); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(top, "gitops")
	symlinkBoundsLink(t, "/usr/bin/python3", filepath.Join(root, ".venv", "bin", "python"))
	if _, _, err := (localProvider{userRoots: userRootSet(root, "", nil)}).RenderSource(context.Background(), render.ResolvedSource{RepoRoot: root, Path: "app"}, render.RenderOptions{}); err != nil {
		t.Fatalf("RenderSource() error = %v, want the ignored virtual environment left out", err)
	}
}

// TestRenderApplicationReportsAnUnknownRefBeforeTheTreeCheck pins the
// repo-server order for a multi-source Helm Application: an unknown $ref
// fails in resolveReferencedSources before the tree check
// (reposerver/repository/repository.go:391 and 499 at v3.6.1).
func TestRenderApplicationReportsAnUnknownRefBeforeTheTreeCheck(t *testing.T) {
	root := refApplicationRoot(t, "")
	symlinkBoundsLink(t, "/etc/hosts", filepath.Join(root, "other", "escape.yaml"))
	application := refApplication()
	application.Spec.Sources[0].Helm.ValueFiles = []string{"$missing/values.yaml"}
	provider := localProvider{repoRoot: root, sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{})}
	_, err := RenderApplication(context.Background(), application, provider)
	if err == nil || !strings.Contains(err.Error(), "unknown ref $missing") || strings.Contains(err.Error(), "out-of-bounds") {
		t.Fatalf("RenderApplication() error = %v, want the unknown $ref error first", err)
	}
}

// TestOrchestratorDiffRefsChecksSnapshotsWhole pins that drydock's own
// snapshots get no Git ignore rules, also when TMPDIR is inside another
// Git worktree whose rules would hide the committed out-of-bounds symlink.
func TestOrchestratorDiffRefsChecksSnapshotsWhole(t *testing.T) {
	outer := t.TempDir()
	outerRepo, err := git.PlainInit(outer, false)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(outer, ".gitignore"), "*\n!.gitignore\n!tmp/\n")
	writeTestFile(t, filepath.Join(outer, "tmp", "keep"), "x\n")
	outerWorktree, err := outerRepo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := outerWorktree.Add(".gitignore"); err != nil {
		t.Fatal(err)
	}
	if _, err := outerWorktree.Add("tmp/keep"); err != nil {
		t.Fatal(err)
	}
	if _, err := outerWorktree.Commit("outer", &git.CommitOptions{Author: &object.Signature{Name: "Test", Email: "test@example.invalid"}}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", filepath.Join(outer, "tmp"))

	root := t.TempDir()
	repo, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	commit := func(message string) {
		t.Helper()
		if err := worktree.AddWithOptions(&git.AddOptions{All: true}); err != nil {
			t.Fatal(err)
		}
		if _, err := worktree.Commit(message, &git.CommitOptions{Author: &object.Signature{Name: "Test", Email: "test@example.invalid"}}); err != nil {
			t.Fatal(err)
		}
	}
	writeTestFile(t, filepath.Join(root, "apps", "demo.yaml"), symlinkedRootApplication("demo", "https://git.example.test/repo.git", "app"))
	writeTestFile(t, filepath.Join(root, "app", "cm.yaml"), symlinkBoundsConfigMap)
	commit("base")
	symlinkBoundsLink(t, "../../outside.yaml", filepath.Join(root, "other", "escape.yaml"))
	commit("escape")

	_, err = Orchestrator{}.DiffApps(context.Background(), DiffRequest{Repo: root, Ref: "HEAD", RefOrig: "HEAD~1", Unified: 3})
	if err == nil || !strings.Contains(err.Error(), "repository contains out-of-bounds symlinks. file: other/escape.yaml") {
		t.Fatalf("DiffApps() error = %v, want the committed symlink reported", err)
	}
}
