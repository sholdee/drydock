package app

import (
	"os"
	"path"
	"path/filepath"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/bmatcuk/doublestar/v4"
	"github.com/sholdee/drydock/internal/render"
)

// helmLocalSelectionPaths returns the repository paths that a Helm source
// rendering from the local tree at root reads through helm.valueFiles and
// helm.fileParameters. $ref and env-templated entries are not local paths.
// A value file or file parameter path that starts with "/" is relative to
// the repository root, as in Argo CD. Each also owns the symlinks that its
// path passes through and the paths they end at
// (helmFileParameterSymlinkSelectionPaths, helmValueFileSymlinkSelectionPaths).
func helmLocalSelectionPaths(root string, globs helmGlobMemo, source argoappv1.ApplicationSource) []string {
	if source.Helm == nil {
		return nil
	}
	var paths []string
	for _, valueFile := range source.Helm.ValueFiles {
		sourcePath, file := source.Path, valueFile
		if strings.HasPrefix(file, "/") {
			sourcePath, file = "", strings.TrimLeft(file, "/")
		}
		if selectionPath, ok := helmLocalInputSelectionPath(sourcePath, file, true); ok {
			paths = append(paths, selectionPath)
		}
		if helmInputPathHasGlob(valueFile) {
			paths = append(paths, helmValueFileGlobSymlinkSelectionPaths(globs, root, source.Path, valueFile)...)
		} else {
			paths = append(paths, helmValueFileSymlinkSelectionPaths(root, source.Path, valueFile)...)
		}
	}
	for _, parameter := range source.Helm.FileParameters {
		sourcePath, file := source.Path, parameter.Path
		if strings.HasPrefix(file, "/") {
			sourcePath, file = "", strings.TrimLeft(file, "/")
		}
		if selectionPath, ok := helmLocalInputSelectionPath(sourcePath, file, false); ok {
			paths = append(paths, selectionPath)
		}
		paths = append(paths, helmFileParameterSymlinkSelectionPaths(root, source.Path, parameter.Name, parameter.Path)...)
	}
	return paths
}

// helmFileParameterSymlinkSelectionPaths returns the repository paths that
// decide what the local file parameter name with path file reads when that
// is not the path as written: each symlink on the way and the paths they
// end at, and the file helm reads after it drops backslashes
// (render.HelmFileParameterInputPaths). appRel is the directory that a
// relative file is relative to. It owns nothing more for a plain path, an
// env-templated path, a URL, or a path that does not resolve. Paths outside
// root are left out: drydock never reads them.
func helmFileParameterSymlinkSelectionPaths(root, appRel, name, file string) []string {
	return helmInputSymlinkSelectionPaths(root, appRel, file, func(appPath, absRoot, file string) (render.HelmFileParameterInputs, error) {
		return render.HelmFileParameterInputPaths(name, appPath, absRoot, file)
	})
}

// helmValueFileSymlinkSelectionPaths returns the repository paths that
// decide what the local value file path file reads when that is not the
// path as written: each symlink on the way and the paths they end at
// (render.HelmValueFileInputPaths). It owns nothing more for a plain path,
// an env-templated path, a URL, or a path that does not resolve.
func helmValueFileSymlinkSelectionPaths(root, appRel, file string) []string {
	return helmInputSymlinkSelectionPaths(root, appRel, file, render.HelmValueFileInputPaths)
}

// helmGlobMemo holds the paths that each value file glob owns on one tree,
// by root and pattern, so a glob that many Applications name expands once
// per diff side.
type helmGlobMemo map[string][]string

// helmValueFileGlobSymlinkSelectionPaths expands the local value file glob
// file, relative to appRel or, with a leading "/", to root, on the tree at
// root, as the render expands it with doublestar. A backslash escapes the
// next character, as in doublestar. Each match owns the symlinks on its way
// and the file where it ends, as a value file written as that path does
// (helmValueFileSymlinkSelectionPaths). The static directory of the glob is
// owned elsewhere (helmLocalInputSelectionPath). memo, when set, keeps the
// result.
func helmValueFileGlobSymlinkSelectionPaths(memo helmGlobMemo, root, appRel, file string) []string {
	if strings.TrimSpace(file) == "" || strings.Contains(file, "$") || strings.Contains(file, "://") {
		return nil
	}
	pattern := path.Join(normalizeSelectPath(appRel), file)
	if strings.HasPrefix(file, "/") {
		pattern = path.Clean(strings.TrimLeft(file, "/"))
	}
	if pattern == "." || pattern == ".." || strings.HasPrefix(pattern, "../") || path.IsAbs(pattern) {
		return nil
	}
	key := root + "\x00" + pattern
	if paths, ok := memo[key]; ok {
		return paths
	}
	matches, err := doublestar.Glob(os.DirFS(root), pattern)
	if err != nil {
		return nil
	}
	var paths []string
	for _, match := range matches {
		paths = append(paths, helmValueFileSymlinkSelectionPaths(root, "", "/"+match)...)
	}
	if memo != nil {
		memo[key] = paths
	}
	return paths
}

// helmInputSymlinkSelectionPaths returns the symlinks and ends that inputs
// reports for the local path file, relative to appRel under root, as
// repository paths. Paths outside root are left out: drydock never reads
// them.
func helmInputSymlinkSelectionPaths(root, appRel, file string, inputPaths func(appPath, absRoot, file string) (render.HelmFileParameterInputs, error)) []string {
	if strings.TrimSpace(file) == "" || strings.Contains(file, "$") || strings.Contains(file, "://") {
		return nil
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil
	}
	realRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return nil
	}
	inputs, err := inputPaths(filepath.Join(absRoot, filepath.FromSlash(appRel)), absRoot, filepath.FromSlash(file))
	if err != nil || (len(inputs.Symlinks) == 0 && inputs.Written == "") {
		return nil
	}
	var paths []string
	for _, input := range append(append([]string(nil), inputs.Symlinks...), inputs.Read, inputs.Written) {
		if input == "" {
			continue
		}
		rel, err := filepath.Rel(realRoot, input)
		if err != nil {
			continue
		}
		if selectionPath, ok := cleanSelectionRelativePath(filepath.ToSlash(rel)); ok {
			paths = append(paths, selectionPath)
		}
	}
	return paths
}

// helmRefSelectionPaths returns the repository paths that application reads
// through $ref value files and file parameters whose ref root is this tree.
// Ref roots come from renderRefsForSource, so selection judges the source the
// provider resolves: a ref naming the consuming source's own repository
// revision reads that source's root, and any other ref drops its own path
// and borrows the path of a same-revision path source, if any. An
// Application that fails to plan fails to render, so it owns nothing here.
func helmRefSelectionPaths(tree *selectionTree, application argoappv1.Application) []string {
	plan, err := Plan(application)
	if err != nil {
		return nil
	}
	var paths []string
	for _, sourcePlan := range plan.Sources {
		if sourcePlan.RefOnly {
			continue
		}
		if sourceRendersFromRoot(tree, application, sourcePlan.Source) {
			sourcePlan.Source = selectionSourceWithOverrides(tree, application, sourcePlan.Source)
		}
		if sourcePlan.Source.Helm == nil {
			continue
		}
		helm := sourcePlan.Source.Helm
		for _, valueFile := range helm.ValueFiles {
			if selectionPath, ok := helmRefInputSelectionPath(tree, plan, sourcePlan, valueFile, true); ok {
				paths = append(paths, selectionPath)
				_, refPath, _ := splitHelmValueFileRef(valueFile)
				if helmInputPathHasGlob(refPath) {
					paths = append(paths, helmValueFileGlobSymlinkSelectionPaths(tree.helmGlobs, tree.root, "", "/"+refPath)...)
				} else {
					paths = append(paths, helmValueFileSymlinkSelectionPaths(tree.root, "", "/"+refPath)...)
				}
			}
		}
		for _, parameter := range helm.FileParameters {
			if selectionPath, ok := helmRefInputSelectionPath(tree, plan, sourcePlan, parameter.Path, false); ok {
				paths = append(paths, selectionPath)
				_, refPath, _ := splitHelmValueFileRef(parameter.Path)
				paths = append(paths, helmFileParameterSymlinkSelectionPaths(tree.root, "", parameter.Name, "/"+refPath)...)
			}
		}
	}
	return paths
}

// helmRefInputSelectionPath mirrors resolveHelmValueFile for one $ref/path
// entry whose ref root renders from root: the path resolves under the ref
// root, which is then the repository root, and must not escape it. Globs
// and env-templated paths follow helmLocalInputSelectionPath.
func helmRefInputSelectionPath(tree *selectionTree, plan PlanResult, sourcePlan SourcePlan, entry string, glob bool) (string, bool) {
	if !strings.HasPrefix(entry, "$") {
		return "", false
	}
	refKey, refPath, ok := splitHelmValueFileRef(entry)
	if !ok {
		return "", false
	}
	refRoots, refSources, err := renderRefsForSource(plan, sourcePlan, []string{entry})
	if err != nil {
		return "", false
	}
	rootSource, ok := refSources[refKey]
	if _, sameRevision := refRoots[refKey]; sameRevision {
		rootSource, ok = resolvedSourceForPlan(sourcePlan), true
	}
	if !ok || !refSourceRendersFromRoot(tree, rootSource) {
		return "", false
	}
	return helmLocalInputSelectionPath("", refPath, glob)
}

// helmLocalInputSelectionPath mirrors resolveHelmValueFile for a local
// value file or file parameter: relative to spec.source.path, allowed to
// escape it but not the repository root. A value-file glob owns its static
// directory; file parameters never glob at render.
func helmLocalInputSelectionPath(sourcePath, ref string, glob bool) (string, bool) {
	// Rendering env-substitutes any entry containing $, so its path is not
	// known here.
	if strings.TrimSpace(ref) == "" || strings.Contains(ref, "://") || strings.Contains(ref, "$") {
		return "", false
	}
	ref = strings.ReplaceAll(ref, "\\", "/")
	if strings.HasPrefix(ref, "/") {
		return "", false
	}
	selectionPath := path.Join(normalizeSelectPath(sourcePath), ref)
	if glob && helmInputPathHasGlob(ref) {
		selectionPath = globStaticPrefix(selectionPath)
	}
	// An empty glob prefix would intersect every changed path and suppress
	// the unowned fallback repo-wide; like an escape, it owns nothing.
	return cleanSelectionRelativePath(selectionPath)
}

// globStaticPrefix returns the leading path segments of pattern that hold no
// glob metacharacter. Value-file globs expand with doublestar, which also
// treats {a,b} as an alternation.
func globStaticPrefix(pattern string) string {
	segments := strings.Split(pattern, "/")
	for i, segment := range segments {
		if strings.ContainsAny(segment, "*?[{") {
			return strings.Join(segments[:i], "/")
		}
	}
	return pattern
}

// selectionSourceWithOverrides returns source with the .argocd-source.yaml
// and .argocd-source-<app>.yaml files of its path under root merged in, as
// PrepareSource merges them before the render and the persistent cache key
// reads the merged source. So an Application owns the value files, file
// parameters, and other inputs that only an override file names. The
// instance name in the file name uses the controller namespace of tree, as
// the render does. When the files cannot be read or merged, it returns
// source: the render then fails, and the Application still owns its path,
// which holds the override files.
func selectionSourceWithOverrides(tree *selectionTree, application argoappv1.Application, source argoappv1.ApplicationSource) argoappv1.ApplicationSource {
	root := tree.root
	if strings.TrimSpace(source.Path) == "" {
		return source
	}
	sourcePath, err := cleanLocalSourcePath(source.Path)
	if err != nil {
		return source
	}
	controllerNamespace := tree.controllerNamespace
	if controllerNamespace == "" {
		controllerNamespace = defaultArgoCDControllerNamespace
	}
	merged, err := mergeArgocdSourceOverrides(source, root, filepath.Join(root, sourcePath), application.InstanceName(controllerNamespace))
	if err != nil {
		return source
	}
	return merged
}
