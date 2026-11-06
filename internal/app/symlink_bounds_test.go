package app

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
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
	if _, _, err := (localProvider{}).RenderSource(context.Background(), source, render.RenderOptions{}); err != nil {
		t.Fatalf("RenderSource() error = %v, want ignored untracked symlinks left out", err)
	}

	// The same symlink, tracked, is in every checkout.
	if _, err := worktree.Add("result"); err != nil {
		t.Fatal(err)
	}
	_, _, err = (localProvider{}).RenderSource(context.Background(), source, render.RenderOptions{})
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
	t.Run("$ref repository", func(t *testing.T) {
		values := t.TempDir()
		writeTestFile(t, filepath.Join(values, "values.yaml"), "a: 1\n")
		symlinkBoundsLink(t, "/etc/hosts", filepath.Join(values, "escape.yaml"))
		provider := localProvider{
			repoRoot:       t.TempDir(),
			sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{RepoMaps: []sourcepkg.RepoMap{{URL: "https://git.example.test/values.git", Path: values}}}),
		}
		_, err := provider.resolveRefRoots(context.Background(), map[string]render.ResolvedSource{
			"$values": {RepoURL: "https://git.example.test/values.git", TargetRevision: "main"},
		})
		if err == nil || !strings.Contains(err.Error(), "ref root $values: repository contains out-of-bounds symlinks. file: escape.yaml") {
			t.Fatalf("resolveRefRoots() error = %v, want the $ref out-of-bounds error", err)
		}
	})
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
