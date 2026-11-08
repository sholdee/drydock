package appset

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/bmatcuk/doublestar/v4"
	"github.com/sholdee/drydock/internal/diagnostic"
)

// OCIArtifactFetcher materializes the artifact an Argo CD v3.6 oci generator
// names (repoURL at revision) and returns the local directory holding its
// extracted content. The directory must stay readable until generation
// finishes. Implementations own acquisition policy: --offline, caches,
// credentials, and repo maps.
type OCIArtifactFetcher interface {
	FetchOCIArtifact(repoURL, revision string) (string, error)
}

// evaluateOciGenerator mirrors Argo CD v3.6.1
// applicationset/generators/oci.go: the shared repo-source traversal of
// repo_path_utils.go over the artifact layout the repo-server's
// GetOciDirectories/GetOciFiles report. sourceIntegrity is always nil
// upstream for OCI, so there is no signature policy to honour.
func evaluateOciGenerator(ctx generatorContext, generator argoappv1.ApplicationSetGenerator) ([]generatorParamSet, []diagnostic.Diagnostic, bool, error) {
	if ctx.Options.OCI == nil {
		return nil, unsupportedOciGeneratorDiagnostic(ctx.ManifestPath), false, nil
	}
	template, err := mergeGeneratorTemplate(ctx.BaseTemplate, generator.Oci.Template)
	if err != nil {
		return nil, nil, true, err
	}
	paramSets, diags, supported, err := ociGeneratorParamSets(ctx, generator.Oci)
	if err != nil || !supported {
		return paramSets, diags, supported, err
	}
	paramSets = setGeneratorTemplate(paramSets, template)
	paramSets, selectorDiags, err := applyGeneratorSelector(ctx.ManifestPath, generator.Selector, paramSets)
	diags = append(diags, selectorDiags...)
	return paramSets, diags, supported, err
}

// ociGeneratorParamSets dispatches like generateRepoSourceParams: directories
// win over files when both are set, and a generator with neither is reported
// the same way as an empty Git generator.
//
// Generated param sets carry no SourcePath: artifact paths are not paths in
// the repository under analysis, so they must not claim changed paths or
// feed the persistent render cache's local input digests. The generated
// Application spec, which the render cache keys on, already carries every
// artifact-derived value.
func ociGeneratorParamSets(ctx generatorContext, oci *argoappv1.OciGenerator) ([]generatorParamSet, []diagnostic.Diagnostic, bool, error) {
	if len(oci.Directories) == 0 && len(oci.Files) == 0 {
		return nil, unsupportedGeneratorDiagnostic(ctx.ManifestPath), false, nil
	}
	root, err := ctx.Options.OCI.FetchOCIArtifact(oci.RepoURL, oci.Revision)
	if err != nil {
		return nil, nil, true, fmt.Errorf("%s oci generator: %w", ctx.ManifestPath, err)
	}
	useGoTemplate := ctx.AppSet.Spec.GoTemplate
	goTemplateOptions := ctx.AppSet.Spec.GoTemplateOptions
	if len(oci.Directories) > 0 {
		paramSets, err := ociDirectoryParamSets(root, ctx.ManifestPath, oci, ctx.Options.IncludeHiddenDirectories, useGoTemplate, goTemplateOptions)
		if err != nil {
			return nil, nil, true, err
		}
		return paramSets, nil, true, nil
	}
	paramSets, diags, err := ociFileParamSets(root, ctx.ManifestPath, oci, useGoTemplate, goTemplateOptions)
	if err != nil {
		return nil, diags, true, err
	}
	return paramSets, diags, true, nil
}

func ociDirectoryParamSets(root, manifestPath string, oci *argoappv1.OciGenerator, includeHidden, useGoTemplate bool, goTemplateOptions []string) ([]generatorParamSet, error) {
	allPaths, err := walkDirectoryTree(root, includeHidden)
	if err != nil {
		return nil, fmt.Errorf("%s oci generator: list directories of %s: %w", manifestPath, oci.RepoURL, err)
	}
	requested := filterOCIDirectoryPaths(oci.Directories, allPaths)
	out := make([]generatorParamSet, 0, len(requested))
	for _, match := range requested {
		params, err := pathParams(match, oci.PathParamPrefix, oci.Values, useGoTemplate, goTemplateOptions)
		if err != nil {
			return nil, fmt.Errorf("%s render %s: %w", manifestPath, match, err)
		}
		out = append(out, generatorParamSet{Params: params, Generator: "oci-directories"})
	}
	return out, nil
}

// filterOCIDirectoryPaths is Argo CD's filterPaths (filterRepoPaths) for
// the directory items of an oci generator.
func filterOCIDirectoryPaths(dirs []argoappv1.OciDirectoryGeneratorItem, allPaths []string) []string {
	patterns := make([]repoPathPattern, 0, len(dirs))
	for _, dir := range dirs {
		patterns = append(patterns, repoPathPattern{Path: dir.Path, Exclude: dir.Exclude})
	}
	return filterRepoPaths(patterns, allPaths)
}

// ociFileParamSets mirrors generateRepoSourceFileParams: every include
// pattern's matches are unioned, every exclude pattern's matches removed, and
// the remaining files are decoded in sorted path order. Param files decode
// through the same helpers as Git files.
func ociFileParamSets(root, manifestPath string, oci *argoappv1.OciGenerator, useGoTemplate bool, goTemplateOptions []string) ([]generatorParamSet, []diagnostic.Diagnostic, error) {
	contents := map[string][]byte{}
	var excludes []string
	for _, file := range oci.Files {
		if file.Exclude {
			excludes = append(excludes, file.Path)
			continue
		}
		matched, err := ociGlobFiles(root, file.Path)
		if err != nil {
			return nil, nil, ociGeneratorError(manifestPath, err)
		}
		maps.Copy(contents, matched)
	}
	for _, pattern := range excludes {
		matched, err := ociGlobFiles(root, pattern)
		if err != nil {
			return nil, nil, ociGeneratorError(manifestPath, err)
		}
		for rel := range matched {
			delete(contents, rel)
		}
	}
	rels := make([]string, 0, len(contents))
	for rel := range contents {
		rels = append(rels, rel)
	}
	sort.Strings(rels)

	var out []generatorParamSet
	var diags []diagnostic.Diagnostic
	for _, rel := range rels {
		// A file that does not parse fails the whole ApplicationSet
		// (repo_path_utils.go:364-367).
		objects, err := decodeFileParamsContent(contents[rel])
		if err != nil {
			return nil, diags, &generatorParamsError{err: fmt.Errorf("error generating params from oci: unable to process file '%s' from repository '%s': %w", rel, oci.RepoURL, err)}
		}
		paramObjects, err := fileParamObjects(objects, rel, oci.PathParamPrefix, oci.Values, useGoTemplate, goTemplateOptions)
		if err != nil {
			return nil, diags, err
		}
		for _, params := range paramObjects {
			out = append(out, generatorParamSet{Params: params, Generator: "oci-files"})
		}
	}
	return out, diags, nil
}

// ociGeneratorError names the ApplicationSet in a drydock-side error and
// keeps a generatorParamsError as it is.
func ociGeneratorError(manifestPath string, err error) error {
	if _, ok := errors.AsType[*generatorParamsError](err); ok {
		return err
	}
	return fmt.Errorf("%s oci generator: %w", manifestPath, err)
}

// walkDirectoryTree is the repo-server's walkDirectoryTree
// (reposerver/repository/utils.go:90-121 at v3.6.1) over an extracted OCI
// artifact: every directory below root in filepath.WalkDir order, never
// through a symlink, and without hidden directories and their subtrees
// unless includeHidden (reposerver.include.hidden.directories). The root
// itself is exempt from the hidden check: Argo CD's root is a temporary
// directory.
func walkDirectoryTree(root string, includeHidden bool) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(root, func(abs string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			return nil
		}
		if !includeHidden && strings.HasPrefix(entry.Name(), ".") && abs != root {
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(root, abs)
		if err != nil || rel == "." {
			return err
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return paths, nil
}

// ociGlobFiles mirrors the Argo CD v3.6.1 repo-server GetOciFiles: an empty
// or "." pattern means "**", matching is always doublestar over an os.Root of
// the artifact (there is no new-globbing toggle for OCI), entries that do not
// stat inside the root are skipped, and directories are dropped.
func ociGlobFiles(root, pattern string) (map[string][]byte, error) {
	if pattern == "" || pattern == "." {
		pattern = "**"
	}
	artifactRoot, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("open extracted OCI artifact: %w", err)
	}
	defer artifactRoot.Close()

	matched, err := doublestar.Glob(artifactRoot.FS(), pattern)
	if err != nil {
		// The repo-server fails GetOciFiles on a bad pattern, so the
		// generator fails (reposerver/repository/repository.go:3768-3771).
		return nil, &generatorParamsError{err: fmt.Errorf("unable to match files. pattern %s: %w", pattern, err)}
	}
	out := make(map[string][]byte, len(matched))
	for _, rel := range matched {
		info, err := artifactRoot.Stat(rel)
		if err != nil {
			// A symlink resolving outside the artifact fails os.Root's stat
			// with *fs.PathError; Argo CD skips such entries.
			if _, ok := errors.AsType[*fs.PathError](err); ok {
				continue
			}
			return nil, fmt.Errorf("stat OCI file %q: %w", rel, err)
		}
		if info.IsDir() {
			continue
		}
		data, err := artifactRoot.ReadFile(rel)
		if err != nil {
			return nil, fmt.Errorf("read OCI file %q: %w", rel, err)
		}
		out[rel] = data
	}
	return out, nil
}
