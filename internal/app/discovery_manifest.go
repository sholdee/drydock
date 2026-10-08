package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/discovery"
	"github.com/sholdee/drydock/internal/manifest"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// withDiscoverManifests loads --discover-manifest files once and keeps the
// decoded objects on the options, so both diff sides discover the same
// Applications and ApplicationSets without re-reading the files.
func withDiscoverManifests(options DiscoveryOptions) (DiscoveryOptions, error) {
	if options.discoverManifests != nil || len(options.DiscoverManifestPaths) == 0 {
		return options, nil
	}
	loaded, err := loadDiscoverManifests(options.DiscoverManifestPaths)
	if err != nil {
		return options, err
	}
	options.discoverManifests = &loaded
	return options, nil
}

// discoverManifestResult returns a deep copy of the external discovery
// objects, loading them first when the caller did not.
func (options DiscoveryOptions) discoverManifestResult() (discovery.Result, error) {
	loaded, err := withDiscoverManifests(options)
	if err != nil || loaded.discoverManifests == nil {
		return discovery.Result{}, err
	}
	return cloneDiscoverManifestResult(*loaded.discoverManifests), nil
}

// mergeDiscoverManifests adds the --discover-manifest objects to a side's
// repository discovery.
func mergeDiscoverManifests(request BuildRequest, discovered discovery.Result) (discovery.Result, []diagnostic.Diagnostic, error) {
	external, err := request.discoverManifestResult()
	if err != nil {
		return discovered, nil, err
	}
	merged, diags := mergeDiscoveryResultsWithDiagnostics(discovered, external)
	return merged, diags, nil
}

func loadDiscoverManifests(rawPaths []string) (discovery.Result, error) {
	var out discovery.Result
	seen := map[string]struct{}{}
	for _, rawPath := range rawPaths {
		path, err := cleanDiscoverManifestPath(rawPath)
		if err != nil {
			return discovery.Result{}, err
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		next, err := loadDiscoverManifest(rawPath, path)
		if err != nil {
			return discovery.Result{}, err
		}
		out.Applications = append(out.Applications, next.Applications...)
		out.ApplicationSets = append(out.ApplicationSets, next.ApplicationSets...)
	}
	return out, nil
}

// cleanDiscoverManifestPath accepts an absolute or working-directory-relative
// operator path. The path may sit outside the repository, but it must not
// contain ".." components and the file itself must be a regular file, not a
// symlink.
func cleanDiscoverManifestPath(rawPath string) (string, error) {
	trimmed := strings.TrimSpace(rawPath)
	if trimmed == "" {
		return "", fmt.Errorf("discover-manifest path must not be empty")
	}
	if slices.Contains(strings.Split(filepath.ToSlash(trimmed), "/"), "..") {
		return "", fmt.Errorf("discover-manifest path %q must not contain .. components", rawPath)
	}
	path, err := filepath.Abs(trimmed)
	if err != nil {
		return "", fmt.Errorf("discover-manifest path %q: %w", rawPath, err)
	}
	// Only the final component is checked for a symlink on purpose: the path
	// is operator input, and parents such as macOS /tmp are symlinks.
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("discover-manifest path %q does not exist", rawPath)
	}
	if err != nil {
		return "", fmt.Errorf("discover-manifest path %q: %w", rawPath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("discover-manifest path %q is a symlink", rawPath)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("discover-manifest path %q must be a regular file", rawPath)
	}
	return path, nil
}

func loadDiscoverManifest(rawPath, path string) (discovery.Result, error) {
	file, err := os.Open(path)
	if err != nil {
		return discovery.Result{}, fmt.Errorf("discover-manifest path %q: %w", rawPath, err)
	}
	defer func() { _ = file.Close() }()

	docs, err := manifest.DecodeDocuments(path, file)
	if err != nil {
		return discovery.Result{}, fmt.Errorf("discover-manifest path %q: %w", rawPath, err)
	}
	objects := make([]*unstructured.Unstructured, 0, len(docs))
	for _, doc := range docs {
		if doc.Object == nil {
			continue
		}
		if !isDiscoverManifestKind(doc.Object) {
			return discovery.Result{}, fmt.Errorf("discover-manifest path %q document %d is %s %s; only argoproj.io/v1alpha1 Application and ApplicationSet are supported", rawPath, doc.Index, doc.Object.GetAPIVersion(), doc.Object.GetKind())
		}
		objects = append(objects, doc.Object)
	}
	if len(objects) == 0 {
		return discovery.Result{}, fmt.Errorf("discover-manifest path %q contains no Application or ApplicationSet", rawPath)
	}
	displayPath := filepath.ToSlash(path)
	result, err := discovery.ScanObjects(displayPath, objects)
	if err != nil {
		return discovery.Result{}, fmt.Errorf("discover-manifest path %q: %w", rawPath, err)
	}
	// The file is outside the trees under analysis, so it is not a digested
	// input path and owns no repository path: changed-only selection only
	// sees the generated Applications' source paths. The render cache still
	// keys each Application on its name, namespace, and full spec, so a file
	// change that alters a generated spec re-renders it.
	for i := range result.Applications {
		result.Applications[i].Tier = discovery.SourceTierExternalManifest
		result.Applications[i].InputPaths = nil
	}
	for i := range result.ApplicationSets {
		result.ApplicationSets[i].Tier = discovery.SourceTierExternalManifest
		result.ApplicationSets[i].InputPaths = nil
	}
	return result, nil
}

func isDiscoverManifestKind(obj *unstructured.Unstructured) bool {
	gvk := obj.GroupVersionKind()
	return gvk.Group == "argoproj.io" && gvk.Version == "v1alpha1" && (gvk.Kind == "Application" || gvk.Kind == "ApplicationSet")
}

func cloneDiscoverManifestResult(input discovery.Result) discovery.Result {
	var out discovery.Result
	for _, item := range input.Applications {
		item.Application = *item.Application.DeepCopy()
		item.InputPaths = append([]string(nil), item.InputPaths...)
		out.Applications = append(out.Applications, item)
	}
	for _, item := range input.ApplicationSets {
		item.ApplicationSet = *item.ApplicationSet.DeepCopy()
		item.InputPaths = append([]string(nil), item.InputPaths...)
		out.ApplicationSets = append(out.ApplicationSets, item)
	}
	return out
}
