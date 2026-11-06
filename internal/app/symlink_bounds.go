package app

import (
	"context"
	"errors"
	"fmt"

	"path/filepath"
	"strings"
	"sync"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/gitref"
	"github.com/sholdee/drydock/internal/ociartifact"
	"github.com/sholdee/drydock/internal/render"
)

// allowOutOfBoundsSymlinks reads reposerver.allow.oob.symlinks from
// argocd-cmd-params-cm. The Argo CD v3.6.1 repo-server reads it through
// env.ParseBoolFromEnv (util/env/env.go:184-193): "true" or "false" in any
// case, and the default false for an empty or any other value
// (cmd/argocd-repo-server/commands/argocd_repo_server.go:266).
func allowOutOfBoundsSymlinks(settings config.ArgoSettings) bool {
	return strings.EqualFold(settings.AllowOutOfBoundsSymlinks.Value, "true")
}

// symlinkBoundsMemo keeps the out-of-bounds symlink check of each tree for
// one run. Every Application that renders from a tree needs the same
// answer, and the run treats the tree as stable, as the render cache does.
// One memo serves the whole run: discovery, build, and changed-only
// selection share it through the request. Each tree has its own lock, so
// different trees are walked at the same time and one tree is walked once.
type symlinkBoundsMemo struct {
	mu      sync.Mutex
	entries map[string]*symlinkBoundsEntry
}

type symlinkBoundsEntry struct {
	mu   sync.Mutex
	done bool
	file string
	err  error
}

func newSymlinkBoundsMemo() *symlinkBoundsMemo {
	return &symlinkBoundsMemo{entries: map[string]*symlinkBoundsEntry{}}
}

func (m *symlinkBoundsMemo) entry(key string) *symlinkBoundsEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.entries[key]
	if !ok {
		entry = &symlinkBoundsEntry{}
		m.entries[key] = entry
	}
	return entry
}

// findOutOfBoundsSymlink returns the first out-of-bounds symlink of the tree
// at root, as the repo-server's CheckOutOfBoundsSymlinks names it, or "".
// A repository scope skips the root .git entry, as the repo-server does for
// a Git checkout, and, when root is the top level of a Git worktree, every
// path that Git ignores and does not track: no Argo CD checkout holds such a
// path, and a local worktree often does, such as a Python virtual
// environment or a Nix build result with absolute symlinks. A nil memo
// checks without keeping the answer.
func (m *symlinkBoundsMemo) findOutOfBoundsSymlink(ctx context.Context, root string, scope render.OutOfBoundsSymlinkScope) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	key := string(scope) + "\x00" + filepath.Clean(abs)
	if m == nil {
		return walkOutOfBoundsSymlinks(ctx, abs, scope)
	}
	entry := m.entry(key)
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.done {
		return entry.file, entry.err
	}
	file, err := walkOutOfBoundsSymlinks(ctx, abs, scope)
	// A canceled walk says nothing about the tree, so the next caller walks
	// again.
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		entry.done, entry.file, entry.err = true, file, err
	}
	return file, err
}

func walkOutOfBoundsSymlinks(ctx context.Context, abs string, scope render.OutOfBoundsSymlinkScope) (string, error) {
	opts := render.OutOfBoundsSymlinkOptions{}
	if scope == render.OutOfBoundsSymlinkScopeRepository {
		opts.SkipRootGit = true
		// A worktree whose Git data cannot be read is checked whole.
		if filter, filterErr := gitref.OpenUntrackedIgnoredFilter(abs); filterErr == nil {
			opts.Skip = filter.Skip
		}
	}
	return render.FindOutOfBoundsSymlink(ctx, abs, opts)
}

// checkOutOfBoundsSymlinks fails, as the Argo CD v3.6.1 repo-server does
// before it generates manifests, when the tree at root holds an
// out-of-bounds symlink and reposerver.allow.oob.symlinks is not true
// (reposerver/repository/repository.go:429-444 for an OCI artifact, 470-485
// for a chart of a Helm repository, 499-514 for a Git source, 941-959 for a
// Git $ref source of Helm value files).
func (p localProvider) checkOutOfBoundsSymlinks(ctx context.Context, root string, scope render.OutOfBoundsSymlinkScope) error {
	if p.allowOutOfBoundsSymlinks || strings.TrimSpace(root) == "" {
		return nil
	}
	file, err := p.symlinkBounds.findOutOfBoundsSymlink(ctx, root, scope)
	if err != nil {
		return err
	}
	if file != "" {
		return &render.OutOfBoundsSymlinkError{Scope: scope, File: file}
	}
	return nil
}

// checkSourceTreesOutOfBoundsSymlinks checks the tree a source renders
// from, as the repo-server does before it generates manifests.
// source.RepoRoot is the resolved root. PrepareSource already checked it
// and the $ref repositories of a path source, so this is a memo hit for a
// prepared source. A chart-only source renders from the chart it acquires,
// which renderChartOnlySource checks before its $ref repositories.
func (p localProvider) checkSourceTreesOutOfBoundsSymlinks(ctx context.Context, source render.ResolvedSource) error {
	if chartFlowSource(source) {
		return nil
	}
	return p.checkOutOfBoundsSymlinks(ctx, source.RepoRoot, sourceOutOfBoundsSymlinkScope(source))
}

// checkSpecHelmRefRootsOutOfBoundsSymlinks checks the $ref repositories of
// a path source as the repo-server does in runManifestGenAsync
// (reposerver/repository/repository.go:870-959 at v3.6.1): only for an
// Application with sources, from the spec's helm valueFiles and then
// fileParameters, before it reads .argocd-source.yaml (line 967). So an
// override that replaces the value files does not skip the check. A $ref
// to the source's own repository is checked with the source. An error
// that resolving a $ref would give is left to the render, which reports it.
func (p localProvider) checkSpecHelmRefRootsOutOfBoundsSymlinks(ctx context.Context, application argoappv1.Application, sourcePlan SourcePlan) error {
	helm := sourcePlan.Source.Helm
	if p.allowOutOfBoundsSymlinks || helm == nil || len(application.Spec.Sources) == 0 {
		return nil
	}
	candidates := append([]string(nil), helm.ValueFiles...)
	for _, parameter := range helm.FileParameters {
		candidates = append(candidates, parameter.Path)
	}
	plan, err := Plan(application)
	if err != nil {
		return nil
	}
	_, refSources, err := renderRefsForSource(plan, sourcePlan, candidates)
	if err != nil {
		return nil
	}
	checked := map[string]bool{}
	for _, candidate := range candidates {
		refKey, ok, err := helmValueFileRefKey(candidate)
		if err != nil || !ok || checked[refKey] {
			continue
		}
		checked[refKey] = true
		refSource, ok := refSources[refKey]
		if !ok {
			continue
		}
		roots, err := p.resolveRefRoots(ctx, map[string]render.ResolvedSource{refKey: refSource})
		if err != nil {
			return nil
		}
		if err := p.checkOutOfBoundsSymlinks(ctx, roots[refKey], render.OutOfBoundsSymlinkScopeRepository); err != nil {
			return fmt.Errorf("ref root %s: %w", refKey, err)
		}
	}
	return nil
}

// checkHelmRefRootsOutOfBoundsSymlinks checks each Git repository that a
// Helm $ref value file or file parameter of a chart-only source reads, in
// the order the repo-server does: value files first, then file parameters,
// each repository once (reposerver/repository/repository.go:870-959 at
// v3.6.1). The repo-server runs this after it checked the chart. A chart-only
// source reads no overrides, so its options hold the spec's value files.
func (p localProvider) checkHelmRefRootsOutOfBoundsSymlinks(ctx context.Context, opts render.RenderOptions) error {
	candidates := append([]string(nil), opts.ValueFiles...)
	for _, parameter := range opts.HelmFileParameters {
		candidates = append(candidates, parameter.Path)
	}
	checked := map[string]bool{}
	for _, candidate := range candidates {
		if !strings.HasPrefix(candidate, "$") {
			continue
		}
		refKey, _, _ := strings.Cut(candidate, "/")
		root, ok := opts.RefRoots[refKey]
		if !ok || checked[refKey] {
			continue
		}
		checked[refKey] = true
		if err := p.checkOutOfBoundsSymlinks(ctx, root, render.OutOfBoundsSymlinkScopeRepository); err != nil {
			return fmt.Errorf("ref root %s: %w", refKey, err)
		}
	}
	return nil
}

// checkRefOnlyOCISource acquires and checks an oci:// source that only sets
// ref. The repo-server skips a ref-only source only when it is not OCI and
// not Helm (reposerver/repository/repository.go:670 at v3.6.1), so it
// extracts such an artifact and checks it (429-444), and then generates
// nothing from it because its path and chart are empty (689-697).
func (p localProvider) checkRefOnlyOCISource(ctx context.Context, sourcePlan SourcePlan) error {
	// With the check off there is nothing to check and nothing renders, so
	// drydock does not acquire the artifact.
	if p.allowOutOfBoundsSymlinks {
		return nil
	}
	source := normalizeOCISource(render.ResolvedSource{
		Path:           sourcePlan.Source.Path,
		RepoURL:        sourcePlan.Source.RepoURL,
		TargetRevision: sourcePlan.Source.TargetRevision,
	})
	root, err := p.resolveSourceRoot(ctx, source)
	if err != nil {
		return err
	}
	return p.checkOutOfBoundsSymlinks(ctx, root, render.OutOfBoundsSymlinkScopeOCI)
}

// sourceOutOfBoundsSymlinkScope is the tree kind the repo-server checks
// for a source that renders from its resolved root.
func sourceOutOfBoundsSymlinkScope(source render.ResolvedSource) render.OutOfBoundsSymlinkScope {
	if ociartifact.IsOCIURL(source.RepoURL) {
		return render.OutOfBoundsSymlinkScopeOCI
	}
	return render.OutOfBoundsSymlinkScopeRepository
}

// rootOutOfBoundsSymlink is the out-of-bounds symlink verdict of the
// provider root, for the persistent render cache key of a source that
// renders from it: the first such symlink, or "" when there is none or the
// check is off. Every Application that renders from the root fails while
// it holds one, wherever it is, so the key must change when the verdict
// does, even when no digested input path changed.
func (p localProvider) rootOutOfBoundsSymlink(ctx context.Context) (string, error) {
	if p.allowOutOfBoundsSymlinks {
		return "", nil
	}
	return p.symlinkBounds.findOutOfBoundsSymlink(ctx, p.repoRoot, render.OutOfBoundsSymlinkScopeRepository)
}
