package render

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/filecopy"
	"github.com/sholdee/drydock/internal/pathsafety"
	"sigs.k8s.io/kustomize/api/types"
)

func copyAcquiredRemoteKustomizeResource(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("source path %q is a symlink", src)
	}
	if info.IsDir() {
		return copyRegularTree(src, dst)
	}
	if info.Mode().IsRegular() {
		return copyRegularFile(src, dst)
	}
	return fmt.Errorf("source path %q is not a regular file or directory", src)
}

func copyRegularTree(srcRoot, dstRoot string) error {
	return copyTree(srcRoot, dstRoot, copyTreeOptions{})
}

type preparedKustomizeWorkspaceCopier struct {
	repoRoot   string
	dstRoot    string
	copiedDirs []string
	copied     map[string]struct{}
	forceCopy  workspaceCopyFiles
}

func copyPreparedKustomizeWorkspaceTree(ctx context.Context, repoRoot, sourceRoot, dstRoot string, graph []kustomizeGraphNode, opts RenderOptions) error {
	sourceOptionGraph, sourceOptionPaths, _, err := sourceKustomizeWorkspaceAdditions(ctx, repoRoot, sourceRoot, opts)
	if err != nil {
		return err
	}
	graph = append(append([]kustomizeGraphNode(nil), graph...), sourceOptionGraph...)
	forceCopy, err := mutableKustomizationFiles(sourceRoot, graph, sourceOptionPaths)
	if err != nil {
		return err
	}
	copier := &preparedKustomizeWorkspaceCopier{
		repoRoot:  filepath.Clean(repoRoot),
		dstRoot:   filepath.Clean(dstRoot),
		copied:    map[string]struct{}{},
		forceCopy: forceCopy,
	}
	if err := copier.copyPath(sourceRoot); err != nil {
		return err
	}
	for _, node := range graph {
		if err := copier.copyPath(node.Dir); err != nil {
			return err
		}
		for _, path := range referencedKustomizeWorkspacePaths(node.Dir, node.Kustomization) {
			if err := copier.copyPath(path); err != nil {
				return err
			}
		}
		for _, path := range node.PluginInputPaths {
			if err := copier.copyPath(path); err != nil {
				return err
			}
		}
	}
	for _, path := range sourceOptionPaths {
		if err := copier.copyPath(path); err != nil {
			return err
		}
	}
	return nil
}

func (c *preparedKustomizeWorkspaceCopier) copyPath(path string) error {
	path = filepath.Clean(path)
	rel, err := filepath.Rel(c.repoRoot, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("copy source path %q escapes source root %q", path, c.repoRoot)
	}
	// Render input never lives in a .git directory; copying one would put
	// repository metadata (credentials included) where kustomize can read it.
	if pathsafety.RelEntersGit(rel) {
		return fmt.Errorf("copy source path %q enters a .git directory", path)
	}
	if c.pathCovered(path) {
		return nil
	}
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	dstPath := filepath.Clean(filepath.Join(c.dstRoot, rel))
	dstRel, err := filepath.Rel(c.dstRoot, dstPath)
	if err != nil || dstRel == ".." || strings.HasPrefix(dstRel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("copy destination path %q escapes destination root %q", dstPath, c.dstRoot)
	}
	if info.IsDir() {
		if err := copyWorkspaceTreeWithForceCopy(path, dstPath, c.forceCopy); err != nil {
			return err
		}
		c.copiedDirs = append(c.copiedDirs, path)
		return nil
	}
	if _, ok := c.copied[path]; ok {
		return nil
	}
	if err := copyWorkspaceFile(path, dstPath, info, c.forceCopy); err != nil {
		return err
	}
	c.copied[path] = struct{}{}
	return nil
}

// workspaceCopyFiles holds the repository files that a prepared workspace
// must copy and never hard-link: the kustomization files that
// prepareKustomizationDir rewrites. It matches a file by the file on disk
// (os.SameFile), not by its path text. The graph walk names a kustomization
// file by the name it looked up, under the directory name written in the
// reference, while the tree copy walks the names on disk. On a
// case-insensitive file system the two differ for one file, for example
// base/kustomization.yaml and base/Kustomization.yaml, or BASE and base.
type workspaceCopyFiles struct {
	files []os.FileInfo
}

func (f *workspaceCopyFiles) add(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	f.files = append(f.files, info)
	return nil
}

// contains reports whether info, the Lstat of a repository file, is one
// of the files f holds.
func (f workspaceCopyFiles) contains(info os.FileInfo) bool {
	return slices.ContainsFunc(f.files, func(file os.FileInfo) bool {
		return os.SameFile(file, info)
	})
}

func mutableKustomizationFiles(sourceRoot string, graph []kustomizeGraphNode, extraPaths []string) (workspaceCopyFiles, error) {
	var out workspaceCopyFiles
	for _, dir := range append([]string{sourceRoot}, kustomizeGraphDirs(graph)...) {
		file, err := findKustomizationFile(dir)
		if err != nil {
			return workspaceCopyFiles{}, err
		}
		if err := out.add(file); err != nil {
			return workspaceCopyFiles{}, err
		}
	}
	for _, path := range extraPaths {
		info, err := os.Lstat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return workspaceCopyFiles{}, err
		}
		if !info.IsDir() {
			continue
		}
		file, err := findKustomizationFile(path)
		if err != nil {
			continue
		}
		if err := out.add(file); err != nil {
			return workspaceCopyFiles{}, err
		}
	}
	return out, nil
}

func kustomizeGraphDirs(graph []kustomizeGraphNode) []string {
	out := make([]string, 0, len(graph))
	for _, node := range graph {
		out = append(out, node.Dir)
	}
	return out
}

func (c *preparedKustomizeWorkspaceCopier) pathCovered(path string) bool {
	for _, dir := range c.copiedDirs {
		rel, err := filepath.Rel(dir, path)
		if err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))) {
			return true
		}
	}
	return false
}

//nolint:gocyclo // Kustomize local-reference collection mirrors the schema fields that can point at files.
func referencedKustomizeWorkspacePaths(dir string, kustomization types.Kustomization) []string {
	refs := make([]string, 0)
	// Refs are copied exactly as written, the name kustomize (or Helm, for
	// value files) reads: "cm.yaml " is not "cm.yaml".
	appendLocalRef := func(ref string) {
		if ref == "" || isRemoteKustomizeRef(ref) || filepath.IsAbs(ref) {
			return
		}
		refs = append(refs, filepath.Clean(filepath.Join(dir, filepath.FromSlash(ref))))
	}

	if len(kustomization.HelmCharts) != 0 {
		appendLocalRef(kustomizationChartHome(kustomization))
	}
	for _, helmChart := range kustomization.HelmCharts {
		appendLocalRef(helmChart.ValuesFile)
		for _, valuesFile := range helmChart.AdditionalValuesFiles {
			appendLocalRef(valuesFile)
		}
	}
	for _, ref := range kustomization.Resources {
		appendLocalRef(ref)
	}
	for _, ref := range kustomization.Components {
		appendLocalRef(ref)
	}
	for _, ref := range kustomization.Crds {
		appendLocalRef(ref)
	}
	appendLocalRef(kustomization.OpenAPI["path"])
	for _, ref := range kustomization.Configurations {
		appendLocalRef(ref)
	}
	for _, ref := range kustomization.Generators {
		// Inline generator entries are YAML documents, not paths — but their
		// KSOPS files: referents (resolved from the kustomization dir, matching
		// emulation/digest/validation) still need copying into the temp tree.
		if isInlineKustomizeGeneratorEntry(ref) {
			if docs, ok := inlineKustomizeGeneratorDocuments(ref); ok {
				for _, fileRef := range ksopsGeneratorFileRefsFromDocuments(docs) {
					appendLocalRef(fileRef)
				}
			}
			continue
		}
		appendLocalRef(ref)
		// KSOPS files: referents resolve relative to the generator manifest
		// (which may sit outside every copied directory); copy them so
		// ksops-compat emulation can read them from the temp tree.
		refs = append(refs, ksopsGeneratorFilePaths(dir, ref)...)
	}
	for _, ref := range kustomization.Transformers {
		appendLocalRef(ref)
	}
	for _, ref := range kustomization.Validators {
		appendLocalRef(ref)
	}
	for _, replacement := range kustomization.Replacements {
		appendLocalRef(replacement.Path)
	}
	for _, patch := range kustomization.Patches {
		appendLocalRef(patch.Path)
	}

	for _, patch := range kustomization.PatchesJson6902 { //nolint:staticcheck // Kustomize still accepts patchesJson6902; copy local refs for parity.
		appendLocalRef(patch.Path)
	}

	for _, patch := range kustomization.PatchesStrategicMerge { //nolint:staticcheck // Kustomize still accepts patchesStrategicMerge; copy local refs for parity.
		ref := string(patch)
		if !isInlineStrategicMergePatch(ref) {
			appendLocalRef(ref)
		}
	}
	for _, generator := range kustomization.ConfigMapGenerator {
		appendGeneratorWorkspaceRefs(appendLocalRef, generator.KvPairSources)
	}
	for _, generator := range kustomization.SecretGenerator {
		appendGeneratorWorkspaceRefs(appendLocalRef, generator.KvPairSources)
	}
	return refs
}

// sourceKustomizeWorkspaceAdditions returns the graph of the
// spec.source.kustomize components rendering adds, the local component
// directories and patch files they read, and optional render inputs that are
// not copied into the workspace. Under ignoreMissingComponents only the
// first valid component is added (see sourceKustomizeComponents). The
// optional inputs are then the in-repository directories of the components
// skipped before it (a kustomization file appearing there changes the
// render) and the kustomization file names of each component after it (they
// decide the dropped-component warning). A component after the first valid
// one is never walked, checked or copied, as the repo-server never reads it.
func sourceKustomizeWorkspaceAdditions(ctx context.Context, repoRoot, sourceRoot string, opts RenderOptions) ([]kustomizeGraphNode, []string, []string, error) {
	if opts.Kustomize == nil {
		return nil, nil, nil, nil
	}
	refs := make([]string, 0, len(opts.Kustomize.Components)+len(opts.Kustomize.Patches))
	graph := make([]kustomizeGraphNode, 0)
	appendLocalRef := func(ref string) {
		if ref == "" || isRemoteKustomizeRef(ref) || filepath.IsAbs(ref) {
			return
		}
		refs = append(refs, filepath.Clean(filepath.Join(sourceRoot, filepath.FromSlash(ref))))
	}
	components, optionalRefs, err := sourceKustomizeAddedComponents(repoRoot, sourceRoot, opts.Kustomize)
	if err != nil {
		return nil, nil, nil, err
	}
	for _, component := range components {
		appendLocalRef(component)
		componentPath := filepath.Clean(filepath.Join(sourceRoot, filepath.FromSlash(component)))
		if isRemoteKustomizeRef(component) || filepath.IsAbs(component) {
			continue
		}
		// Check the path before the graph walk reads any file under it, so
		// a symlink to .git never gets read.
		if err := rejectSourceKustomizeComponentPath(repoRoot, component, componentPath); err != nil {
			return nil, nil, nil, err
		}
		if _, err := os.Lstat(componentPath); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, nil, nil, err
		}
		_, componentGraph, err := collectKustomizeGraph(ctx, repoRoot, componentPath, kustomizeInputWalk)
		if err != nil {
			return nil, nil, nil, err
		}
		graph = append(graph, componentGraph...)
	}
	for _, patch := range opts.Kustomize.Patches {
		appendLocalRef(patch.Path)
	}
	return graph, refs, optionalRefs, nil
}

// sourceKustomizeAddedComponents returns the spec.source.kustomize
// components that rendering adds and the optional render inputs that decide
// them. Under ignoreMissingComponents the optional inputs are the
// in-repository directories skipped before the first valid component, the
// kustomization file names of the kept component, which decide that it is
// kept, and the kustomization file names of each component after it. The
// kept folder itself is not always a glob match, for example when its name
// holds glob characters, so its own files are inputs. The components
// then go through the globs of `kustomize edit add component`
// (sourceKustomizeComponentMatches).
//
// The directories that the globs list are not inputs. Each match is a
// component, whose files are inputs, and the render cache key records the
// path of each input. So a new, removed, or renamed match changes the set
// of inputs and the key, and an entry that does not match changes neither
// the render nor the key. The order of the matches follows from their
// names. Owning the listed directory as a whole tree would digest every
// file under it, and a symlink anywhere under it would turn the cache off.
func sourceKustomizeAddedComponents(repoRoot, sourceRoot string, kustomize *argoappv1.ApplicationSourceKustomize) ([]string, []string, error) {
	components := kustomize.Components
	var optionalRefs []string
	if kustomize.IgnoreMissingComponents {
		selection, err := selectIgnoreMissingComponents(sourceRoot, repoRoot, kustomize.Components)
		if err != nil {
			return nil, nil, err
		}
		for _, skipped := range selection.skipped {
			optionalRefs = appendNonEmpty(optionalRefs, skipped.probe)
		}
		optionalRefs = append(optionalRefs, laterIgnoreMissingComponentInputs(sourceRoot, repoRoot, selection.later)...)
		if !selection.found {
			return nil, optionalRefs, nil
		}
		optionalRefs = append(optionalRefs, keptIgnoreMissingComponentInputs(repoRoot, selection.keptRel)...)
		components = []string{selection.kept}
	}
	matches, _, err := sourceKustomizeComponentMatches(repoRoot, sourceRoot, components)
	if err != nil {
		return nil, nil, err
	}
	return matches, optionalRefs, nil
}

// sourceKustomizeComponentMatches returns the components that `kustomize
// edit add component` reads from the patterns in components, without the
// source kustomization file itself, and the directories that the glob
// patterns list (kustomizeComponentGlobDir), which changed-only selection
// owns. When the arguments or the globs fail the command, the patterns come
// back as written: the render then fails in applySourceKustomizeOptions.
// A glob that must read through a symlink fails here with the symlink
// error, so no later step looks at the path the symlink leads to.
func sourceKustomizeComponentMatches(repoRoot, sourceRoot string, components []string) (matches, globDirs []string, err error) {
	patterns := sourceKustomizeComponentArgs(components)
	matches, err = expandKustomizeComponentPatternsOrWritten(sourceRoot, repoRoot, patterns)
	if err != nil {
		return nil, nil, err
	}
	if kustomizationFile, err := findKustomizationFile(sourceRoot); err == nil {
		name := filepath.Base(kustomizationFile)
		matches = slices.DeleteFunc(matches, func(match string) bool { return match == name })
	}
	for _, pattern := range patterns {
		if globDir, ok := kustomizeComponentGlobDir(sourceRoot, pattern); ok {
			globDirs = append(globDirs, globDir)
		}
	}
	return matches, globDirs, nil
}

// sourceKustomizeComponentArgs returns the component patterns that `kustomize
// edit add component` reads from components after its flags, or components
// as written when the flags fail the command.
func sourceKustomizeComponentArgs(components []string) []string {
	line, err := parseKustomizeEditArgs("add component", components, kustomizeEditCommonFlags)
	switch {
	case err != nil:
		return components
	case line.help:
		return nil
	}
	return line.args
}

// expandKustomizeComponentPatternsOrWritten returns the matches of
// patterns, or patterns as written when the globs fail for any reason but
// a symlink. The symlink error comes back as is.
func expandKustomizeComponentPatternsOrWritten(sourceRoot, repoRoot string, patterns []string) ([]string, error) {
	matches, err := expandKustomizeComponentPatterns(sourceRoot, repoRoot, patterns)
	var symlinkErr kustomizeComponentSymlinkError
	switch {
	case err == nil:
		return matches, nil
	case errors.As(err, &symlinkErr):
		return nil, err
	default:
		return patterns, nil
	}
}

// keptIgnoreMissingComponentInputs returns the kustomization file names in
// the kept ignoreMissingComponents folder, keptRel under repoRoot.
func keptIgnoreMissingComponentInputs(repoRoot, keptRel string) []string {
	if keptRel == "" || pathsafety.RelEntersGit(keptRel) {
		return nil
	}
	inputs := make([]string, 0, len(kustomizationFileNames))
	for _, name := range kustomizationFileNames {
		inputs = append(inputs, filepath.Join(repoRoot, keptRel, name))
	}
	return inputs
}

// rejectSourceKustomizeComponentPath fails a spec.source.kustomize
// component path that leaves the repository, enters .git, or passes
// through a symlink. drydock rejects such a component in the render
// anyway, and this check runs before any file under it is read.
func rejectSourceKustomizeComponentPath(repoRoot, component, componentPath string) error {
	if err := rejectPathOutsideBoundary("kustomize components", componentPath, repoRoot); err != nil {
		return err
	}
	if pathEntersGit(repoRoot, componentPath) {
		return gitPathRefError("kustomize components", component)
	}
	if err := rejectSymlinkedPath(repoRoot, componentPath); err != nil {
		return fmt.Errorf("kustomize components %q: %w", component, err)
	}
	return nil
}

func appendNonEmpty(values []string, value string) []string {
	if value == "" {
		return values
	}
	return append(values, value)
}

func appendGeneratorWorkspaceRefs(appendLocalRef func(string), sources types.KvPairSources) {
	for _, source := range sources.FileSources {
		appendLocalRef(generatorFileSourcePath(source))
	}
	for _, source := range sources.EnvSources {
		appendLocalRef(source)
	}
}

type copyTreeOptions struct {
	preserveSymlinks bool
	linkRegularFiles bool
	forceCopy        workspaceCopyFiles
}

func copyWorkspaceTreeWithForceCopy(srcRoot, dstRoot string, forceCopy workspaceCopyFiles) error {
	return copyTree(srcRoot, dstRoot, copyTreeOptions{
		preserveSymlinks: true,
		linkRegularFiles: true,
		forceCopy:        forceCopy,
	})
}

//nolint:gocyclo // Tree copy handles regular files, directories, optional symlinks, hardlinks, and parent safety.
func copyTree(srcRoot, dstRoot string, options copyTreeOptions) error {
	srcRoot = filepath.Clean(srcRoot)
	dstRoot = filepath.Clean(dstRoot)

	return filepath.WalkDir(srcRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcRoot, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("copy source path %q escapes source root %q", path, srcRoot)
		}
		if strings.EqualFold(entry.Name(), ".git") {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		dstPath := filepath.Clean(filepath.Join(dstRoot, rel))
		dstRel, err := filepath.Rel(dstRoot, dstPath)
		if err != nil || dstRel == ".." || strings.HasPrefix(dstRel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("copy destination path %q escapes destination root %q", dstPath, dstRoot)
		}

		if entry.Type()&os.ModeSymlink != 0 {
			if !options.preserveSymlinks {
				return fmt.Errorf("copy source path %q is a symlink", path)
			}
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
				return err
			}
			return os.Symlink(target, dstPath)
		}

		if entry.IsDir() {
			return os.MkdirAll(dstPath, 0o755)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("copy source path %q is not a regular file", path)
		}
		if options.linkRegularFiles {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			return copyWorkspaceFile(path, dstPath, info, options.forceCopy)
		}
		return copyRegularFile(path, dstPath)
	})
}

func copyRegularFile(src, dst string) error {
	return filecopy.CopyRegularFile(src, dst, 0o644)
}

// copyWorkspaceFile puts the repository file src, whose Lstat is info, at
// dst in a prepared workspace: as a copy when forceCopy holds it, else as a
// hard link when it can. writeWorkspaceFile already breaks a hard link
// before it writes. The copy of a rewritten file is a second guard: a
// write in place at dst would still not reach src.
func copyWorkspaceFile(src, dst string, info os.FileInfo, forceCopy workspaceCopyFiles) error {
	if forceCopy.contains(info) {
		return copyRegularFile(src, dst)
	}
	return filecopy.LinkOrCopyRegularFile(src, dst, 0o644)
}
