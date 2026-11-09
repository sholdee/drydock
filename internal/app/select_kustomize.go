package app

import (
	"context"
	"fmt"
	"slices"
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
	// unscanned holds the Applications whose Jsonnet import scan ran out of
	// budget or failed. selectChangedDiffSides selects them whenever a path
	// changed.
	unscanned []argoappv1.Application
}

// withSelectionOnlyPaths returns copies of inputs whose Paths also carry
// the local inputs of every source that renders from root: its Kustomize
// graph (bases, components, helmCharts values files, patches, generator
// files, and spec.source.kustomize components and patches), its Helm
// value files and file parameters, the paths that the symlinks under its
// path and Jsonnet libraries reach, and the files that its Jsonnet files
// import, even outside spec.source.path. Every
// source also owns the $ref value files and file parameters whose ref root
// is this tree (selfRepo identifies the repository under diff). Each
// source is read with its .argocd-source*.yaml files merged in, as the
// render reads it (selectionSourceWithOverrides).
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
//
// It reads the override files with the default controller namespace;
// withSelectionOnlyPathsIn takes the namespace from the settings.
func withSelectionOnlyPaths(ctx context.Context, root string, repoMaps []sourcepkg.RepoMap, selfRepo selfRepoRefs, gates sourceTypeGates, inputs []ApplicationSelectionInput) selectionSide {
	return withSelectionOnlyPathsIn(ctx, root, repoMaps, selfRepo, gates, defaultArgoCDControllerNamespace, inputs)
}

// withSelectionOnlyPathsIn is withSelectionOnlyPaths for the Argo CD
// controller namespace controllerNamespace, which decides the instance name
// in the .argocd-source-<app>.yaml file name, as in the render.
func withSelectionOnlyPathsIn(ctx context.Context, root string, repoMaps []sourcepkg.RepoMap, selfRepo selfRepoRefs, gates sourceTypeGates, controllerNamespace string, inputs []ApplicationSelectionInput) selectionSide {
	tree := newSelectionTree(root, repoMaps, selfRepo)
	tree.controllerNamespace = controllerNamespace
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
	symlinkMemo := map[string]symlinkSelection{}
	symlinkInputs := func(application argoappv1.Application, source argoappv1.ApplicationSource) symlinkSelection {
		jsonnet := directoryJsonnetSelectionFor(application, source, tree.controllerNamespace)
		key := jsonnet.memoKey(source.Path)
		selection, ok := symlinkMemo[key]
		if !ok {
			selection = directorySelectionInputs(ctx, root, source.Path, jsonnet, gates)
			symlinkMemo[key] = selection
		}
		return selection
	}
	var unscanned []argoappv1.Application
	out := make([]ApplicationSelectionInput, len(inputs))
	renderedDirs := map[string][]string{}
	for i, input := range inputs {
		out[i] = input
		out[i].Paths = append([]string(nil), input.Paths...)
		// Not behind the locality gate below: a $ref consumer is usually a
		// chart-repository source.
		out[i].Paths = append(out[i].Paths, helmRefSelectionPaths(tree, input.Application)...)
		for _, specSource := range applicationSources(input.Application) {
			if !sourceRendersFromRoot(tree, input.Application, specSource) {
				continue
			}
			if input.Application.Spec.SourceHydrator != nil {
				// applicationSourcePaths leaves the sync path out, because
				// the render reads it from the local tree only here.
				out[i].Paths = append(out[i].Paths, normalizeSelectPath(specSource.Path))
			}
			// The render reads the source after its override files.
			source := selectionSourceWithOverrides(tree, input.Application, specSource)
			out[i].Paths = append(out[i].Paths, helmLocalSelectionPaths(root, tree.helmGlobs, source)...)
			// A directory source reads the files that its symlinks reach
			// and its Jsonnet files import, also outside its path. The
			// Kustomize renderer and Helm charts and value files reject
			// symlinks, and neither evaluates Jsonnet. Helm file parameters
			// own their symlinks above.
			if rendersAsDirectory(root, source, gates) {
				selection := symlinkInputs(input.Application, source)
				out[i].Paths = append(out[i].Paths, selection.paths...)
				if selection.unknown {
					// The inputs are not known, so selectChangedDiffSides
					// selects the Application on any change. Its Paths stay
					// exact, so the other changed paths still count as
					// unowned when no Application owns them.
					unscanned = append(unscanned, input.Application)
				}
			}
			if mayWalkKustomizeGraph(source) {
				out[i].Paths = append(out[i].Paths, kustomizeGraph(source)...)
			}
		}
		if input.RenderedDir != "" {
			renderedDirs[input.RenderedDir] = kustomizeGraph(argoappv1.ApplicationSource{Path: input.RenderedDir})
		}
	}
	return selectionSide{inputs: out, renderedDirs: renderedDirs, unscanned: unscanned}
}

// symlinkSelection is what a directory source reads outside its path, for
// changed-only selection.
type symlinkSelection struct {
	paths []string
	// unknown reports a Jsonnet import scan that ran out of budget or
	// failed, or extVar or TLA code whose import paths read the revision:
	// the source's inputs are not known.
	unknown bool
}

// directoryJsonnetSelection is what decides the Jsonnet inputs of a
// directory source for changed-only selection.
type directoryJsonnetSelection struct {
	jsonnet argoappv1.ApplicationSourceJsonnet
	// env is the build environment of the render (argoRenderEnv). It holds
	// the spec revision, not the revision that the render resolves.
	env argoappv1.Env
}

// revisionProbes are two revisions that differ in every byte, also in the
// two short forms, for codeImportsReadRevision.
var revisionProbes = [2]string{
	"0000000000000000000000000000000000000000",
	"1111111111111111111111111111111111111111",
}

// directoryJsonnetSelectionFor returns the Jsonnet inputs of source, a
// source of application, as the render gets them: its Jsonnet spec and the
// build environment of the render (argoRenderEnv) in controllerNamespace.
func directoryJsonnetSelectionFor(application argoappv1.Application, source argoappv1.ApplicationSource, controllerNamespace string) directoryJsonnetSelection {
	if source.Directory == nil {
		return directoryJsonnetSelection{}
	}
	if controllerNamespace == "" {
		controllerNamespace = defaultArgoCDControllerNamespace
	}
	return directoryJsonnetSelection{
		jsonnet: source.Directory.Jsonnet,
		env:     argoRenderEnv(application, source, controllerNamespace),
	}
}

// substitutes reports whether env substitution can change a code value.
// Envsubst changes only a value with a "$".
func (s directoryJsonnetSelection) substitutes() bool {
	for _, value := range append(append([]argoappv1.JsonnetVar(nil), s.jsonnet.TLAs...), s.jsonnet.ExtVars...) {
		if value.Code && strings.Contains(value.Value, "$") {
			return true
		}
	}
	return false
}

// memoKey identifies one selection scan of the source at sourcePath. The
// build environment counts only when it can change a code value.
func (s directoryJsonnetSelection) memoKey(sourcePath string) string {
	var code, env []string
	for _, value := range append(append([]argoappv1.JsonnetVar(nil), s.jsonnet.TLAs...), s.jsonnet.ExtVars...) {
		if value.Code {
			code = append(code, value.Value)
		}
	}
	if s.substitutes() {
		for _, entry := range s.env {
			if entry != nil {
				env = append(env, entry.Name+"="+entry.Value)
			}
		}
	}
	return fmt.Sprintf("%q %q %q %q", sourcePath, s.jsonnet.Libs, code, env)
}

// codeImportsReadRevision reports whether an import path in the extVar or
// TLA code of the source at sourcePath names an ARGOCD_APP_REVISION
// variable, in any env substitution pass (render.JsonnetCodeValuePasses).
// Selection does not know the revision of the render, so it does not know
// those imports. It substitutes the code with two different revisions, and
// the imports read the revision when their paths differ. A revision in any
// other place of the code does not change its imports. A source without a
// Jsonnet file never evaluates the code, so it has no passes.
func (s directoryJsonnetSelection) codeImportsReadRevision(root, sourcePath string) bool {
	if !s.substitutes() {
		return false
	}
	passes, err := render.DirectoryJsonnetFileCount(root, sourcePath)
	if err != nil {
		return true
	}
	first := render.JsonnetCodeValuePasses(s.jsonnet, withArgoAppRevision(s.env, revisionProbes[0]), passes)
	second := render.JsonnetCodeValuePasses(s.jsonnet, withArgoAppRevision(s.env, revisionProbes[1]), passes)
	for index := range max(len(first), len(second)) {
		if codeImportsDiffer(codePassAt(first, index), codePassAt(second, index)) {
			return true
		}
	}
	return false
}

// codePassAt returns pass index of passes. render.JsonnetCodeValuePasses
// stops at the first pass that repeats, so a missing pass is the last one.
func codePassAt(passes [][]string, index int) []string {
	return passes[min(index, len(passes)-1)]
}

// codeImportsDiffer reports whether two substitutions of the same code
// values import different paths.
func codeImportsDiffer(first, second []string) bool {
	for index := range first {
		if first[index] == second[index] {
			continue
		}
		if !slices.Equal(render.JsonnetCodeImportLiterals(first[index]), render.JsonnetCodeImportLiterals(second[index])) {
			return true
		}
	}
	return false
}

// directorySelectionInputs returns the paths that a directory source at
// sourcePath reads through symlinks, Jsonnet libraries, and Jsonnet
// imports, also the imports in extVar and TLA code. With Jsonnet off in
// argocd-cm, no library or import is read, so only the symlinks under the
// source path count.
func directorySelectionInputs(ctx context.Context, root, sourcePath string, jsonnet directoryJsonnetSelection, gates sourceTypeGates) symlinkSelection {
	var selection symlinkSelection
	if gates.jsonnetDisabled {
		selection.paths = render.DirectorySymlinkInputPaths(root, []string{sourcePath})
		return selection
	}
	// The symlinks under the source path and under each library that
	// resolves, as the renderer resolves it, and the library paths and
	// symlinks that decide where a library is.
	libDirs, libLinks, libErr := render.DirectoryJsonnetLibInputs(root, sourcePath, jsonnet.jsonnet.Libs)
	dirs := []string{sourcePath}
	for _, dir := range libDirs {
		// A library at the repository root would cover every path, so it
		// adds no tree to walk; the import scan still owns the files it
		// finds there.
		if dir != "." {
			dirs = append(dirs, dir)
		}
	}
	selection.paths = render.DirectorySymlinkInputPaths(root, dirs)
	selection.paths = append(selection.paths, libLinks...)
	// A scan that ran out of budget or failed, such as on an unreadable
	// directory, leaves the imports unknown. So does code whose import
	// paths read the revision.
	imports, err := render.DirectoryJsonnetImportPaths(ctx, root, sourcePath, jsonnet.jsonnet, jsonnet.env)
	selection.unknown = err != nil || libErr != nil || jsonnet.codeImportsReadRevision(root, sourcePath)
	selection.paths = append(selection.paths, imports...)
	return selection
}

// rendersAsDirectory reports whether the local renderer renders source as a
// directory source, as selectLocalRenderer decides for the render with the
// same source type gates. An explicit plugin source is not one.
func rendersAsDirectory(root string, source argoappv1.ApplicationSource, gates sourceTypeGates) bool {
	explicitType, err := source.ExplicitType()
	if err != nil {
		return false
	}
	resolved := render.ResolvedSource{RepoRoot: root, Path: source.Path}
	if explicitType != nil {
		if *explicitType == argoappv1.ApplicationSourceTypePlugin {
			return false
		}
		resolved.ExplicitType = *explicitType
	}
	renderer, err := selectLocalRenderer(resolved, gates)
	if err != nil {
		return false
	}
	_, ok := renderer.(render.DirectoryRenderer)
	return ok
}

// kustomizeSelectionMemoKey identifies one selection walk: sources sharing a
// path but adding different spec.source.kustomize components or patches, or
// resolving components under a different ignoreMissingComponents, own
// different inputs.
func kustomizeSelectionMemoKey(source argoappv1.ApplicationSource) string {
	var components, patches []string
	ignoreMissing := false
	if source.Kustomize != nil {
		components = source.Kustomize.Components
		ignoreMissing = source.Kustomize.IgnoreMissingComponents
		for _, patch := range source.Kustomize.Patches {
			patches = append(patches, patch.Path)
		}
	}
	return fmt.Sprintf("%q %q %q %t", source.Path, components, patches, ignoreMissing)
}

func applicationSources(app argoappv1.Application) argoappv1.ApplicationSources {
	return applicationRenderSources(app.Spec)
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
	// helmGlobs memoizes the paths that each Helm value file glob owns on
	// this tree.
	helmGlobs helmGlobMemo
	// controllerNamespace is the Argo CD controller namespace, which
	// decides the instance name in the .argocd-source-<app>.yaml file name.
	controllerNamespace string
}

func newSelectionTree(root string, repoMaps []sourcepkg.RepoMap, selfRepo selfRepoRefs) *selectionTree {
	return &selectionTree{
		root:         root,
		resolver:     sourcepkg.NewResolver(sourcepkg.Options{RepoMaps: repoMaps}),
		selfRepo:     newSelfRepoMatcher(selfRepo),
		mappedToRoot: map[string]bool{},
		helmGlobs:    helmGlobMemo{},
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

// pathSourceRendersFromRoot mirrors localProvider.resolveSourceRootIdentity
// for a source that is not a source hydrator sync source (see
// hydratorSyncRendersFromRoot): a path source renders from the local tree
// when it is not repo-mapped elsewhere, not OCI, and its path exists under
// root — whatever its repoURL.
// Sources fetched from another repository never contribute local ownership,
// an oci:// source is never local whether or not it sets chart (Argo CD
// ignores chart there and the path selects within the artifact), and a
// schemeless-OCI or HTTP(S) chart source's value files resolve inside the
// fetched chart.
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

// sourceRendersFromRoot reports whether the render reads source from the
// local tree: hydratorSyncRendersFromRoot for the sync source of an
// Application with spec.sourceHydrator, pathSourceRendersFromRoot for any
// other source.
func sourceRendersFromRoot(tree *selectionTree, application argoappv1.Application, source argoappv1.ApplicationSource) bool {
	if application.Spec.SourceHydrator != nil {
		return hydratorSyncRendersFromRoot(tree, source)
	}
	return pathSourceRendersFromRoot(tree, source)
}

// hydratorSyncRendersFromRoot mirrors localProvider.resolveHydratorSyncSource:
// a sync source renders from the local tree only when it is the repository
// under analysis, by its remote or by a --repo-map to root, at a branch that
// the self-repo rule tracks (selfRepoMatcher.matches and tracksRevision). A path that exists locally is not enough,
// because the dry and the sync source often share a path.
func hydratorSyncRendersFromRoot(tree *selectionTree, source argoappv1.ApplicationSource) bool {
	branch := strings.TrimSpace(source.TargetRevision)
	if source.Path == "" || branch == "" {
		return false
	}
	if toRoot, mapped := tree.repoMapped(source.RepoURL); mapped {
		// A map to this tree names the repository under analysis
		// (localProvider.hydratorSelfMapped); a map elsewhere never renders
		// from this tree.
		return toRoot && tree.selfRepo.tracksRevision(branch)
	}
	return tree.selfRepo.matches(source.RepoURL, branch)
}

// refSourceRendersFromRoot mirrors localProvider.resolveSourceRootIdentity
// for the source renderRefsForSource hands the provider as a $ref root: it
// reports whether that root is this tree, reached without acquisition. A
// repo-mapped source resolves to its mapped path and an OCI source is never
// local (chart or no chart — the provider classifies every oci:// spelling
// as an artifact source); otherwise a path present under root, a self-repo
// reference, or an empty repoURL resolves to root. Unlike
// pathSourceRendersFromRoot it needs no path. One documented divergence: a
// schemeless-OCI or HTTP(S) chart-only source reports false although the
// provider returns root for it. Argo CD cannot use a chart repository as a
// $ref root, and a non-OCI ref source cannot set chart, so only a ref twin
// of a chart-only consumer (same repoURL and revision) reaches it. The
// provider's nil-resolver branch serves hand-built providers only.
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
