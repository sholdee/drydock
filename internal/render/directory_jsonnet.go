package render

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	jpaths := []string{absAppPath}
	for _, lib := range sourceJsonnet.Libs {
		resolved, err := resolveJsonnetLib(repoRoot, lib)
		if err != nil {
			return nil, err
		}
		jpaths = append(jpaths, resolved)
	}
	vm.Importer(newBoundedJsonnetImporter(jpaths))
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
	resolved := filepath.Join(repoRoot, clean)
	if err := rejectPathOutsideBoundary("jsonnet lib", resolved, repoRoot); err != nil {
		return "", err
	}
	if err := rejectSymlinkedPath(repoRoot, resolved); err != nil {
		return "", err
	}
	return filepath.Abs(resolved)
}

type boundedJsonnetImporter struct {
	roots []string
	cache map[string]*boundedJsonnetImportCacheEntry
}

type boundedJsonnetImportCacheEntry struct {
	contents jsonnet.Contents
	exists   bool
}

func newBoundedJsonnetImporter(roots []string) *boundedJsonnetImporter {
	normalized := make([]string, 0, len(roots))
	for _, root := range roots {
		normalized = append(normalized, filepath.Clean(root))
	}
	return &boundedJsonnetImporter{roots: normalized, cache: map[string]*boundedJsonnetImportCacheEntry{}}
}

func (i *boundedJsonnetImporter) Import(importedFrom, importedPath string) (jsonnet.Contents, string, error) {
	candidates, err := i.importCandidates(importedFrom, importedPath)
	if err != nil {
		return jsonnet.Contents{}, "", err
	}
	for _, candidate := range candidates {
		contents, found, err := i.tryImport(candidate.root, candidate.path)
		if err != nil {
			return jsonnet.Contents{}, "", err
		}
		if found {
			return contents, candidate.path, nil
		}
	}
	return jsonnet.Contents{}, "", fmt.Errorf("couldn't open import %q: no match locally or in the Jsonnet library paths", importedPath)
}

type jsonnetImportCandidate struct {
	root string
	path string
}

func (i *boundedJsonnetImporter) importCandidates(importedFrom, importedPath string) ([]jsonnetImportCandidate, error) {
	if strings.TrimSpace(importedFrom) == "" && filepath.IsAbs(importedPath) {
		root, ok, err := i.rootFor(importedPath)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("jsonnet import %q is outside configured import roots", importedPath)
		}
		candidate := filepath.Clean(importedPath)
		if err := rejectPathOutsideBoundary("jsonnet import", candidate, root); err != nil {
			return nil, err
		}
		return []jsonnetImportCandidate{{root: root, path: candidate}}, nil
	}
	if filepath.IsAbs(importedPath) {
		return nil, fmt.Errorf("jsonnet import %q must be relative", importedPath)
	}

	importPath := filepath.FromSlash(importedPath)
	var candidates []jsonnetImportCandidate
	seen := map[string]bool{}
	if strings.TrimSpace(importedFrom) != "" {
		root, ok, err := i.rootFor(importedFrom)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("jsonnet import source %q is outside configured import roots", importedFrom)
		}
		candidate := filepath.Clean(filepath.Join(filepath.Dir(importedFrom), importPath))
		if err := rejectPathOutsideBoundary("jsonnet import", candidate, root); err != nil {
			return nil, err
		}
		candidates = append(candidates, jsonnetImportCandidate{root: root, path: candidate})
		seen[candidate] = true
	}

	for idx := len(i.roots) - 1; idx >= 0; idx-- {
		root := i.roots[idx]
		candidate := filepath.Clean(filepath.Join(root, importPath))
		if err := rejectPathOutsideBoundary("jsonnet import", candidate, root); err != nil {
			if len(candidates) == 0 {
				return nil, err
			}
			continue
		}
		if seen[candidate] {
			continue
		}
		candidates = append(candidates, jsonnetImportCandidate{root: root, path: candidate})
		seen[candidate] = true
	}
	return candidates, nil
}

func (i *boundedJsonnetImporter) rootFor(path string) (string, bool, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", false, err
	}
	absPath = filepath.Clean(absPath)
	for _, root := range i.roots {
		rel, err := filepath.Rel(root, absPath)
		if err == nil && !pathsafety.RelEscapes(rel) {
			return root, true, nil
		}
	}
	return "", false, nil
}

func (i *boundedJsonnetImporter) tryImport(root, path string) (jsonnet.Contents, bool, error) {
	if err := rejectSymlinkedPath(root, path); err != nil {
		return jsonnet.Contents{}, false, err
	}
	if entry, ok := i.cache[path]; ok {
		return entry.contents, entry.exists, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			i.cache[path] = &boundedJsonnetImportCacheEntry{exists: false}
			return jsonnet.Contents{}, false, nil
		}
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
