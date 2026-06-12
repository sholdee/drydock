package render

import (
	"fmt"
	"path/filepath"

	"github.com/sholdee/drydock/internal/gitref"
	"github.com/sholdee/drydock/internal/pathsafety"
)

// DirectoryInputDigestPaths returns conservative committed repository paths for
// directory rendering. The source directory tree is always included; Jsonnet
// library roots are included when declared and validated with the same bounded
// path semantics used by the renderer.
func DirectoryInputDigestPaths(source ResolvedSource, opts RenderOptions) ([]gitref.PathDigestPath, error) {
	root, err := sourceRoot(source)
	if err != nil {
		return nil, err
	}
	sourceRel, err := relativeDigestPath(source.RepoRoot, root)
	if err != nil {
		return nil, err
	}
	paths := []gitref.PathDigestPath{{Path: sourceRel}}
	for _, lib := range opts.Jsonnet.Libs {
		resolved, err := resolveJsonnetLib(source.RepoRoot, lib)
		if err != nil {
			return nil, err
		}
		libRel, err := relativeDigestPath(source.RepoRoot, resolved)
		if err != nil {
			return nil, err
		}
		paths = append(paths, gitref.PathDigestPath{Path: libRel})
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
