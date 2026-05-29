package render

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/sholdee/drydock/internal/remote"
	"go.yaml.in/yaml/v4"
	"helm.sh/helm/v4/pkg/chart/common"
)

func helmValueFilesBaseDir(source ResolvedSource, opts RenderOptions) string {
	if opts.ValueFilesBaseDir != "" {
		return opts.ValueFilesBaseDir
	}
	return source.Path
}

func helmValueFilesBoundaryRoot(source ResolvedSource, opts RenderOptions) string {
	if opts.ValueFilesBoundaryRoot != "" {
		return opts.ValueFilesBoundaryRoot
	}
	return helmValueFilesBaseDir(source, opts)
}

func shouldLoadHelmValueFiles(mode string, inlineValues map[string]any) (bool, error) {
	switch mode {
	case "", "override", "merge":
		return true, nil
	case "replace":
		return len(inlineValues) == 0, nil
	default:
		return false, fmt.Errorf("unsupported helm values merge mode %q", mode)
	}
}

func loadHelmValueFiles(ctx context.Context, repoRoot, baseDir, boundaryDir string, refRoots map[string]string, files []string, ignoreMissing bool, opts RenderOptions) (map[string]any, error) {
	loader := helmValueFileLoader{
		ctx:           ctx,
		repoRoot:      repoRoot,
		baseDir:       baseDir,
		boundaryDir:   boundaryDir,
		refRoots:      refRoots,
		ignoreMissing: ignoreMissing,
		opts:          opts,
		explicit:      map[string]struct{}{},
		globSeen:      map[string]struct{}{},
	}
	if err := loader.collectExplicitIdentities(files); err != nil {
		return nil, err
	}
	out := map[string]any{}
	for _, file := range files {
		valuesList, err := loader.load(file)
		if err != nil {
			return nil, err
		}
		for _, values := range valuesList {
			if err := mergeHelmValueMap(out, values.values); err != nil {
				return nil, fmt.Errorf("merge helm value file %q: %w", values.display, err)
			}
		}
	}
	return out, nil
}

type helmValueFileLoader struct {
	ctx           context.Context
	repoRoot      string
	baseDir       string
	boundaryDir   string
	refRoots      map[string]string
	ignoreMissing bool
	opts          RenderOptions
	explicit      map[string]struct{}
	globSeen      map[string]struct{}
}

type loadedHelmValueFile struct {
	display string
	values  map[string]any
}

func (l *helmValueFileLoader) collectExplicitIdentities(files []string) error {
	for _, raw := range files {
		file := envsubstHelmValueFilePath(raw, l.opts, l.refRoots)
		if hasHelmValueGlob(file) || isRemoteHelmValueFile(file) {
			continue
		}
		root, resolved, err := resolveHelmValueFile(l.repoRoot, l.baseDir, l.boundaryDir, l.refRoots, file)
		if err != nil {
			return err
		}
		if _, err := os.Lstat(resolved); err != nil {
			if l.ignoreMissing && os.IsNotExist(err) {
				continue
			}
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		identity, err := localHelmValueFileIdentity(root, resolved, file)
		if err != nil {
			return err
		}
		l.explicit[identity] = struct{}{}
	}
	return nil
}

func (l *helmValueFileLoader) load(raw string) ([]loadedHelmValueFile, error) {
	file := envsubstHelmValueFilePath(raw, l.opts, l.refRoots)
	if isRemoteHelmValueFile(file) {
		values, err := l.loadRemote(file)
		if err != nil {
			return nil, err
		}
		return []loadedHelmValueFile{values}, nil
	}
	if hasHelmValueGlob(file) {
		return l.loadGlob(file)
	}
	values, ok, err := l.loadLocal(file, file)
	if err != nil || !ok {
		return nil, err
	}
	return []loadedHelmValueFile{values}, nil
}

func (l *helmValueFileLoader) loadGlob(pattern string) ([]loadedHelmValueFile, error) {
	root, resolvedPattern, err := resolveHelmValueFile(l.repoRoot, l.baseDir, l.boundaryDir, l.refRoots, pattern)
	if err != nil {
		return nil, err
	}
	matches, err := doublestar.FilepathGlob(resolvedPattern)
	if err != nil {
		return nil, fmt.Errorf("helm value file glob %q: %w", pattern, err)
	}
	if len(matches) == 0 {
		if l.ignoreMissing {
			return nil, nil
		}
		return nil, fmt.Errorf("helm value file glob %q matched no files", pattern)
	}
	out := make([]loadedHelmValueFile, 0, len(matches))
	for _, match := range matches {
		identity, err := localHelmValueFileIdentity(root, match, pattern)
		if err != nil {
			return nil, err
		}
		if _, ok := l.explicit[identity]; ok {
			continue
		}
		if _, ok := l.globSeen[identity]; ok {
			continue
		}
		l.globSeen[identity] = struct{}{}
		display := displayHelmValueGlobMatch(root, match, pattern)
		values, ok, err := l.loadLocalResolved(root, match, display)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, values)
		}
	}
	return out, nil
}

func (l *helmValueFileLoader) loadLocal(file, display string) (loadedHelmValueFile, bool, error) {
	root, resolved, err := resolveHelmValueFile(l.repoRoot, l.baseDir, l.boundaryDir, l.refRoots, file)
	if err != nil {
		return loadedHelmValueFile{}, false, err
	}
	return l.loadLocalResolved(root, resolved, display)
}

func (l *helmValueFileLoader) loadLocalResolved(root, resolved, display string) (loadedHelmValueFile, bool, error) {
	if err := rejectSymlinkedPath(root, resolved); err != nil {
		return loadedHelmValueFile{}, false, fmt.Errorf("helm value file %q: %w", display, err)
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		if l.ignoreMissing && os.IsNotExist(err) {
			return loadedHelmValueFile{}, false, nil
		}
		return loadedHelmValueFile{}, false, fmt.Errorf("read helm value file %q: %w", display, err)
	}
	values, err := parseHelmValueFile(display, data)
	if err != nil {
		return loadedHelmValueFile{}, false, err
	}
	return loadedHelmValueFile{display: display, values: values}, true, nil
}

func (l *helmValueFileLoader) loadRemote(file string) (loadedHelmValueFile, error) {
	parsed, err := url.Parse(file)
	if err != nil {
		return loadedHelmValueFile{}, fmt.Errorf("parse helm value file URL %q: %w", remote.RedactURL(file), err)
	}
	if !helmValueFileSchemeAllowed(parsed.Scheme, l.opts) {
		return loadedHelmValueFile{}, fmt.Errorf("helm value file URL scheme %q is not allowed", parsed.Scheme)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return loadedHelmValueFile{}, fmt.Errorf("helm value file URL scheme %q is configured but not supported by drydock", parsed.Scheme)
	}
	acquirer := l.opts.RemoteResourceAcquirer
	if acquirer == nil {
		acquirer = remote.DefaultAcquirer{}
	}
	forbiddenRoots := l.opts.RemoteResourceForbiddenRoots
	if len(forbiddenRoots) == 0 {
		forbiddenRoots = []string{l.repoRoot}
	}
	request := remote.Request{URL: file, Kind: remote.RequestHTTPFile}
	acquired, err := acquirer.Acquire(l.ctx, request, remote.Options{
		CacheDir:       l.opts.RemoteResourceCacheDir,
		Offline:        l.opts.OfflineRemoteResources,
		Refresh:        l.opts.RefreshRemoteResources,
		ForbiddenRoots: forbiddenRoots,
		Credentials:    l.opts.RemoteResourceCredentials,
		GitCredentials: l.opts.RemoteResourceGitCredentials,
	})
	if err != nil {
		recordRemoteCacheEvent(l.opts, request, err, remote.Result{})
		return loadedHelmValueFile{}, fmt.Errorf("acquire helm value file %s: %s", remote.RedactURL(file), remote.RedactCredentialError(err.Error(), l.opts.RemoteResourceCredentials, l.opts.RemoteResourceGitCredentials))
	}
	release := acquired.Release
	defer func() {
		if release != nil {
			release()
		}
	}()
	recordRemoteCacheEvent(l.opts, request, nil, acquired)
	if err := rejectSymlinkedPath(filepath.Dir(acquired.Path), acquired.Path); err != nil {
		return loadedHelmValueFile{}, fmt.Errorf("helm value file %s: %w", remote.RedactURL(file), err)
	}
	data, err := os.ReadFile(acquired.Path)
	if err != nil {
		return loadedHelmValueFile{}, fmt.Errorf("read helm value file %s: %w", remote.RedactURL(file), err)
	}
	values, err := parseHelmValueFile(remote.RedactURL(file), data)
	if err != nil {
		return loadedHelmValueFile{}, err
	}
	return loadedHelmValueFile{display: remote.RedactURL(file), values: values}, nil
}

func parseHelmValueFile(display string, data []byte) (map[string]any, error) {
	values := map[string]any{}
	if err := yaml.Unmarshal(data, &values); err != nil {
		return nil, fmt.Errorf("helm value file %q must be a YAML mapping: %w", display, err)
	}
	if values == nil {
		values = map[string]any{}
	}
	return values, nil
}

func hasHelmValueGlob(file string) bool {
	return strings.ContainsAny(file, "*?[")
}

func isRemoteHelmValueFile(file string) bool {
	parsed, err := url.Parse(file)
	return err == nil && parsed.IsAbs() && parsed.Scheme != ""
}

func envsubstHelmValueFilePath(raw string, opts RenderOptions, refRoots map[string]string) string {
	ref, rest, ok := helmValueFileRefPrefix(raw, refRoots)
	if !ok {
		return opts.ArgoEnv.Envsubst(raw)
	}
	return ref + opts.ArgoEnv.Envsubst(rest)
}

func helmValueFileRefPrefix(raw string, refRoots map[string]string) (string, string, bool) {
	if !strings.HasPrefix(raw, "$") {
		return "", "", false
	}
	ref, rest, ok := strings.Cut(raw, "/")
	if !ok || ref == "" || rest == "" {
		return "", "", false
	}
	if _, ok := refRoots[ref]; ok {
		return ref, "/" + rest, true
	}
	if _, ok := refRoots[strings.TrimPrefix(ref, "$")]; ok {
		return ref, "/" + rest, true
	}
	return "", "", false
}

func localHelmValueFileIdentity(root, resolved, display string) (string, error) {
	if err := rejectSymlinkedPath(root, resolved); err != nil {
		return "", fmt.Errorf("helm value file %q: %w", display, err)
	}
	real, err := filepath.EvalSymlinks(resolved)
	if err != nil {
		return "", err
	}
	return filepath.Clean(real), nil
}

func displayHelmValueGlobMatch(root, match, pattern string) string {
	rel, err := filepath.Rel(root, match)
	if err != nil {
		return pattern
	}
	return filepath.ToSlash(rel)
}

func helmValueFileSchemeAllowed(scheme string, opts RenderOptions) bool {
	allowed := opts.HelmValueFileSchemes
	if !opts.HelmValueFileSchemesSet {
		allowed = []string{"https", "http"}
	}
	for _, candidate := range allowed {
		if strings.EqualFold(strings.TrimSpace(candidate), scheme) {
			return true
		}
	}
	return false
}

func resolveHelmValueFile(repoRoot, baseDir, boundaryDir string, refRoots map[string]string, file string) (string, string, error) {
	if strings.HasPrefix(file, "$") {
		ref, refPath, ok := strings.Cut(strings.TrimPrefix(file, "$"), "/")
		if !ok || ref == "" || refPath == "" {
			return "", "", fmt.Errorf("helm value file %q must use $ref/path syntax", file)
		}
		refKey := "$" + ref
		root, ok := refRoots[refKey]
		if !ok || root == "" {
			root, ok = refRoots[ref]
		}
		if !ok || root == "" {
			return "", "", fmt.Errorf("helm value file %q references unknown ref %q", file, refKey)
		}
		cleanRoot := filepath.Clean(root)
		if err := rejectHelmRefRootSymlink(cleanRoot); err != nil {
			return "", "", fmt.Errorf("helm value file %q ref root %q: %w", file, refKey, err)
		}
		return resolveHelmValueFileUnderRoot(cleanRoot, refPath, file)
	}

	cleanBase, err := cleanSourcePath(baseDir)
	if err != nil {
		return "", "", fmt.Errorf("helm value files base dir %q: %w", baseDir, err)
	}
	if err := rejectHelmValueBaseDirSymlinkComponents(repoRoot, cleanBase); err != nil {
		return "", "", fmt.Errorf("helm value files base dir %q: %w", baseDir, err)
	}
	cleanBoundary, err := cleanSourcePath(boundaryDir)
	if err != nil {
		return "", "", fmt.Errorf("helm value files boundary root %q: %w", boundaryDir, err)
	}
	if err := rejectHelmValueBaseDirSymlinkComponents(repoRoot, cleanBoundary); err != nil {
		return "", "", fmt.Errorf("helm value files boundary root %q: %w", boundaryDir, err)
	}
	baseRoot := filepath.Join(repoRoot, cleanBase)
	boundaryRoot := filepath.Join(repoRoot, cleanBoundary)
	if cleanBoundary == cleanBase {
		return resolveHelmValueFileUnderRoot(baseRoot, file, file)
	}
	return resolveHelmValueFileUnderBoundary(baseRoot, boundaryRoot, file, file)
}

func rejectHelmRefRootSymlink(root string) error {
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("path %q is a symlink", root)
	}
	return nil
}

func rejectHelmValueBaseDirSymlinkComponents(repoRoot, sourcePath string) error {
	if sourcePath == "." {
		return nil
	}

	current := filepath.Clean(repoRoot)
	for _, component := range strings.Split(sourcePath, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("source path %q includes symlink component %q", sourcePath, component)
		}
	}
	return nil
}

func resolveHelmValueFileUnderRoot(root, file, display string) (string, string, error) {
	if filepath.IsAbs(file) {
		return "", "", fmt.Errorf("helm value file %q must be relative", display)
	}
	clean := filepath.Clean(file)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("helm value file %q escapes value files root", display)
	}
	resolved := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("helm value file %q escapes value files root", display)
	}
	return root, resolved, nil
}

func resolveHelmValueFileUnderBoundary(baseRoot, boundaryRoot, file, display string) (string, string, error) {
	if filepath.IsAbs(file) {
		return "", "", fmt.Errorf("helm value file %q must be relative", display)
	}
	clean := filepath.Clean(file)
	if strings.TrimSpace(file) == "" || clean == "." {
		return "", "", fmt.Errorf("helm value file %q escapes value files boundary", display)
	}
	resolved := filepath.Clean(filepath.Join(baseRoot, clean))
	rel, err := filepath.Rel(boundaryRoot, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("helm value file %q escapes value files boundary", display)
	}
	return boundaryRoot, resolved, nil
}

func mergeHelmValues(fileValues, inlineValues map[string]any, mode string) (map[string]any, error) {
	switch mode {
	case "", "override":
		out := cloneValues(fileValues)
		if err := mergeHelmValueMap(out, inlineValues); err != nil {
			return nil, err
		}
		return out, nil
	case "merge":
		out := cloneValues(inlineValues)
		if err := mergeHelmValueMap(out, fileValues); err != nil {
			return nil, err
		}
		return out, nil
	case "replace":
		if len(inlineValues) != 0 {
			return cloneValues(inlineValues), nil
		}
		return cloneValues(fileValues), nil
	default:
		return nil, fmt.Errorf("unsupported helm values merge mode %q", mode)
	}
}

func mergeHelmValueMap(dst, src map[string]any) error {
	for key, srcValue := range src {
		srcMap, srcIsMap := helmValueMap(srcValue)
		dstMap, dstIsMap := helmValueMap(dst[key])
		if srcIsMap && dstIsMap {
			if err := mergeHelmValueMap(dstMap, srcMap); err != nil {
				return err
			}
			dst[key] = dstMap
			continue
		}
		if srcIsMap {
			dst[key] = cloneValues(srcMap)
			continue
		}
		dst[key] = srcValue
	}
	return nil
}

func helmValueMap(value any) (map[string]any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		return typed, true
	case common.Values:
		return map[string]any(typed), true
	default:
		return nil, false
	}
}

func cloneValues(values map[string]any) map[string]any {
	out := make(map[string]any, len(values))
	for key, value := range values {
		if valueMap, ok := helmValueMap(value); ok {
			out[key] = cloneValues(valueMap)
			continue
		}
		out[key] = value
	}
	return out
}
