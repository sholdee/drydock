package render

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/pathsafety"
	"helm.sh/helm/v4/pkg/strvals"
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
// once, with the same error, without reading its link text. The
// repo-server reads it and accepts a symlink there that points back into
// the repository. Later links in a chain are read as the repo-server reads
// them, also outside the repository, but only their link text: the result
// must still be in the repository.
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
	// file is the path after env substitution, as the errors name it.
	file string
	// url is set for a URL with an allowed scheme.
	url string
	// root is the absolute root that path is under: the repository root,
	// or the root of a $ref source.
	root string
	// path is the absolute resolved path.
	path string
	// helmPath is the path that helm --set-file reads, after it drops
	// backslashes (helmSetFileReadPath), set for the entry that helm reads.
	helmPath string
}

// helmFileParameterPath is a helm.fileParameters path before it resolves:
// the directory a relative path is relative to, the root it must stay
// under, and the path to resolve there, or a URL.
type helmFileParameterPath struct {
	// file is the path after env substitution, as the errors name it.
	file    string
	ref     bool
	url     string
	appPath string
	root    string
	path    string
}

// helmFileParameterRefName is getReferencedSourceName
// (reposerver/repository/repository.go:1620-1625 at v3.6.1): the first
// segment of a path that starts with "$", also a bare "$name".
func helmFileParameterRefName(raw string) string {
	if !strings.HasPrefix(raw, "$") {
		return ""
	}
	name, _, _ := strings.Cut(raw, "/")
	return name
}

// helmRefRootFor returns the root of the $ref source that the ref name
// names. "$$name" is an escaped "$", as Env.Envsubst reads it, and names no
// source.
func helmRefRootFor(name string, refRoots map[string]string) (string, bool) {
	trimmed := strings.TrimPrefix(name, "$")
	if trimmed == "" || trimmed == name || strings.HasPrefix(trimmed, "$") {
		return "", false
	}
	if root, ok := refRoots[name]; ok {
		return root, true
	}
	root, ok := refRoots[trimmed]
	return root, ok
}

// helmFileParameterPathFor decides, as helmTemplate does
// (reposerver/repository/repository.go:1361-1377 at v3.6.1), how the raw
// helm.fileParameters path resolves. A path whose first segment names a
// $ref source, before env substitution, resolves in the root of that
// source: the rest of the path, env-substituted, is relative to that root
// (getResolvedRefValueFile, repository.go:1583-1604). A bare "$name" or
// "$name/" resolves to that root, which then fails. Any other path is
// env-substituted, so "$$" becomes "$" and an unknown "$name" becomes
// its env value, and resolves against the source path and the repository
// root. For an Application with sources, the plan fails a "$name" that
// names no source first, as the repo-server does.
func helmFileParameterPathFor(source ResolvedSource, opts RenderOptions, raw string) (helmFileParameterPath, error) {
	if name := helmFileParameterRefName(raw); name != "" {
		if root, ok := helmRefRootFor(name, opts.RefRoots); ok {
			rest := opts.ArgoEnv.Envsubst(strings.TrimPrefix(raw, name))
			p := helmFileParameterPath{file: name + rest, ref: true, path: rest}
			if strings.TrimSpace(root) == "" {
				return p, fmt.Errorf("helm file parameter %q references unknown ref %q", p.file, name)
			}
			cleanRoot := filepath.Clean(root)
			if err := rejectHelmRefRootSymlink(cleanRoot); err != nil {
				return p, fmt.Errorf("helm file parameter %q ref root %q: %w", p.file, name, err)
			}
			p.appPath, p.root = cleanRoot, cleanRoot
			return p, nil
		}
	}
	file := opts.ArgoEnv.Envsubst(raw)
	p := helmFileParameterPath{file: file, path: file}
	if _, remote := parseRemoteHelmValueFile(file); remote {
		p.url = file
		return p, nil
	}
	appPath, root, err := helmValueFileRoots(source.RepoRoot, helmValueFilesBaseDir(source, opts), helmValueFilesBoundaryRoot(source, opts))
	if err != nil {
		return p, err
	}
	p.appPath, p.root = appPath, root
	return p, nil
}

// resolveHelmFileParameter resolves one helm.fileParameters entry as the
// Argo CD v3.6.1 repo-server does before it runs helm
// (helmFileParameterPathFor, resolveArgoValueFilePath). It runs for every
// entry, also one that a later entry with the same name overrides, so such
// an entry still fails when its path does not resolve. The errors have the
// repo-server's text. For the entry that helm reads (read), it also finds
// the path that helm --set-file reads (helmSetFileReadPath). The reader
// checks that path only when helm reads it, as helm would fail only then.
func resolveHelmFileParameter(source ResolvedSource, opts RenderOptions, parameter argoappv1.HelmFileParameter, read bool) (helmFileParameterTarget, error) {
	p, err := helmFileParameterPathFor(source, opts, parameter.Path)
	target := helmFileParameterTarget{file: p.file}
	if err != nil {
		return target, err
	}
	if p.url != "" {
		parsed, _ := parseRemoteHelmValueFile(p.url)
		if !helmValueFileSchemeAllowed(parsed.Scheme, opts) {
			return target, fmt.Errorf("error resolving helm value file path: the URL scheme '%s' is not allowed", parsed.Scheme)
		}
		target.url = p.url
		return target, nil
	}
	resolved, err := resolveArgoValueFilePath(p.appPath, p.root, p.path)
	if err != nil {
		if p.ref {
			return target, fmt.Errorf("error resolving set-file path: error resolving value file path: %w", err)
		}
		return target, fmt.Errorf("error resolving helm value file path: %w", err)
	}
	target.path = resolved
	if target.root, err = filepath.Abs(p.root); err != nil {
		return target, err
	}
	if !read {
		return target, nil
	}
	target.helmPath = helmSetFileReadPath(parameter.Name, target.root, resolved)
	return target, nil
}

// helmFileParameterSetValue is the file that drydock passes to --set-file
// for the file resolved at path under root. The repo-server passes the
// absolute resolved path. drydock passes it relative to the root, with a
// leading "/", so a comma or a backslash in the path of drydock's checkout
// cannot change how helm splits the value.
func helmFileParameterSetValue(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return "/" + filepath.ToSlash(rel)
}

// helmSetFileReadPath returns the path that helm --set-file reads for the
// file parameter name resolved at path under root. helm parses
// name=<path> with strvals, which drops a backslash before any character,
// so the file it reads can differ from the resolved path
// (cleanHelmSetParameter, helm pkg/cli/values/options.go:94-106). When the
// expression does not parse, it returns path: the render then fails with
// the parse error.
func helmSetFileReadPath(name, root, path string) string {
	value := helmFileParameterSetValue(root, path)
	var parsed string
	capture := func(input []rune) (any, error) {
		parsed = string(input)
		return "", nil
	}
	if err := strvals.ParseIntoFile(name+"="+cleanHelmSetParameter(value), map[string]any{}, capture); err != nil || !strings.HasPrefix(parsed, "/") {
		return path
	}
	return filepath.Join(root, filepath.FromSlash(parsed))
}

// confineHelmFileParameterRead returns the real path of path, the file that
// helm --set-file reads for file, and fails when that real path is outside
// root or in .git. The repo-server reads such a file through a symlinked
// directory, but its out-of-bounds symlink check fails the repository first
// unless reposerver.allow.oob.symlinks is true.
func confineHelmFileParameterRead(root, path, file string) (string, error) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	realPath, err := pathsafety.ResolveForContainment(path)
	if err != nil {
		return "", fmt.Errorf("read helm file parameter %q: %w", file, err)
	}
	rel, err := filepath.Rel(realRoot, realPath)
	if err != nil || filepath.IsAbs(rel) || pathsafety.RelEscapes(rel) {
		return "", fmt.Errorf("helm file parameter %q makes helm read a file outside the repository, which drydock never reads", file)
	}
	if pathsafety.RelEntersGit(rel) {
		return "", gitPathRefError("helm file parameter", file)
	}
	return realPath, nil
}

// HelmFileParameterInputs are the paths that decide how the repo-server
// resolves one local helm.fileParameters path and what helm then reads.
// Every path is absolute, with the symlinks of the root resolved, and can be
// outside the root.
type HelmFileParameterInputs struct {
	// Symlinks are the symlinks that the path and the file helm reads pass
	// through, a symlinked directory included.
	Symlinks []string
	// Read is where the path that helm --set-file reads ends. It is empty
	// when that path cannot be resolved, such as in a symlink loop.
	Read string
	// Written is where the path as written ends, when it differs from
	// Read. It may be missing.
	Written string
}

// HelmFileParameterInputPaths returns the inputs of the local
// helm.fileParameters entry name with path file. appPath is the directory
// that a relative file is relative to, and root is the root that it must
// stay under. It fails when the path does not resolve, as the render does.
func HelmFileParameterInputPaths(name, appPath, root, file string) (HelmFileParameterInputs, error) {
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
		// The trace starts at the real root, so it never lists the
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
	if inputs.Read, err = trace(helmSetFileReadPath(name, absRoot, resolved)); err != nil {
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
