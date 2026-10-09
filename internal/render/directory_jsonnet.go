package render

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/google/go-jsonnet"
	"github.com/sholdee/drydock/internal/pathsafety"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func renderJsonnetFile(appPath, repoRoot, filePath, manifestPath string, opts RenderOptions) ([]Manifest, error) {
	vm, err := makeJsonnetVM(appPath, repoRoot, opts.Jsonnet, opts.ArgoEnv)
	if err != nil {
		return nil, err
	}
	absFilePath, err := filepath.Abs(filePath)
	if err != nil {
		return nil, err
	}
	jsonOutput, err := vm.EvaluateFile(absFilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to evaluate jsonnet %q: %w", manifestPath, err)
	}
	manifests, err := decodeJsonnetManifests(manifestPath, []byte(jsonOutput))
	if err != nil {
		return nil, err
	}
	return manifests, nil
}

func makeJsonnetVM(appPath, repoRoot string, sourceJsonnet argoappv1.ApplicationSourceJsonnet, env argoappv1.Env) (*jsonnet.VM, error) {
	vm := jsonnet.MakeVM()
	for _, arg := range sourceJsonnet.TLAs {
		value := env.Envsubst(arg.Value)
		if arg.Code {
			vm.TLACode(arg.Name, value)
		} else {
			vm.TLAVar(arg.Name, value)
		}
	}
	for _, extVar := range sourceJsonnet.ExtVars {
		value := env.Envsubst(extVar.Value)
		if extVar.Code {
			vm.ExtCode(extVar.Name, value)
		} else {
			vm.ExtVar(extVar.Name, value)
		}
	}

	absAppPath, err := filepath.Abs(appPath)
	if err != nil {
		return nil, err
	}
	absRepoRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return nil, err
	}
	jpaths := []string{absAppPath}
	for _, lib := range sourceJsonnet.Libs {
		resolved, err := resolveJsonnetLib(repoRoot, lib)
		if err != nil {
			return nil, err
		}
		jpaths = append(jpaths, resolved)
	}
	vm.Importer(newBoundedJsonnetImporter(absRepoRoot, jpaths))
	return vm, nil
}

func resolveJsonnetLib(repoRoot, raw string) (string, error) {
	lib := strings.TrimSpace(raw)
	clean := filepath.Clean(filepath.FromSlash(lib))
	if filepath.IsAbs(clean) {
		return "", fmt.Errorf("jsonnet lib %q must be relative to repository root", raw)
	}
	if pathsafety.RelEscapes(clean) {
		return "", fmt.Errorf("jsonnet lib %q escapes repository root", raw)
	}
	if pathsafety.RelEntersGit(clean) {
		return "", gitPathRefError("jsonnet lib", raw)
	}
	resolved := filepath.Join(repoRoot, clean)
	if err := rejectPathOutsideBoundary("jsonnet lib", resolved, repoRoot); err != nil {
		return "", err
	}
	if err := rejectSymlinkedPath(repoRoot, resolved); err != nil {
		return "", err
	}
	return filepath.Abs(resolved)
}

// boundedJsonnetImporter resolves import, importstr, and importbin the way
// go-jsonnet's FileImporter does (the importing file's directory first, then
// the library paths in reverse order) while confining every resolved path to
// the repository root. This mirrors the Argo CD repo-server's confined
// importer (reposerver/repository, v3.5.4+): a relative import may reach any
// file under the repository root but never a path outside it, a symlinked
// path, a .git directory, or a non-regular file. Absolute imports are
// rejected outright; drydock renders CI checkouts whose absolute paths never
// match a repo-server's, so accepting them would only invite path confusion.
type boundedJsonnetImporter struct {
	boundary string
	roots    []string
	cache    map[string]*boundedJsonnetImportCacheEntry
}

type boundedJsonnetImportCacheEntry struct {
	contents jsonnet.Contents
	exists   bool
}

func newBoundedJsonnetImporter(boundary string, roots []string) *boundedJsonnetImporter {
	normalized := make([]string, 0, len(roots))
	for _, root := range roots {
		normalized = append(normalized, filepath.Clean(root))
	}
	return &boundedJsonnetImporter{
		boundary: filepath.Clean(boundary),
		roots:    normalized,
		cache:    map[string]*boundedJsonnetImportCacheEntry{},
	}
}

func (i *boundedJsonnetImporter) Import(importedFrom, importedPath string) (jsonnet.Contents, string, error) {
	candidates, err := i.importCandidates(importedFrom, importedPath)
	if err != nil {
		return jsonnet.Contents{}, "", err
	}
	for _, candidate := range candidates {
		contents, found, err := i.tryImport(candidate)
		if err != nil {
			return jsonnet.Contents{}, "", err
		}
		if found {
			return contents, candidate, nil
		}
	}
	return jsonnet.Contents{}, "", fmt.Errorf("couldn't open import %q: no match locally or in the Jsonnet library paths", importedPath)
}

func (i *boundedJsonnetImporter) importCandidates(importedFrom, importedPath string) ([]string, error) {
	if strings.TrimSpace(importedFrom) == "" && filepath.IsAbs(importedPath) {
		// The entrypoint handed to EvaluateFile: an absolute path drydock
		// built under the source root, not repository content.
		candidate := filepath.Clean(importedPath)
		if err := rejectPathOutsideBoundary("jsonnet import", candidate, i.boundary); err != nil {
			return nil, err
		}
		return []string{candidate}, nil
	}
	if filepath.IsAbs(importedPath) {
		return nil, fmt.Errorf("jsonnet import %q must be relative", importedPath)
	}

	importPath := filepath.FromSlash(importedPath)
	var candidates []string
	seen := map[string]bool{}
	if strings.TrimSpace(importedFrom) != "" {
		from, err := filepath.Abs(importedFrom)
		if err != nil {
			return nil, err
		}
		if err := rejectPathOutsideBoundary("jsonnet import source", from, i.boundary); err != nil {
			return nil, err
		}
		candidate := filepath.Clean(filepath.Join(filepath.Dir(from), importPath))
		if err := rejectPathOutsideBoundary("jsonnet import", candidate, i.boundary); err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
		seen[candidate] = true
	}

	for _, root := range slices.Backward(i.roots) {
		candidate := filepath.Clean(filepath.Join(root, importPath))
		if err := rejectPathOutsideBoundary("jsonnet import", candidate, i.boundary); err != nil {
			if len(candidates) == 0 {
				return nil, err
			}
			continue
		}
		if seen[candidate] {
			continue
		}
		candidates = append(candidates, candidate)
		seen[candidate] = true
	}
	return candidates, nil
}

func (i *boundedJsonnetImporter) tryImport(path string) (jsonnet.Contents, bool, error) {
	if pathEntersGit(i.boundary, path) {
		return jsonnet.Contents{}, false, fmt.Errorf("jsonnet import %q enters a .git directory", path)
	}
	if err := rejectSymlinkedPath(i.boundary, path); err != nil {
		return jsonnet.Contents{}, false, err
	}
	if entry, ok := i.cache[path]; ok {
		return entry.contents, entry.exists, nil
	}
	// Stat before reading: a non-regular file (a directory, or a device such
	// as /dev/zero reached through some future gap) must be rejected without
	// ever being read.
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			i.cache[path] = &boundedJsonnetImportCacheEntry{exists: false}
			return jsonnet.Contents{}, false, nil
		}
		return jsonnet.Contents{}, false, err
	}
	if !info.Mode().IsRegular() {
		return jsonnet.Contents{}, false, fmt.Errorf("jsonnet import %q is not a regular file", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return jsonnet.Contents{}, false, err
	}
	entry := &boundedJsonnetImportCacheEntry{
		contents: jsonnet.MakeContentsRaw(data),
		exists:   true,
	}
	i.cache[path] = entry
	return entry.contents, true, nil
}

func decodeJsonnetManifests(path string, data []byte) ([]Manifest, error) {
	var objects []map[string]any
	if err := json.Unmarshal(data, &objects); err == nil {
		return jsonnetManifestsFromObjects(path, objects)
	}

	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, fmt.Errorf("failed to unmarshal generated json %q: %w", path, err)
	}
	if object == nil {
		return nil, nil
	}
	return jsonnetManifestsFromObjects(path, []map[string]any{object})
}

func jsonnetManifestsFromObjects(path string, objects []map[string]any) ([]Manifest, error) {
	manifests := make([]Manifest, 0, len(objects))
	for _, object := range objects {
		if object == nil {
			continue
		}
		manifest := Manifest{
			Path:   path,
			Object: &unstructured.Unstructured{Object: object},
		}
		include, err := classifyDirectoryDocument(manifest)
		if err != nil {
			return nil, err
		}
		if !include {
			continue
		}
		manifests = append(manifests, manifest)
	}
	return manifests, nil
}
