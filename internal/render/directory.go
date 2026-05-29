package render

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	argoglob "github.com/argoproj/argo-cd/v3/util/glob"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/manifest"
	"sigs.k8s.io/kustomize/api/types"
)

type DirectoryRenderer struct{}

const (
	argocdSkipFileRenderingMarker = "+argocd:skip-file-rendering"
	drydockCacheMetadataDirName   = ".drydock-cache"
)

//nolint:gocyclo // Directory rendering keeps walk, filtering, decode, and path provenance in one pass.
func (DirectoryRenderer) Render(ctx context.Context, source ResolvedSource, opts RenderOptions) ([]Manifest, []diagnostic.Diagnostic, error) {
	root, err := sourceRoot(source)
	if err != nil {
		return nil, nil, err
	}
	var out []Manifest
	skipFiles, err := kustomizeGeneratorSkipSet(ctx, root, opts)
	if err != nil {
		return nil, nil, err
	}

	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			if shouldSkipDirectoryCandidate(root, path, entry, opts) {
				return filepath.SkipDir
			}
			return nil
		}
		if !isManifestFile(path) {
			return nil
		}
		if skipFiles[filepath.Clean(path)] {
			return nil
		}
		if !directoryManifestIncluded(root, path, opts) {
			return nil
		}

		manifestPath, pathErr := relativeManifestPath(source.RepoRoot, path)
		if pathErr != nil {
			return pathErr
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(data, []byte(argocdSkipFileRenderingMarker)) {
			return nil
		}
		if isJsonnetFile(path) {
			rendered, err := renderJsonnetFile(root, source.RepoRoot, path, manifestPath, opts)
			if err != nil {
				return err
			}
			out = append(out, rendered...)
			return nil
		}
		roots, err := manifest.DecodeDocumentRoots(manifestPath, bytes.NewReader(data))
		if err != nil {
			if shouldIgnoreDirectoryDecodeError(data) {
				return nil
			}
			return err
		}
		hasCandidate, err := classifyDirectoryRoots(roots)
		if err != nil {
			return err
		}
		if !hasCandidate {
			return nil
		}
		docs, err := manifest.DecodeDocuments(manifestPath, bytes.NewReader(data))
		if err != nil {
			return err
		}

		for _, doc := range docs {
			if doc.RootObject != nil && doc.RootObject != doc.Object {
				if _, err := classifyDirectoryDocument(Manifest{Path: doc.Path, Object: doc.RootObject}); err != nil {
					return err
				}
			}
			rendered := Manifest{
				Path:   doc.Path,
				Object: doc.Object,
			}
			include, err := classifyDirectoryDocument(rendered)
			if err != nil {
				return err
			}
			if !include {
				continue
			}
			out = append(out, rendered)
		}
		return nil
	})
	return out, nil, err
}

func classifyDirectoryRoots(docs []manifest.Document) (bool, error) {
	hasCandidate := false
	for _, doc := range docs {
		include, err := classifyDirectoryDocument(Manifest{Path: doc.Path, Object: doc.Object})
		if err != nil {
			return false, err
		}
		if include {
			hasCandidate = true
		}
	}
	return hasCandidate, nil
}

func classifyDirectoryDocument(manifest Manifest) (bool, error) {
	if manifest.Object == nil {
		return false, nil
	}
	apiVersion := manifest.Object.GetAPIVersion()
	kind := manifest.Object.GetKind()
	if apiVersion == "" && kind == "" {
		return false, nil
	}
	if apiVersion == "" {
		return false, fmt.Errorf("%s document is missing apiVersion", manifest.Path)
	}
	if kind == "" {
		return false, fmt.Errorf("%s document is missing kind", manifest.Path)
	}
	return true, nil
}

func shouldIgnoreDirectoryDecodeError(data []byte) bool {
	return !directoryDataLooksKubernetesManifest(data)
}

func directoryDataLooksKubernetesManifest(data []byte) bool {
	text := string(data)
	if strings.Contains(text, `"apiVersion"`) || strings.Contains(text, `"kind"`) {
		return true
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		trimmedLeft := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmedLeft, "#") || strings.HasPrefix(trimmedLeft, "---") || strings.HasPrefix(trimmedLeft, "...") {
			continue
		}
		if len(trimmedLeft) != len(line) {
			continue
		}
		if strings.HasPrefix(trimmedLeft, "apiVersion:") || strings.HasPrefix(trimmedLeft, "kind:") {
			return true
		}
	}
	return false
}

func directoryManifestIncluded(root, filePath string, opts RenderOptions) bool {
	rel, err := filepath.Rel(root, filePath)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	if opts.DirectoryExclude != "" && argoglob.Match(opts.DirectoryExclude, rel) {
		return false
	}
	if opts.DirectoryInclude != "" && !argoglob.Match(opts.DirectoryInclude, rel) {
		return false
	}
	return true
}

func shouldSkipDirectoryCandidate(root, path string, entry os.DirEntry, opts RenderOptions) bool {
	if path == root {
		return false
	}
	if entry.Name() == drydockCacheMetadataDirName {
		return true
	}
	return !opts.DirectoryRecurse
}

func kustomizeGeneratorSkipSet(ctx context.Context, root string, opts RenderOptions) (map[string]bool, error) {
	skipFiles := make(map[string]bool)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			if shouldSkipDirectoryCandidate(root, path, entry, opts) {
				return filepath.SkipDir
			}
			return nil
		}
		if !isKustomizationFileName(entry.Name()) {
			return nil
		}
		skipFiles[filepath.Clean(path)] = true

		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		var kustomization types.Kustomization
		if !unmarshalKustomizationForDirectorySkip(content, &kustomization) {
			return nil
		}
		for _, generator := range kustomization.ConfigMapGenerator {
			addKvPairSourcesToSkipSet(skipFiles, root, filepath.Dir(path), generator.KvPairSources)
		}
		for _, generator := range kustomization.SecretGenerator {
			addKvPairSourcesToSkipSet(skipFiles, root, filepath.Dir(path), generator.KvPairSources)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return skipFiles, nil
}

func unmarshalKustomizationForDirectorySkip(content []byte, kustomization *types.Kustomization) bool {
	return kustomization.Unmarshal(content) == nil
}

func addKvPairSourcesToSkipSet(skipFiles map[string]bool, root, dir string, sources types.KvPairSources) {
	for _, source := range sources.FileSources {
		addKustomizeGeneratorRefToSkipSet(skipFiles, root, dir, generatorFileSourcePath(source))
	}
	for _, source := range sources.EnvSources {
		addKustomizeGeneratorRefToSkipSet(skipFiles, root, dir, source)
	}
	if sources.EnvSource != "" {
		addKustomizeGeneratorRefToSkipSet(skipFiles, root, dir, sources.EnvSource)
	}
}

func addKustomizeGeneratorRefToSkipSet(skipFiles map[string]bool, root, dir, ref string) {
	ref = strings.TrimSpace(ref)
	if ref == "" || filepath.IsAbs(ref) {
		return
	}
	if isRemoteKustomizeRef(ref) {
		return
	}
	cleanRef := filepath.Clean(filepath.FromSlash(ref))
	if filepath.IsAbs(cleanRef) {
		return
	}
	path := filepath.Clean(filepath.Join(dir, cleanRef))
	if rel, err := filepath.Rel(root, path); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return
	}
	if err := rejectSymlinkedPath(root, path); err != nil {
		return
	}
	skipFiles[path] = true
}

func isKustomizationFileName(name string) bool {
	for _, candidate := range kustomizationFileNames {
		if name == candidate {
			return true
		}
	}
	return false
}

func isManifestFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".yaml" || ext == ".yml" || ext == ".json" || ext == ".jsonnet"
}

func isJsonnetFile(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".jsonnet")
}

func sourceRoot(source ResolvedSource) (string, error) {
	sourcePath, err := cleanSourcePath(source.Path)
	if err != nil {
		return "", err
	}
	if err := rejectSymlinkComponents(source.RepoRoot, sourcePath); err != nil {
		return "", err
	}
	return filepath.Join(source.RepoRoot, sourcePath), nil
}

func cleanSourcePath(path string) (string, error) {
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("source path %q must be relative", path)
	}

	clean := filepath.Clean(path)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("source path %q escapes repository root", path)
	}
	return clean, nil
}

func rejectSymlinkComponents(repoRoot, sourcePath string) error {
	if sourcePath == "." {
		return nil
	}

	current := filepath.Clean(repoRoot)
	for _, component := range strings.Split(sourcePath, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("source path %q includes symlink component %q", sourcePath, component)
		}
	}
	return nil
}

func relativeManifestPath(repoRoot, path string) (string, error) {
	rel, err := filepath.Rel(repoRoot, path)
	if err != nil || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return "", fmt.Errorf("manifest path %q escapes repository root %q", path, repoRoot)
	}
	return rel, nil
}
