package app

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"sync"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/discovery"
	"github.com/sholdee/drydock/internal/gitref"
	"github.com/sholdee/drydock/internal/ociartifact"
	"github.com/sholdee/drydock/internal/project"
	"github.com/sholdee/drydock/internal/render"
)

// warnGitGeneratorProjects warns about each ApplicationSet whose Git
// generator looked up an AppProject that the repository does not declare
// and that none of its Applications names. The Argo CD v3.6.1 Git
// generator gets that AppProject from the controller namespace before it
// lists the repository, and fails when it is missing
// (applicationset/generators/git.go:93-111). The ApplicationSet then
// creates no Application at all, also from its other generators
// (applicationset/controllers/applicationset_controller.go:199-216).
//
// drydock cannot know the AppProjects of the cluster offline: they can come
// from another repository, a chart, a remote Kustomize base, or the cluster
// itself. So it keeps the Applications. When an Application names the same
// AppProject, its own project.missing warning says that the Git generator
// fails the ApplicationSet (project.GeneratedBy), so this warns only for
// the rest, once for each ApplicationSet. The default project always
// exists. When the repository declares no AppProject at all, project
// validation already reports that it cannot tell, and this warns nothing.
func warnGitGeneratorProjects(discovered discovery.Result, settings config.ArgoSettings, landing landingFunc) []diagnostic.Diagnostic {
	// The repository declares AppProjects also when --argocd-namespace
	// skips every one of them, because the controllers never find them.
	if len(discovered.Projects) == 0 {
		return nil
	}
	declared := appendDiscoveredProjects(nil, discovered, settings, landing)
	index := project.ByName(declared)
	type lookup struct {
		path    string
		name    string
		project string
		covered bool
	}
	missing := map[string]*lookup{}
	for _, appFile := range discovered.Applications {
		projectName := appFile.GitGeneratorProject
		if projectName == nil || *projectName == argoappv1.DefaultAppProjectName {
			continue
		}
		if _, ok := index[*projectName]; ok {
			continue
		}
		entry, ok := missing[appFile.ApplicationSetKey]
		if !ok {
			entry = &lookup{path: appFile.Path, name: applicationSetDisplayKey(appFile.ApplicationSetKey), project: *projectName}
			missing[appFile.ApplicationSetKey] = entry
		}
		if appFile.Application.Spec.Project == *projectName {
			entry.covered = true
		}
	}
	keys := make([]string, 0, len(missing))
	for key, entry := range missing {
		if !entry.covered {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	diags := make([]diagnostic.Diagnostic, 0, len(keys))
	for _, key := range keys {
		entry := missing[key]
		diag := diagnostic.Diagnostic{
			Severity:   diagnostic.SeverityWarning,
			Category:   "project",
			Message:    project.GitGeneratorMissingProjectMessage(entry.name, entry.project),
			Provenance: diagnostic.Provenance{Path: entry.path, Pointer: "spec.template.spec.project"},
		}
		diag.Code = diagnostic.StableCode(diag)
		diags = append(diags, diag)
	}
	return diags
}

// lazyWorktreeFilter opens the untracked-ignored filter of a working tree
// the first time a Git generator asks, so a run without one never runs git.
// A tree whose Git data cannot be read skips nothing. It also lists the
// gitlinks of the index.
type lazyWorktreeFilter struct {
	ctx          context.Context
	root         string
	once         sync.Once
	filter       *gitref.UntrackedIgnoredFilter
	gitlinksOnce sync.Once
	gitlinks     []string
}

// Gitlinks returns the gitlinks of the index, relative to the root.
func (f *lazyWorktreeFilter) Gitlinks() []string {
	f.gitlinksOnce.Do(func() {
		f.gitlinks = gitref.IndexGitlinks(f.ctx, f.root)
	})
	return f.gitlinks
}

// snapshotGitlinks gives the Git generator the gitlinks of a snapshot of
// a Git ref, which holds committed paths only, so it skips nothing.
type snapshotGitlinks struct {
	root string
	once sync.Once
	// links are the gitlinks the snapshot listed when listed is set.
	links  []string
	listed bool
}

func (s *snapshotGitlinks) Skip(string, bool) bool { return false }

// Gitlinks returns the gitlinks the snapshot listed. A snapshot that did
// not list them holds no submodule content, so they are its empty
// directories (gitref.SnapshotGitlinks).
func (s *snapshotGitlinks) Gitlinks() []string {
	s.once.Do(func() {
		if !s.listed {
			s.links = gitref.SnapshotGitlinks(s.root)
		}
	})
	return s.links
}

func newLazyWorktreeFilter(ctx context.Context, root string) *lazyWorktreeFilter {
	return &lazyWorktreeFilter{ctx: ctx, root: root}
}

// Skip reports whether Git ignores rel and does not track it.
func (f *lazyWorktreeFilter) Skip(rel string, isDir bool) bool {
	f.once.Do(func() {
		if filter, err := gitref.OpenUntrackedIgnoredFilter(f.ctx, f.root); err == nil {
			f.filter = filter
		}
	})
	return f.filter.Skip(rel, isDir)
}

// dotGitSourceCode identifies the warning for a source whose path is the
// .git directory.
const dotGitSourceCode = "render.git-directory-source"

// dotGitSourceDiagnostic reports a Git source whose path is the .git
// directory of the repository. A Git directories generator lists ".git"
// with reposerver.include.hidden.directories on, so an Application can get
// it as its path. The Argo CD repo-server renders the .git directory of its
// checkout as a directory source, and no file there has a manifest
// extension, so the source has no manifests. drydock never reads .git, so
// it renders the source empty and warns. An explicit source type is left
// to the renderer.
func dotGitSourceDiagnostic(source render.ResolvedSource) (diagnostic.Diagnostic, bool) {
	if ociartifact.IsOCIURL(source.RepoURL) || source.Chart != "" || source.ExplicitType != "" {
		return diagnostic.Diagnostic{}, false
	}
	// The name is exact: on Linux, where the repo-server runs, ".GIT" is
	// another path, which the checkout does not hold.
	if path.Clean(filepath.ToSlash(source.Path)) != ".git" {
		return diagnostic.Diagnostic{}, false
	}
	return diagnostic.Diagnostic{
		Code:     dotGitSourceCode,
		Severity: diagnostic.SeverityWarning,
		Category: "render",
		Message:  fmt.Sprintf("source path %q is the .git directory of the repository; Argo CD renders it as a directory source without manifests, and drydock renders it empty without reading .git", source.Path),
	}, true
}
