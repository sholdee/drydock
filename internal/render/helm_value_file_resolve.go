package render

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/sholdee/drydock/internal/pathsafety"
	"github.com/sholdee/drydock/internal/remote"
)

// argoHelmValueFiles reports whether the value files of opts resolve as the
// Argo CD v3.6.1 repo-server resolves helm.valueFiles
// (getResolvedValueFiles). The value files of a Kustomize helmCharts entry
// (LiteralValueFilePaths) follow kustomize and keep drydock's own resolver.
func argoHelmValueFiles(opts RenderOptions) bool {
	return !opts.LiteralValueFilePaths
}

// argoHelmValueFile is one helm.valueFiles entry after the repo-server
// resolves its path: a URL, or an absolute path under root. path can hold
// glob characters.
type argoHelmValueFile struct {
	// file is the path after env substitution, as the errors name it.
	file string
	url  string
	// root is the absolute root that path must stay under: the repository
	// root, or the root of a $ref source.
	root string
	path string
}

// resolveArgoHelmValueFile resolves the path of one raw value file as
// getResolvedValueFiles does (reposerver/repository/repository.go:1468-1604
// at v3.6.1): helmArgoInputPathFor decides the root and the path, and
// pathutil.ResolveValueFilePathOrUrl (resolveArgoValueFilePath) resolves
// it. A path that starts with "/" is relative to the repository root, and
// a symlink is followed when the path itself is one. The errors have the
// repo-server's text. drydock also fails a resolved path in .git, which it
// never reads.
func (l *helmValueFileLoader) resolveArgoHelmValueFile(raw string) (argoHelmValueFile, error) {
	p, err := helmArgoInputPathFor(l.repoRoot, l.baseDir, l.boundaryDir, l.opts, "helm value file", raw)
	target := argoHelmValueFile{file: p.file}
	if err != nil {
		return target, err
	}
	if p.url != "" {
		parsed, _ := parseRemoteHelmValueFile(p.url)
		if !helmValueFileSchemeAllowed(parsed.Scheme, l.opts) {
			return target, fmt.Errorf("error resolving value file path: the URL scheme '%s' is not allowed", parsed.Scheme)
		}
		target.url = p.url
		return target, nil
	}
	resolved, err := resolveArgoValueFilePath(p.appPath, p.root, p.path)
	if err != nil {
		if p.ref {
			return target, fmt.Errorf("error resolving value file path: error resolving value file path: %w", err)
		}
		return target, fmt.Errorf("error resolving value file path: %w", err)
	}
	if target.root, err = filepath.Abs(p.root); err != nil {
		return target, err
	}
	target.path = resolved
	if pathEntersGit(target.root, resolved) {
		return target, gitPathRefError("helm value file", target.file)
	}
	return target, nil
}

// argoValueFileGlob reports whether the resolved path of target is a glob.
// The repo-server checks the whole resolved path (isGlobPath). drydock
// checks only the part under the root, so glob characters in the path of
// drydock's checkout do not count.
func (target argoHelmValueFile) glob() bool {
	rel, err := filepath.Rel(target.root, target.path)
	if err != nil {
		return hasHelmValueGlob(target.path)
	}
	return hasHelmValueGlob(rel)
}

// planArgo resolves every value file as getResolvedValueFiles does
// (reposerver/repository/repository.go:1468-1581 at v3.6.1). Each resolved
// path or URL is read once, at its first place in the list (appendUnique),
// so [a, b, a] reads a and then b. A glob expands with doublestar in lexical
// order and leaves out the files that the list also names explicitly, which
// keep their explicit place. A glob match must stay in its root after its
// symlinks resolve (verifyGlobMatchesWithinRoot). A file that does not
// exist is left out with ignoreMissingValueFiles.
func (l *helmValueFileLoader) planArgo(files []string) ([]plannedHelmValueFile, error) {
	explicit := map[string]struct{}{}
	for _, raw := range files {
		target, err := l.resolveArgoHelmValueFile(raw)
		if err != nil {
			// The main loop below reports the error.
			continue
		}
		if target.url != "" {
			explicit[target.url] = struct{}{}
			continue
		}
		if !target.glob() {
			explicit[target.path] = struct{}{}
		}
	}

	var plans []plannedHelmValueFile
	seen := map[string]struct{}{}
	appendUnique := func(key string, plan plannedHelmValueFile) {
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		plans = append(plans, plan)
	}
	for _, raw := range files {
		target, err := l.resolveArgoHelmValueFile(raw)
		if err != nil {
			return nil, fmt.Errorf("error resolving helm value files: %w", err)
		}
		if target.url != "" {
			appendUnique(target.url, plannedHelmValueFile{display: remote.RedactURL(target.url), url: target.url})
			continue
		}
		if target.glob() {
			matches, err := l.expandArgoHelmValueGlob(raw, target)
			if err != nil {
				return nil, fmt.Errorf("error resolving helm value files: %w", err)
			}
			for _, match := range matches {
				if _, ok := explicit[match]; ok {
					continue
				}
				appendUnique(match, plannedHelmValueFile{display: displayHelmValueGlobMatch(target.root, match, target.file), root: target.root, path: match, argo: true})
			}
			continue
		}
		if _, err := os.Stat(target.path); os.IsNotExist(err) && l.ignoreMissing {
			continue
		}
		appendUnique(target.path, plannedHelmValueFile{display: target.file, root: target.root, path: target.path, argo: true})
	}
	return plans, nil
}

// expandArgoHelmValueGlob returns the files that the glob of target
// matches, in lexical order, as getResolvedValueFiles expands it with
// doublestar.FilepathGlob. A glob that matches nothing fails with the
// repo-server's GlobNoMatchError text, which names the raw pattern, unless
// ignoreMissingValueFiles is set. Every match must resolve, with its
// symlinks, under the root (verifyGlobMatchesWithinRoot). drydock also
// fails a match in .git, which it never reads.
func (l *helmValueFileLoader) expandArgoHelmValueGlob(raw string, target argoHelmValueFile) ([]string, error) {
	rel, err := filepath.Rel(target.root, target.path)
	if err != nil {
		return nil, err
	}
	relMatches, err := doublestar.Glob(os.DirFS(target.root), filepath.ToSlash(rel))
	if err != nil {
		return nil, fmt.Errorf("error expanding glob pattern %q: %w", raw, err)
	}
	if len(relMatches) == 0 {
		if l.ignoreMissing {
			return nil, nil
		}
		return nil, fmt.Errorf("values file glob %q matched no files", raw)
	}
	matches := make([]string, 0, len(relMatches))
	for _, relMatch := range relMatches {
		matches = append(matches, filepath.Join(target.root, filepath.FromSlash(relMatch)))
	}
	if err := verifyArgoGlobMatchesWithinRoot(matches, target.root); err != nil {
		return nil, fmt.Errorf("glob pattern %q: %w", raw, err)
	}
	for _, match := range matches {
		if pathEntersGit(target.root, match) {
			return nil, fmt.Errorf("helm value file glob %q matched %q, which enters a .git directory", target.file, displayHelmValueGlobMatch(target.root, match, target.file))
		}
	}
	return matches, nil
}

// verifyArgoGlobMatchesWithinRoot is verifyGlobMatchesWithinRoot
// (reposerver/repository/repository.go:1677-1702 at v3.6.1): each match,
// with its symlinks resolved, must be the root or under it. The errors have
// the repo-server's text, with the match relative to the root, where the
// repo-server names the path in its checkout.
func verifyArgoGlobMatchesWithinRoot(matches []string, root string) error {
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("error resolving symlinks in repo root: %w", err)
	}
	requiredRoot := canonicalRoot
	if !strings.HasSuffix(requiredRoot, string(os.PathSeparator)) {
		requiredRoot += string(os.PathSeparator)
	}
	for _, match := range matches {
		display := displayHelmValueGlobMatch(root, match, match)
		realMatch, err := filepath.EvalSymlinks(match)
		if err != nil {
			return fmt.Errorf("error resolving symlink for glob match %q: %w", display, errors.Unwrap(err))
		}
		if realMatch+string(os.PathSeparator) != requiredRoot && !strings.HasPrefix(realMatch, requiredRoot) {
			return fmt.Errorf("glob match %q resolved to outside repository root", display)
		}
	}
	return nil
}

// loadArgoLocal reads the local value file that helm --values reads at
// path. drydock reads it only when its real path is in the root and outside
// .git (confineHelmInputRead).
func (l *helmValueFileLoader) loadArgoLocal(planned plannedHelmValueFile) (loadedHelmValueFile, bool, error) {
	realPath, err := confineHelmInputRead("helm value file", planned.root, planned.path, planned.display)
	if err != nil {
		return loadedHelmValueFile{}, false, err
	}
	data, err := pathsafety.ReadRegularFile(realPath)
	if errors.Is(err, pathsafety.ErrNotRegular) {
		if info, statErr := os.Stat(realPath); statErr == nil && info.IsDir() {
			// helm's os.ReadFile fails on a directory with "is a
			// directory".
			_, err = os.ReadFile(realPath)
		}
	}
	if err != nil {
		if l.ignoreMissing && os.IsNotExist(err) {
			return loadedHelmValueFile{}, false, nil
		}
		return loadedHelmValueFile{}, false, fmt.Errorf("read helm value file %q: %w", planned.display, err)
	}
	values, err := l.decode(planned.display, data)
	if err != nil {
		return loadedHelmValueFile{}, false, err
	}
	return loadedHelmValueFile{display: planned.display, values: values}, true, nil
}
