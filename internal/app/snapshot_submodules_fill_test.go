package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/gitref"
	"github.com/sholdee/drydock/internal/gittree"
	"github.com/sholdee/drydock/internal/render"
)

// snapshotFillRequest takes a snapshot of the submodule fixture and
// returns its root with the BuildRequest of a diff side that renders it.
func snapshotFillRequest(t *testing.T) (string, BuildRequest) {
	t.Helper()
	repo, _ := submoduleApplicationRepo(t, true)
	result, err := gitref.Snapshot(context.Background(), gitref.Request{Repo: repo, Ref: "HEAD", KeepOutOfBoundsSymlinks: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = result.Cleanup() })
	diff := DiffRequest{Offline: true, GitCacheDir: t.TempDir()}
	request := BuildRequest{Offline: true, GitCacheDir: diff.GitCacheDir}
	request.rootRevision = result.Revision
	request.rootRepo = repo
	request.rootSnapshot = snapshotSubmodulesOf(result, diff.snapshotSubmoduleOptions(repo))
	request.symlinkBounds = newSymlinkBoundsMemo()
	return result.Path, request
}

func submodulesOff() config.ArgoSettings {
	var settings config.ArgoSettings
	settings.EnableGitSubmodules.Value = "false"
	return settings
}

// TestAppsetOCIFetcherLeavesSnapshotSubmodulesAlone pins that the provider
// of the ApplicationSet OCI fetcher, which has empty settings and never
// renders from the root, does not fill the submodules of a snapshot root
// that the run's settings turned off.
func TestAppsetOCIFetcherLeavesSnapshotSubmodulesAlone(t *testing.T) {
	root, request := snapshotFillRequest(t)
	fillSnapshotSubmodules(context.Background(), root, request, submodulesOff())
	child := filepath.Join(root, "mods", "child", "child", "app", "cm.yaml")
	fetcher := newAppsetOCIArtifactFetcher(context.Background(), Orchestrator{}, root, request)
	if _, err := fetcher.ensureProvider(); err != nil {
		t.Fatal(err)
	}
	defer fetcher.close()
	if _, err := os.Stat(child); !os.IsNotExist(err) {
		t.Fatalf("stat submodule content = %v, want the OCI fetcher to leave the root empty", err)
	}
}

// TestSnapshotFillStateGuardFailsSourcesOnChange pins that a provider whose
// snapshot root another provider emptied fails its sources loudly, through
// the render path (submoduleRenderDiagnostics), instead of reading a tree
// that changed under it.
func TestSnapshotFillStateGuardFailsSourcesOnChange(t *testing.T) {
	root, request := snapshotFillRequest(t)
	fill := fillSnapshotSubmodules(context.Background(), root, request, config.ArgoSettings{})
	provider := localProvider{repoRoot: root, gitSubmodules: true, submodules: newRootSubmodules(root, request, fill)}
	source := render.ResolvedSource{RepoRoot: root, Path: "apps"}
	if _, err := provider.submoduleRenderDiagnostics(context.Background(), source); err != nil {
		t.Fatalf("submoduleRenderDiagnostics() before the change = %v", err)
	}
	fillSnapshotSubmodules(context.Background(), root, request, submodulesOff())
	_, err := provider.submoduleRenderDiagnostics(context.Background(), source)
	if err == nil || !strings.Contains(err.Error(), "changed while drydock rendered") {
		t.Fatalf("submoduleRenderDiagnostics() after another provider emptied the root = %v, want a clear failure", err)
	}
}

// TestRenderErrorNamesMissingSubmodulesOnlyWhenFilled pins that a render
// error names the missing submodules only while the snapshot holds
// submodule content: an emptied root has no missing submodule to blame.
func TestRenderErrorNamesMissingSubmodulesOnlyWhenFilled(t *testing.T) {
	fill := &snapshotFill{result: gittree.SubmoduleResult{Missing: []gittree.MissingSubmodule{{Path: "mods/child", Err: errors.New("offline cache miss")}}}}
	base := errors.New("render failed")
	if got := fill.renderError(base); got.Error() != base.Error() {
		t.Fatalf("renderError() of an empty root = %v, want the error unchanged", got)
	}
	fill.filled = true
	if got := fill.renderError(base); !strings.Contains(got.Error(), `submodule "mods/child" could not be checked out`) {
		t.Fatalf("renderError() of a filled root = %v, want the missing submodule named", got)
	}
}
