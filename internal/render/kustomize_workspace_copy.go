package render

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sholdee/drydock/internal/filecopy"
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
	forceCopy  map[string]struct{}
}

func copyPreparedKustomizeWorkspaceTree(repoRoot, sourceRoot, dstRoot string, graph []kustomizeGraphNode) error {
	forceCopy, err := mutableKustomizationFiles(sourceRoot, graph)
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
	}
	return nil
}

func (c *preparedKustomizeWorkspaceCopier) copyPath(path string) error {
	path = filepath.Clean(path)
	rel, err := filepath.Rel(c.repoRoot, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("copy source path %q escapes source root %q", path, c.repoRoot)
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
	if err := copyWorkspaceFile(path, dstPath, c.forceCopy); err != nil {
		return err
	}
	c.copied[path] = struct{}{}
	return nil
}

func mutableKustomizationFiles(sourceRoot string, graph []kustomizeGraphNode) (map[string]struct{}, error) {
	out := map[string]struct{}{}
	for _, dir := range append([]string{sourceRoot}, kustomizeGraphDirs(graph)...) {
		file, err := findKustomizationFile(dir)
		if err != nil {
			return nil, err
		}
		out[filepath.Clean(file)] = struct{}{}
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
	appendLocalRef := func(ref string) {
		ref = strings.TrimSpace(ref)
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
	//nolint:staticcheck // Kustomize still accepts bases; copy local refs for parity.
	for _, ref := range kustomization.Bases {
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
		appendLocalRef(ref)
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
	//nolint:staticcheck // Kustomize still accepts patchesJson6902; copy local refs for parity.
	for _, patch := range kustomization.PatchesJson6902 {
		appendLocalRef(patch.Path)
	}
	//nolint:staticcheck // Kustomize still accepts patchesStrategicMerge; copy local refs for parity.
	for _, patch := range kustomization.PatchesStrategicMerge {
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

func appendGeneratorWorkspaceRefs(appendLocalRef func(string), sources types.KvPairSources) {
	for _, source := range sources.FileSources {
		appendLocalRef(generatorFileSourcePath(source))
	}
	for _, source := range sources.EnvSources {
		appendLocalRef(source)
	}
	appendLocalRef(sources.EnvSource)
}

type copyTreeOptions struct {
	preserveSymlinks bool
	linkRegularFiles bool
	forceCopy        map[string]struct{}
}

func copyWorkspaceTreeWithForceCopy(srcRoot, dstRoot string, forceCopy map[string]struct{}) error {
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
		if entry.Name() == ".git" {
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
			return copyWorkspaceFile(path, dstPath, options.forceCopy)
		}
		return copyRegularFile(path, dstPath)
	})
}

func copyRegularFile(src, dst string) error {
	return filecopy.CopyRegularFile(src, dst, 0o644)
}

func copyWorkspaceFile(src, dst string, forceCopy map[string]struct{}) error {
	if _, ok := forceCopy[filepath.Clean(src)]; ok {
		return copyRegularFile(src, dst)
	}
	return filecopy.LinkOrCopyRegularFile(src, dst, 0o644)
}
