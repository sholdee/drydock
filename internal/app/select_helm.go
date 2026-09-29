package app

import (
	"path"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
)

// helmLocalSelectionPaths returns the repository paths that a Helm source
// rendering from the local tree reads through helm.valueFiles and
// helm.fileParameters. $ref and env-templated entries are not local paths.
func helmLocalSelectionPaths(source argoappv1.ApplicationSource) []string {
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
		if selectionPath, ok := helmLocalInputSelectionPath(source.Path, parameter.Path, false); ok {
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
