package render

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/sholdee/drydock/internal/pathsafety"
)

// HelmValueFileReads returns the repository-relative local files that the
// Helm renderer reads for opts.ValueFiles of source. It resolves each entry
// with the same steps as helmValueFileLoader: env substitution with
// opts.ArgoEnv, $ref roots from opts.RefRoots, the value files base and
// boundary of source, and doublestar globs. With refOnly, it reads only $ref
// entries: the other entries of a chart from a chart repository are inside
// that chart. It skips remote files, files that do not exist, files that
// the renderer would reject, and files outside repoRoot. It never fails, so
// a caller gets the files it can tell.
func HelmValueFileReads(repoRoot string, source ResolvedSource, opts RenderOptions, refOnly bool) []string {
	repoRoot = filepath.Clean(repoRoot)
	if source.RepoRoot == "" {
		source.RepoRoot = repoRoot
	}
	reads := map[string]struct{}{}
	if argoHelmValueFiles(opts) {
		addArgoHelmValueFileReads(reads, repoRoot, source, opts, refOnly)
		return sortedKeys(reads)
	}
	for _, raw := range opts.ValueFiles {
		file := envsubstHelmValueFilePath(raw, opts, opts.RefRoots)
		if isRemoteHelmValueFile(file) || (refOnly && !strings.HasPrefix(file, "$")) {
			continue
		}
		root, resolved, err := resolveHelmValueFile(source.RepoRoot, helmValueFilesBaseDir(source, opts), helmValueFilesBoundaryRoot(source, opts), opts.RefRoots, file)
		if err != nil {
			continue
		}
		matches := []string{resolved}
		if hasHelmValueGlob(file) {
			if matches, err = doublestar.FilepathGlob(resolved); err != nil {
				continue
			}
		}
		for _, match := range matches {
			if pathEntersGit(root, match) || rejectSymlinkedPath(root, match) != nil {
				continue
			}
			addRegularRepoFile(reads, repoRoot, match)
		}
	}
	return sortedKeys(reads)
}

// addArgoHelmValueFileReads adds the local files that the Argo CD resolver
// reads for opts.ValueFiles (planArgo): the real path of each file that
// helm reads, when it is a regular file in repoRoot.
func addArgoHelmValueFileReads(reads map[string]struct{}, repoRoot string, source ResolvedSource, opts RenderOptions, refOnly bool) {
	realRepoRoot, err := filepath.EvalSymlinks(repoRoot)
	if err != nil {
		return
	}
	loader := newHelmValueFileLoader(context.Background(), source.RepoRoot, helmValueFilesBaseDir(source, opts), helmValueFilesBoundaryRoot(source, opts), opts.RefRoots, opts.IgnoreMissingValueFiles, opts, nil)
	for _, raw := range opts.ValueFiles {
		file := envsubstHelmValueFilePath(raw, opts, opts.RefRoots)
		if isRemoteHelmValueFile(file) || (refOnly && !strings.HasPrefix(file, "$")) {
			continue
		}
		plans, err := loader.planArgo([]string{raw})
		if err != nil {
			continue
		}
		for _, planned := range plans {
			if rel, ok := argoHelmValueFileRead(realRepoRoot, planned); ok {
				reads[rel] = struct{}{}
			}
		}
	}
}

// argoHelmValueFileRead returns the path, relative to realRepoRoot, of the
// regular local file that helm reads for planned.
func argoHelmValueFileRead(realRepoRoot string, planned plannedHelmValueFile) (string, bool) {
	if !planned.argo {
		return "", false
	}
	realPath, err := confineHelmInputRead("helm value file", planned.root, planned.path, planned.display)
	if err != nil {
		return "", false
	}
	info, err := os.Lstat(realPath)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	rel, err := filepath.Rel(realRepoRoot, realPath)
	if err != nil || filepath.IsAbs(rel) || pathsafety.RelEscapes(rel) || pathsafety.RelEntersGit(rel) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// HelmChartValuesFiles returns the repository-relative values.yaml files
// that Helm reads as defaults when it loads the local chart in chartDir: the
// chart's own values.yaml and the values.yaml of each unpacked subchart
// under charts/, at any depth. Packed subcharts (.tgz) have no local values
// file.
func HelmChartValuesFiles(repoRoot, chartDir string) []string {
	repoRoot = filepath.Clean(repoRoot)
	reads := map[string]struct{}{}
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if depth > 16 {
			return
		}
		addRegularRepoFile(reads, repoRoot, filepath.Join(dir, "values.yaml"))
		entries, err := os.ReadDir(filepath.Join(dir, "charts"))
		if err != nil {
			return
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			sub := filepath.Join(dir, "charts", entry.Name())
			if info, err := os.Lstat(filepath.Join(sub, "Chart.yaml")); err == nil && info.Mode().IsRegular() {
				walk(sub, depth+1)
			}
		}
	}
	walk(filepath.Clean(chartDir), 0)
	return sortedKeys(reads)
}

func addRegularRepoFile(reads map[string]struct{}, repoRoot, path string) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return
	}
	if rejectPathOutsideBoundary("helm value file", path, repoRoot) != nil {
		return
	}
	rel, err := relativeManifestPath(repoRoot, path)
	if err != nil {
		return
	}
	reads[filepath.ToSlash(rel)] = struct{}{}
}

func sortedKeys(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
