package app

import (
	"path"
	"path/filepath"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/render"
)

// helmLocalSelectionPaths returns the repository paths that a Helm source
// rendering from the local tree at root reads through helm.valueFiles and
// helm.fileParameters. $ref and env-templated entries are not local paths.
// A file parameter path that starts with "/" is relative to the repository
// root, as in Argo CD. A file parameter also owns the symlinks that its
// path passes through and the paths they end at
// (helmFileParameterSymlinkSelectionPaths).
func helmLocalSelectionPaths(root string, source argoappv1.ApplicationSource) []string {
	if source.Helm == nil {
		return nil
	}
	var paths []string
	for _, valueFile := range source.Helm.ValueFiles {
		if selectionPath, ok := helmLocalInputSelectionPath(source.Path, valueFile, true); ok {
			paths = append(paths, selectionPath)
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
		paths = append(paths, helmFileParameterSymlinkSelectionPaths(root, source.Path, parameter.Path)...)
	}
	return paths
}

// helmFileParameterSymlinkSelectionPaths returns the repository paths that
// decide what the local file parameter path file reads when it passes
// through a symlink: each symlink on the way and the paths they end at
// (render.HelmFileParameterInputPaths). appRel is the directory that a
// relative file is relative to. It owns nothing for a path without a
// symlink, an env-templated path, a URL, or a path that does not resolve.
// Paths outside root are left out: drydock never reads them.
func helmFileParameterSymlinkSelectionPaths(root, appRel, file string) []string {
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
	inputs, err := render.HelmFileParameterInputPaths(filepath.Join(absRoot, filepath.FromSlash(appRel)), absRoot, filepath.FromSlash(file))
	if err != nil || len(inputs.Symlinks) == 0 {
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
		if sourcePlan.RefOnly || sourcePlan.Source.Helm == nil {
			continue
		}
		helm := sourcePlan.Source.Helm
		for _, valueFile := range helm.ValueFiles {
			if selectionPath, ok := helmRefInputSelectionPath(tree, plan, sourcePlan, valueFile, true); ok {
				paths = append(paths, selectionPath)
			}
		}
		for _, parameter := range helm.FileParameters {
			if selectionPath, ok := helmRefInputSelectionPath(tree, plan, sourcePlan, parameter.Path, false); ok {
				paths = append(paths, selectionPath)
				_, refPath, _ := splitHelmValueFileRef(parameter.Path)
				paths = append(paths, helmFileParameterSymlinkSelectionPaths(tree.root, "", "/"+refPath)...)
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
