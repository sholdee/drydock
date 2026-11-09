package render

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/sholdee/drydock/internal/gitref"
	"github.com/sholdee/drydock/internal/pathsafety"
)

// DirectoryInputDigestPaths returns conservative committed repository paths for
// directory rendering. The source directory tree is always included. For a
// source with a Jsonnet file, each library is included as the renderer
// resolves it, with the library path as written and the symlinks on the way
// (DirectoryJsonnetLibInputs). The paths outside those trees that
// their symlinks read are included too (DirectorySymlinkInputPaths), as
// optional paths: a symlink whose target is missing reads nothing now but
// renders the target once it exists. So are the files that its Jsonnet
// files and the code values of its extVars and TLAs import from outside
// those trees (DirectoryJsonnetImportPaths). A code value is substituted
// with opts.ArgoEnv, so the caller must pass the build environment of the
// render. When
// argocd-cm jsonnet.enable turns Jsonnet off, the renderer evaluates no
// Jsonnet file, so the libraries and the imports are not inputs.
func DirectoryInputDigestPaths(ctx context.Context, source ResolvedSource, opts RenderOptions) ([]gitref.PathDigestPath, error) {
	root, err := sourceRoot(source)
	if err != nil {
		return nil, err
	}
	sourceRel, err := relativeDigestPath(source.RepoRoot, root)
	if err != nil {
		return nil, err
	}
	paths := []gitref.PathDigestPath{{Path: sourceRel}}
	if opts.DirectoryJsonnetDisabled {
		for _, rel := range DirectorySymlinkInputPaths(source.RepoRoot, []string{sourceRel}) {
			paths = append(paths, gitref.PathDigestPath{Path: rel, Optional: true})
		}
		return paths, nil
	}
	// The libraries count only for a source with a Jsonnet file. A library
	// need not exist: the repo-server accepts a missing one.
	libDirs, libLinks, err := DirectoryJsonnetLibInputs(source.RepoRoot, source.Path, opts.Jsonnet.Libs)
	if err != nil {
		return nil, err
	}
	for _, rel := range libDirs {
		paths = append(paths, gitref.PathDigestPath{Path: rel, Optional: true})
	}
	for _, rel := range libLinks {
		paths = append(paths, gitref.PathDigestPath{Path: rel, Optional: true})
	}
	trees := append([]string{sourceRel}, libDirs...)
	for _, rel := range DirectorySymlinkInputPaths(source.RepoRoot, trees) {
		paths = append(paths, gitref.PathDigestPath{Path: rel, Optional: true})
	}
	code := JsonnetCodeValues(opts.Jsonnet, opts.ArgoEnv)
	imports, err := DirectoryJsonnetImportPaths(ctx, source.RepoRoot, source.Path, opts.Jsonnet.Libs, code)
	if err != nil {
		return nil, err
	}
	for _, rel := range imports {
		paths = append(paths, gitref.PathDigestPath{Path: rel, Optional: true})
	}
	return paths, nil
}

func relativeDigestPath(repoRoot, path string) (string, error) {
	rel, err := filepath.Rel(filepath.Clean(repoRoot), filepath.Clean(path))
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(rel) || pathsafety.RelEscapes(rel) {
		return "", fmt.Errorf("digest path %q escapes repository root %q", path, repoRoot)
	}
	return filepath.ToSlash(rel), nil
}
