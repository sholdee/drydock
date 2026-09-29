package app

import (
	"context"
	"fmt"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/ociartifact"
	"github.com/sholdee/drydock/internal/render"
	sourcepkg "github.com/sholdee/drydock/internal/source"
)

// selectionSide is one diff side's changed-only ownership, built by
// withSelectionOnlyPaths.
type selectionSide struct {
	// inputs are copies of the listed inputs, each carrying its own
	// selection-only paths.
	inputs []ApplicationSelectionInput
	// renderedDirs maps each directory rendered to declare Applications
	// without a parent Application (RenderedDir) to its Kustomize graph.
	// The graph is kept once per directory, never copied into the Paths of
	// every Application the directory declares.
	renderedDirs map[string][]string
}

// withSelectionOnlyPaths returns copies of inputs whose Paths also carry
// the local inputs of every source that renders from root: its Kustomize
// graph (bases, components, helmCharts values files, patches, generator
// files, and spec.source.kustomize components and patches) and its Helm
// value files and file parameters, even outside spec.source.path. Every
// source also owns the $ref value files and file parameters whose ref root
// is this tree (selfRepo identifies the repository under diff).
// It exists for changed-only ownership only: ApplicationSelectionInput.Paths
// from discovery also key the persistent render cache, so the listed inputs
// are never mutated. applicationSourcePaths feeds the same key as required
// inputs, so Helm inputs that may be missing (ignoreMissingValueFiles, glob
// directories) are owned here, not there. A source whose graph cannot be
// read keeps its spec.source.path ownership.
//
// An Application rendered without a parent Application (RenderedDir:
// --discover-kustomize, plugin-policy bootstrap) is also selected when the
// Kustomize graph of the directory rendered to declare it changes: a change
// there can rewrite its spec. That graph is walked and kept once per
// directory in renderedDirs; selectChangedDiffSides owns its paths and
// selects every Application the directory declares. One rendered by a
// parent Application (ParentKey) owns only its own inputs here;
// selectChangedDiffSides selects it with its parent. Directories in Paths
// are never walked: a listed input is not a render root.
func withSelectionOnlyPaths(ctx context.Context, root string, repoMaps []sourcepkg.RepoMap, selfRepo selfRepoRefs, inputs []ApplicationSelectionInput) selectionSide {
	tree := newSelectionTree(root, repoMaps, selfRepo)
	memo := map[string][]string{}
	kustomizeGraph := func(source argoappv1.ApplicationSource) []string {
		key := kustomizeSelectionMemoKey(source)
		paths, ok := memo[key]
		if !ok {
			paths, _ = render.KustomizeSelectionPaths(ctx, root, source.Path, source.Kustomize)
			memo[key] = paths
		}
		return paths
	}
	out := make([]ApplicationSelectionInput, len(inputs))
	renderedDirs := map[string][]string{}
	for i, input := range inputs {
		out[i] = input
		out[i].Paths = append([]string(nil), input.Paths...)
		// Not behind the locality gate below: a $ref consumer is usually a
		// chart-repository source.
		out[i].Paths = append(out[i].Paths, helmRefSelectionPaths(tree, input.Application)...)
		for _, source := range applicationSources(input.Application) {
			if !pathSourceRendersFromRoot(tree, source) {
				continue
			}
			out[i].Paths = append(out[i].Paths, helmLocalSelectionPaths(source)...)
			if mayWalkKustomizeGraph(source) {
				out[i].Paths = append(out[i].Paths, kustomizeGraph(source)...)
			}
		}
		if input.RenderedDir != "" {
			renderedDirs[input.RenderedDir] = kustomizeGraph(argoappv1.ApplicationSource{Path: input.RenderedDir})
		}
	}
	return selectionSide{inputs: out, renderedDirs: renderedDirs}
}

// kustomizeSelectionMemoKey identifies one selection walk: sources sharing a
// path but adding different spec.source.kustomize components or patches own
// different inputs.
func kustomizeSelectionMemoKey(source argoappv1.ApplicationSource) string {
	var components, patches []string
	if source.Kustomize != nil {
		components = source.Kustomize.Components
		for _, patch := range source.Kustomize.Patches {
			patches = append(patches, patch.Path)
		}
	}
	return fmt.Sprintf("%q %q %q", source.Path, components, patches)
}

func applicationSources(app argoappv1.Application) argoappv1.ApplicationSources {
	if len(app.Spec.Sources) == 0 && app.Spec.Source != nil {
		return argoappv1.ApplicationSources{*app.Spec.Source}
	}
	return app.Spec.Sources
}

// selectionTree is the tree one withSelectionOnlyPaths call owns inputs in,
// with the facts that decide whether a source renders from it.
type selectionTree struct {
	root     string
	resolver *sourcepkg.Resolver
	selfRepo selfRepoMatcher
	// mappedToRoot memoizes sameLocalPath per mapped path: it resolves
	// symlinks, which dominates the cost of a repo-mapped fleet.
	mappedToRoot map[string]bool
}

func newSelectionTree(root string, repoMaps []sourcepkg.RepoMap, selfRepo selfRepoRefs) *selectionTree {
	return &selectionTree{
		root:         root,
		resolver:     sourcepkg.NewResolver(sourcepkg.Options{RepoMaps: repoMaps}),
		selfRepo:     newSelfRepoMatcher(selfRepo),
		mappedToRoot: map[string]bool{},
	}
}

// repoMapped reports whether repoURL is repo-mapped and, if so, whether it
// maps to root.
func (t *selectionTree) repoMapped(repoURL string) (toRoot, mapped bool) {
	mappedPath, mapped := t.resolver.MappedPath(repoURL)
	if !mapped {
		return false, false
	}
	toRoot, ok := t.mappedToRoot[mappedPath]
	if !ok {
		toRoot = sameLocalPath(mappedPath, t.root)
		t.mappedToRoot[mappedPath] = toRoot
	}
	return toRoot, true
}

// pathSourceRendersFromRoot mirrors localProvider.resolveSourceRootIdentity:
// a path source renders from the local tree when it is not repo-mapped
// elsewhere, not OCI, and its path exists under root — whatever its repoURL.
// Sources fetched from another repository never contribute local ownership,
// and a chart source's value files resolve inside the fetched chart.
func pathSourceRendersFromRoot(tree *selectionTree, source argoappv1.ApplicationSource) bool {
	if source.Path == "" || source.Chart != "" {
		return false
	}
	if toRoot, mapped := tree.repoMapped(source.RepoURL); mapped && !toRoot {
		return false
	}
	if ociartifact.IsOCIURL(source.RepoURL) {
		return false
	}
	exists, err := sourcePathExists(tree.root, source.Path)
	return err == nil && exists
}

// refSourceRendersFromRoot mirrors localProvider.resolveSourceRootIdentity
// for the source renderRefsForSource hands the provider as a $ref root: it
// reports whether that root is this tree, reached without acquisition. A
// repo-mapped source resolves to its mapped path and an OCI source is never
// local; otherwise a path present under root, a self-repo reference, or an
// empty repoURL resolves to root. Unlike pathSourceRendersFromRoot it needs
// no path. One documented divergence: a chart-only source reports false
// although the provider returns root for it. Argo CD cannot use a chart
// repository as a $ref root, and a ref source cannot set chart, so only a
// ref twin of a chart-only consumer (same repoURL and revision) reaches it.
// The provider's nil-resolver branch serves hand-built providers only.
func refSourceRendersFromRoot(tree *selectionTree, source render.ResolvedSource) bool {
	if source.Path == "" && source.Chart != "" {
		return false
	}
	if toRoot, mapped := tree.repoMapped(source.RepoURL); mapped {
		return toRoot
	}
	if ociartifact.IsOCIURL(source.RepoURL) {
		return false
	}
	if source.Path != "" {
		exists, err := sourcePathExists(tree.root, source.Path)
		if err != nil {
			return false
		}
		if exists {
			return true
		}
	}
	return tree.selfRepo.matches(source.RepoURL, source.TargetRevision) || strings.TrimSpace(source.RepoURL) == ""
}

// mayWalkKustomizeGraph reports whether a local source may render a
// Kustomize graph. Explicit Helm and Directory sources are skipped. Implicit
// and plugin sources are detected by KustomizeSelectionPaths finding (or not)
// a kustomization file: AVP and native Kustomize plugin compatibility render
// a plugin path with Kustomize, and a Kustomize-based CMP reads the same
// graph. Leaving a plugin out would let a sibling's graph own the shared base
// alone and drop the plugin Application from the diff.
func mayWalkKustomizeGraph(source argoappv1.ApplicationSource) bool {
	explicitType, err := source.ExplicitType()
	return err == nil && (explicitType == nil || *explicitType == argoappv1.ApplicationSourceTypeKustomize || *explicitType == argoappv1.ApplicationSourceTypePlugin)
}
