package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/discovery"
	"github.com/sholdee/drydock/internal/render"
	sourcepkg "github.com/sholdee/drydock/internal/source"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func localDiscoverySourceRoot(root string, request BuildRequest, source argoappv1.ApplicationSource) (string, bool) {
	repoRoot := root
	if mapped, ok := mappedRepositoryPath(request, source.RepoURL); ok {
		repoRoot = mapped
	}
	sourcePath := source.Path
	if strings.TrimSpace(sourcePath) == "" {
		return repoRoot, true
	}
	clean, err := cleanLocalSourcePath(sourcePath)
	if err != nil {
		return "", false
	}
	path := filepath.Join(repoRoot, clean)
	exists, err := localPathExists(path)
	if err != nil || !exists {
		return "", false
	}
	return path, true
}

func mappedRepositoryPath(request BuildRequest, repoURL string) (string, bool) {
	normalized := sourcepkg.NormalizeURL(repoURL)
	for _, repoMap := range request.RepoMaps {
		if sourcepkg.NormalizeURL(repoMap.URL) == normalized {
			return repoMap.Path, true
		}
	}
	return "", false
}

func pathMayContainDiscoveryObjects(root string) (bool, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, nil
	}
	if !info.IsDir() {
		return fileMayContainDiscoveryObjects(root)
	}
	found := false
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if found {
			return filepath.SkipAll
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			if path != root && shouldSkipDiscoveryCandidateDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		matches, err := fileMayContainDiscoveryObjects(path)
		if err != nil {
			return err
		}
		found = matches
		return nil
	})
	return found, err
}

func fileMayContainDiscoveryObjects(path string) (bool, error) {
	if !isDiscoveryCandidateYAML(path) {
		return false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	return textMayContainDiscoveryObjects(string(data)), nil
}

func textMayContainDiscoveryObjects(text string) bool {
	return strings.Contains(text, "kind: Application") ||
		strings.Contains(text, "kind: ApplicationSet") ||
		strings.Contains(text, "kind: AppProject") ||
		strings.Contains(text, "argocd-cm") ||
		strings.Contains(text, "argocd-cmp-cm") ||
		strings.Contains(text, "argocd.argoproj.io/secret-type")
}

func localSourceAlreadyDiscovered(root, sourceRoot string, discovered discovery.Result) bool {
	rel, err := filepath.Rel(root, sourceRoot)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return false
	}
	rel = filepath.ToSlash(filepath.Clean(rel))
	if rel == "." {
		rel = ""
	}
	return discoveryHasPathUnder(discovered, rel)
}

func discoveryHasPathUnder(discovered discovery.Result, root string) bool {
	for _, item := range discovered.Applications {
		if pathUnderRoot(item.Path, root) {
			return true
		}
	}
	for _, item := range discovered.ApplicationSets {
		if pathUnderRoot(item.Path, root) {
			return true
		}
	}
	for _, item := range discovered.Projects {
		if pathUnderRoot(item.Path, root) {
			return true
		}
	}
	for _, item := range discovered.SettingsCandidates {
		if pathUnderRoot(item.Path, root) {
			return true
		}
	}
	return false
}

func pathUnderRoot(pathValue, root string) bool {
	pathValue = filepath.ToSlash(filepath.Clean(pathValue))
	root = filepath.ToSlash(filepath.Clean(root))
	if root == "." || root == "" {
		return pathValue != "." && pathValue != ""
	}
	return pathValue == root || strings.HasPrefix(pathValue, root+"/")
}

func sourceRootHasLocalChart(root string) bool {
	exists, err := localPathExists(filepath.Join(root, "Chart.yaml"))
	return err == nil && exists
}

func shouldSkipDiscoveryCandidateDir(name string) bool {
	return name == ".git" || name == ".out" || strings.HasPrefix(name, ".cache")
}

func isDiscoveryCandidateYAML(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".yaml" || ext == ".yml"
}

func cleanDiscoverKustomizePath(root, rawPath string) (string, error) {
	clean, err := cleanLocalSourcePath(rawPath)
	if err != nil {
		return "", fmt.Errorf("discover-kustomize path %q: %w", rawPath, err)
	}
	if err := rejectLocalSymlinkComponents(root, clean); err != nil {
		return "", fmt.Errorf("discover-kustomize path %q: %w", rawPath, err)
	}
	if !hasKustomizationFile(filepath.Join(root, clean)) {
		return "", fmt.Errorf("discover-kustomize path %q does not contain a kustomization file", rawPath)
	}
	return clean, nil
}

func hasKustomizationFile(root string) bool {
	for _, name := range []string{"kustomization.yaml", "kustomization.yml", "Kustomization"} {
		exists, err := localPathExists(filepath.Join(root, name))
		if err == nil && exists {
			return true
		}
	}
	return false
}

func manifestObjects(manifests []render.Manifest) []*unstructured.Unstructured {
	objects := make([]*unstructured.Unstructured, 0, len(manifests))
	for _, manifest := range manifests {
		if manifest.Object == nil {
			continue
		}
		objects = append(objects, manifest.Object.DeepCopy())
	}
	return objects
}
