package render

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	argofiles "github.com/argoproj/argo-cd/v3/util/io/files"
	"github.com/google/go-jsonnet"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// renderJsonnetFile evaluates one Jsonnet file of a directory source and
// returns the objects it produces, as the repo-server's findManifests does
// (reposerver/repository/repository.go:2005-2051 at v3.6.1). The objects go
// through the same list split and kind check as the objects of every other
// file.
func renderJsonnetFile(appPath, repoRoot, filePath, manifestPath string, opts RenderOptions) ([]generatedObject, error) {
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
	return decodeJsonnetObjects(manifestPath, []byte(jsonOutput))
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

// resolveJsonnetLib resolves one entry of spec.source.directory.jsonnet.libs
// as the Argo CD v3.6.1 repo-server does, with
// pathutil.ResolveFileOrDirectoryPath(repoRoot, repoRoot, lib)
// (reposerver/repository/repository.go:2304-2313, util/io/path/
// resolved.go:73-80 and 134-185). The lib is relative to the repository
// root, also when it starts with "/". When the path itself is a symlink, it
// is resolved, up to ten levels. The result may be the repository root, and
// must not be outside it, which fails with the repo-server's "file '<lib>'
// resolved to outside repository root". The lib directory need not exist.
// drydock also rejects a lib in a .git directory, and trims spaces around
// the lib.
func resolveJsonnetLib(repoRoot, raw string) (string, error) {
	lib := strings.TrimSpace(raw)
	absRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return "", err
	}
	// filepath.Join cleans, so an absolute lib is joined to the root.
	path := filepath.Join(absRoot, filepath.FromSlash(lib))
	resolved, err := resolveJsonnetLibSymlink(path, 10)
	if err != nil {
		return "", fmt.Errorf("jsonnet lib %q: internal error: failed to resolve path. Check logs for more details: %w", raw, err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", err
	}
	requiredRoot := absRoot
	if !strings.HasSuffix(requiredRoot, string(os.PathSeparator)) {
		requiredRoot += string(os.PathSeparator)
	}
	if resolved+string(os.PathSeparator) != requiredRoot && !strings.HasPrefix(resolved, requiredRoot) {
		return "", fmt.Errorf("jsonnet lib: file '%s' resolved to outside repository root", lib)
	}
	if pathEntersGit(absRoot, resolved) {
		return "", gitPathRefError("jsonnet lib", raw)
	}
	return resolved, nil
}

// resolveJsonnetLibSymlink is resolveSymbolicLinkRecursive (util/io/path/
// resolved.go:22-47 at v3.6.1): when path itself is a symlink, it follows it
// up to maxDepth levels, joining a relative target to the symlink's
// directory. A path that is not a symlink, or does not exist, is returned
// unchanged.
func resolveJsonnetLibSymlink(path string, maxDepth int) (string, error) {
	target, err := os.Readlink(path)
	if err != nil {
		if _, ok := errors.AsType[*os.PathError](err); ok {
			return path, nil
		}
		return "", err
	}
	if maxDepth == 0 {
		return "", errors.New("maximum nesting level reached")
	}
	if !strings.HasPrefix(target, string(os.PathSeparator)) {
		target = filepath.Join(filepath.Dir(path), target)
	}
	return resolveJsonnetLibSymlink(target, maxDepth-1)
}

// boundedJsonnetImporter resolves import, importstr, and importbin the way
// go-jsonnet's FileImporter does (the importing file's directory first, then
// the library paths in reverse order) while confining every resolved path to
// the repository root. This mirrors the Argo CD repo-server's confinedImporter
// (reposerver/repository/repository.go:2330-2397 at v3.6.1): the first
// candidate that exists wins, its symlinks are resolved, and the import fails
// when the resolved path is outside the repository root or is not a regular
// file. So an import may go through an in-repository symlink, and a relative
// import in a file reached through a symlink resolves against the directory
// of the symlink, not of its target. drydock also rejects a path in a .git
// directory, and an absolute import outright: drydock renders CI checkouts
// whose absolute paths never match a repo-server's, so accepting them would
// only invite path confusion.
type boundedJsonnetImporter struct {
	boundary string
	roots    []string
	cache    map[string]*boundedJsonnetImportCacheEntry
	// realBoundary is boundary with its symlinks resolved, set on first use.
	// files.Inbound compares a resolved import path with it as text.
	realBoundary string
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
		contents, found, err := i.tryImport(candidate, importedPath)
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

func (i *boundedJsonnetImporter) tryImport(path, importedPath string) (jsonnet.Contents, bool, error) {
	if pathEntersGit(i.boundary, path) {
		return jsonnet.Contents{}, false, fmt.Errorf("jsonnet import %q enters a .git directory", path)
	}
	if entry, ok := i.cache[path]; ok {
		return entry.contents, entry.exists, nil
	}
	// confinedImporter.locate takes the first candidate that os.Lstat finds,
	// so a symlink whose target is missing is found here and fails below.
	if _, err := os.Lstat(path); err != nil {
		if os.IsNotExist(err) {
			i.cache[path] = &boundedJsonnetImportCacheEntry{exists: false}
			return jsonnet.Contents{}, false, nil
		}
		return jsonnet.Contents{}, false, err
	}
	resolved, err := i.validate(path, importedPath)
	if err != nil {
		return jsonnet.Contents{}, false, err
	}
	// Read the resolved path that validate checked.
	data, err := os.ReadFile(resolved)
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

// validate is confinedImporter.validate (reposerver/repository/
// repository.go:2381-2397 at v3.6.1): it resolves the symlinks of the found
// path with files.ResolveInbound and requires a regular file inside the
// repository root. It checks with stat calls only, so a non-regular file
// such as a device is never read. drydock also rejects a resolved path in a
// .git directory.
func (i *boundedJsonnetImporter) validate(foundAt, importedPath string) (string, error) {
	if i.realBoundary == "" {
		realBoundary, err := filepath.EvalSymlinks(i.boundary)
		if err != nil {
			return "", fmt.Errorf("resolve repository root: %w", err)
		}
		i.realBoundary = realBoundary
	}
	resolved, ok, err := argofiles.ResolveInbound(foundAt, i.realBoundary)
	if err != nil {
		return "", fmt.Errorf("failed to resolve jsonnet import %q: %w", importedPath, err)
	}
	if !ok {
		return "", fmt.Errorf("jsonnet import %q resolves outside repository root", importedPath)
	}
	if pathEntersGit(i.realBoundary, resolved) {
		return "", fmt.Errorf("jsonnet import %q resolves into a .git directory", importedPath)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("failed to stat jsonnet import %q: %w", importedPath, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("jsonnet import %q is not a regular file", importedPath)
	}
	return resolved, nil
}

// decodeJsonnetObjects decodes Jsonnet output the way findManifests does.
// It first decodes the output as an array of Unstructured objects, and when
// that fails, as one Unstructured object. Each Unstructured decode fails on an
// object without a non-empty string kind, so such an object fails the source
// in the repo-server. A JSON null in the array, or null output, adds nothing.
func decodeJsonnetObjects(manifestPath string, data []byte) ([]generatedObject, error) {
	var objects []*unstructured.Unstructured
	if err := json.Unmarshal(data, &objects); err == nil {
		out := make([]generatedObject, 0, len(objects))
		for index, object := range objects {
			out = append(out, generatedObject{
				path:   manifestPath,
				label:  fmt.Sprintf("%s output item %d", manifestPath, index),
				object: object,
			})
		}
		return out, nil
	}
	var object unstructured.Unstructured
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, fmt.Errorf("failed to unmarshal generated json %q: %s", manifestPath, describeJsonnetDecodeError(data, err))
	}
	return []generatedObject{{
		path:   manifestPath,
		label:  manifestPath + " output",
		object: &object,
	}}, nil
}

// describeJsonnetDecodeError names why Jsonnet output did not decode,
// without echoing the output: Unstructured's missing-kind error quotes the
// whole object. The repo-server reports the error of its second decode,
// which for array output only says that an array is not an object. drydock
// names the array item that failed the first decode instead.
func describeJsonnetDecodeError(data []byte, err error) string {
	var items []json.RawMessage
	if json.Unmarshal(data, &items) != nil {
		return describeDirectoryJSONDecodeError(err)
	}
	for index, item := range items {
		var object *unstructured.Unstructured
		if itemErr := json.Unmarshal(item, &object); itemErr != nil {
			return fmt.Sprintf("output item %d: %s", index, describeDirectoryJSONDecodeError(itemErr))
		}
	}
	return describeDirectoryJSONDecodeError(err)
}
