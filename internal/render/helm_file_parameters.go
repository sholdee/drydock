package render

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sholdee/drydock/internal/pathsafety"
)

// argoResolveFailure is the text of resolveFailure (util/io/path/
// resolved.go:67-73 at v3.6.1). The repo-server hides the path and the
// cause and only logs them. drydock adds the cause after this text.
const argoResolveFailure = "internal error: failed to resolve path. Check logs for more details"

// resolveArgoSymlinkRecursive is resolveSymbolicLinkRecursive (util/io/path/
// resolved.go:22-48 at v3.6.1). When path itself is a symlink, it follows
// it up to maxDepth levels and joins a relative target to the directory of
// the symlink as text. A symlinked directory in the middle of path is not
// resolved. A path that is not a symlink, or does not exist, is returned
// unchanged, so a dangling symlink resolves to its missing target.
func resolveArgoSymlinkRecursive(path string, maxDepth int) (string, error) {
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
	return resolveArgoSymlinkRecursive(target, maxDepth-1)
}

// resolveArgoValueFilePath is pathutil.ResolveValueFilePathOrUrl for a path
// that is not a URL (util/io/path/resolved.go:112-187 at v3.6.1). A relative
// file is joined to appPath, and an absolute file to repoRoot. When the
// joined path is a symlink, it is followed with resolveArgoSymlinkRecursive.
// The result must be under repoRoot as text and must not be repoRoot
// itself. The errors have the repo-server's text.
//
// drydock differs in one point: a joined path outside repoRoot fails at
// once, with the same error, because drydock does not read link text
// outside the repository. The repo-server would read it and accept a
// symlink there that points back into the repository.
func resolveArgoValueFilePath(appPath, repoRoot, file string) (string, error) {
	absRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return "", fmt.Errorf("%s: %w", argoResolveFailure, err)
	}
	path := file
	if !filepath.IsAbs(path) {
		absApp, err := filepath.Abs(appPath)
		if err != nil {
			return "", fmt.Errorf("%s: %w", argoResolveFailure, err)
		}
		path = filepath.Join(absApp, path)
	} else {
		path = filepath.Join(absRoot, path)
	}
	requiredRoot := absRoot
	if !strings.HasSuffix(requiredRoot, string(os.PathSeparator)) {
		requiredRoot += string(os.PathSeparator)
	}
	outside := fmt.Errorf("file '%s' resolved to outside repository root", file)
	if path+string(os.PathSeparator) != requiredRoot && !strings.HasPrefix(path, requiredRoot) {
		return "", outside
	}
	delinked, err := resolveArgoSymlinkRecursive(path, 10)
	if err != nil {
		return "", fmt.Errorf("%s: %w", argoResolveFailure, err)
	}
	path, err = filepath.Abs(delinked)
	if err != nil {
		return "", fmt.Errorf("%s: %w", argoResolveFailure, err)
	}
	if path+string(os.PathSeparator) == requiredRoot {
		return "", fmt.Errorf("%s: path resolved to repository root, which is not allowed", argoResolveFailure)
	}
	if !strings.HasPrefix(path, requiredRoot) {
		return "", outside
	}
	return path, nil
}

// helmFileParameterTarget is where the repo-server resolves the path of one
// helm.fileParameters entry, which it passes to helm --set-file.
type helmFileParameterTarget struct {
	// file is the path after env substitution.
	file string
	// url is set for a URL with an allowed scheme.
	url string
	// root is the absolute root that path is under: the repository root,
	// or the root of a $ref source.
	root string
	// path is the absolute resolved path.
	path string
}

// resolveHelmFileParameter resolves the path of one helm.fileParameters
// entry as the Argo CD v3.6.1 repo-server does before it runs helm
// (helmTemplate, reposerver/repository/repository.go:1361-1377). A $ref
// path resolves in the root of the ref source with getResolvedRefValueFile
// (repository.go:1583-1604). Any other path resolves with
// ResolveValueFilePathOrUrl against the source path and the repository
// root (resolveArgoValueFilePath). It runs for every entry, also one that a
// later entry with the same name overrides, so such an entry still fails
// when its path does not resolve. The errors have the repo-server's text.
// drydock also rejects a path in a .git directory.
func resolveHelmFileParameter(source ResolvedSource, opts RenderOptions, rawPath string) (helmFileParameterTarget, error) {
	file := envsubstHelmValueFilePath(rawPath, opts, opts.RefRoots)
	target := helmFileParameterTarget{file: file}
	appPath, root, path, local, err := helmFileParameterRoots(source, opts, file)
	if err != nil {
		return target, err
	}
	if !local {
		parsed, _ := parseRemoteHelmValueFile(file)
		if !helmValueFileSchemeAllowed(parsed.Scheme, opts) {
			return target, fmt.Errorf("error resolving helm value file path: the URL scheme '%s' is not allowed", parsed.Scheme)
		}
		target.url = file
		return target, nil
	}
	resolved, err := resolveArgoValueFilePath(appPath, root, path)
	if err != nil {
		if strings.HasPrefix(file, "$") {
			return target, fmt.Errorf("error resolving set-file path: error resolving value file path: %w", err)
		}
		return target, fmt.Errorf("error resolving helm value file path: %w", err)
	}
	target.path = resolved
	if target.root, err = filepath.Abs(root); err != nil {
		return target, err
	}
	if pathEntersGit(target.root, target.path) {
		return target, gitPathRefError("helm file parameter", file)
	}
	return target, nil
}

// readHelmFileParameter reads the file at path, a path that helm --set-file
// received for a file parameter that resolved under root. drydock reads only
// a file whose real path is inside root and outside .git. A symlinked
// directory on the way can lead elsewhere: the repo-server follows it, but
// its out-of-bounds symlink check fails such a repository unless
// reposerver.allow.oob.symlinks is true.
func readHelmFileParameter(root, path, file string) ([]byte, error) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	realPath, err := pathsafety.ResolveForContainment(path)
	if err != nil {
		return nil, fmt.Errorf("read helm file parameter %q: %w", file, err)
	}
	rel, err := filepath.Rel(realRoot, realPath)
	if err != nil || filepath.IsAbs(rel) || pathsafety.RelEscapes(rel) {
		return nil, fmt.Errorf("helm file parameter %q resolves to a file outside the repository through a symlinked directory, which drydock never reads", file)
	}
	if pathsafety.RelEntersGit(rel) {
		return nil, gitPathRefError("helm file parameter", file)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read helm file parameter %q: %w", file, err)
	}
	return data, nil
}

// HelmFileParameterInputs are the paths that decide how the repo-server
// resolves one local helm.fileParameters path and what helm then reads.
// Every path is absolute, with the symlinks of the root resolved, and can be
// outside the root.
type HelmFileParameterInputs struct {
	// Symlinks are the symlinks that the path and its resolved path pass
	// through, a symlinked directory included.
	Symlinks []string
	// Read is where the resolved path ends, the file helm reads. It is
	// empty when the path cannot be resolved, such as in a symlink loop.
	Read string
	// Written is where the path as written ends, when it differs from
	// Read. It may be missing.
	Written string
}

// HelmFileParameterInputPaths returns the inputs of the local
// helm.fileParameters path file. appPath is the directory that a relative
// file is relative to, and root is the root that it must stay under. It
// fails when the path does not resolve, as the render does.
func HelmFileParameterInputPaths(appPath, root, file string) (HelmFileParameterInputs, error) {
	var inputs HelmFileParameterInputs
	resolved, err := resolveArgoValueFilePath(appPath, root, file)
	if err != nil {
		return inputs, err
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return inputs, err
	}
	realRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return inputs, err
	}
	joined := filepath.Join(absRoot, file)
	if !filepath.IsAbs(file) {
		absApp, err := filepath.Abs(appPath)
		if err != nil {
			return inputs, err
		}
		joined = filepath.Join(absApp, file)
	}
	seen := map[string]struct{}{}
	trace := func(path string) (string, error) {
		// resolveArgoValueFilePath checked that both paths are under absRoot
		// as text, so the trace starts at the real root and never lists the
		// symlinks above it.
		rel, err := filepath.Rel(absRoot, path)
		if err != nil || pathsafety.RelEscapes(rel) {
			return "", fmt.Errorf("file '%s' resolved to outside repository root", file)
		}
		visited, end := traceSymlinkPaths(filepath.Join(realRoot, rel))
		for _, link := range visited {
			if _, ok := seen[link]; !ok {
				seen[link] = struct{}{}
				inputs.Symlinks = append(inputs.Symlinks, link)
			}
		}
		return end, nil
	}
	if inputs.Read, err = trace(resolved); err != nil {
		return inputs, err
	}
	written, err := trace(joined)
	if err != nil {
		return inputs, err
	}
	if written != inputs.Read {
		inputs.Written = written
	}
	return inputs, nil
}

// helmFileParameterRoots returns the directory that the helm.fileParameters
// path file is relative to, the root it must stay under, and the path to
// resolve there, as resolveHelmFileParameter resolves it. ok is false for a
// URL.
func helmFileParameterRoots(source ResolvedSource, opts RenderOptions, file string) (appPath, root, path string, ok bool, err error) {
	if strings.HasPrefix(file, "$") {
		root, refPath, err := helmValueFileRefRoot(file, opts.RefRoots)
		if err != nil {
			return "", "", "", false, err
		}
		return root, root, refPath, true, nil
	}
	if _, remote := parseRemoteHelmValueFile(file); remote {
		return "", "", "", false, nil
	}
	appPath, root, err = helmValueFileRoots(source.RepoRoot, helmValueFilesBaseDir(source, opts), helmValueFilesBoundaryRoot(source, opts))
	if err != nil {
		return "", "", "", false, err
	}
	return appPath, root, file, true, nil
}
