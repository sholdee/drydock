package render

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/home-operations/argocd-local/internal/diagnostic"
	"github.com/home-operations/argocd-local/internal/manifest"
	"sigs.k8s.io/kustomize/api/types"
)

type DirectoryRenderer struct{}

func (DirectoryRenderer) Render(ctx context.Context, source ResolvedSource, _ RenderOptions) ([]Manifest, []diagnostic.Diagnostic, error) {
	root, err := sourceRoot(source)
	if err != nil {
		return nil, nil, err
	}
	var out []Manifest
	skipFiles, err := kustomizeGeneratorSkipSet(ctx, root)
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
			if path != root && strings.HasPrefix(entry.Name(), ".") {
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

		file, err := os.Open(path)
		if err != nil {
			return err
		}
		manifestPath, pathErr := relativeManifestPath(source.RepoRoot, path)
		if pathErr != nil {
			_ = file.Close()
			return pathErr
		}
		docs, decodeErr := manifest.DecodeDocuments(manifestPath, file)
		closeErr := file.Close()
		if decodeErr != nil {
			return decodeErr
		}
		if closeErr != nil {
			return closeErr
		}

		for _, doc := range docs {
			out = append(out, Manifest{
				Path:   doc.Path,
				Object: doc.Object,
			})
		}
		return nil
	})
	return out, nil, err
}

func kustomizeGeneratorSkipSet(ctx context.Context, root string) (map[string]bool, error) {
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
			if path != root && strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !isKustomizationFileName(entry.Name()) {
			return nil
		}
		skipFiles[filepath.Clean(path)] = true

		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var kustomization types.Kustomization
		if err := kustomization.Unmarshal(content); err != nil {
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
	return ext == ".yaml" || ext == ".yml" || ext == ".json"
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
