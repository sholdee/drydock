package render

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/bmatcuk/doublestar/v4"
	"github.com/sholdee/drydock/internal/pathsafety"
	"github.com/sholdee/drydock/internal/remote"
	"helm.sh/helm/v4/pkg/chart/common"
	chartv2loader "helm.sh/helm/v4/pkg/chart/v2/loader"
)

type HelmLocalInputPath struct {
	Path     string
	Optional bool
}

type HelmLocalInputOptions struct {
	RepoRoot string
	Source   ResolvedSource
	Options  RenderOptions
	// OverriddenFileParameters are helm.fileParameters entries that a later
	// entry with the same name overrides. The render resolves their paths
	// but never reads them, so only the symlinks and ends of those paths
	// are inputs, as optional paths.
	OverriddenFileParameters []argoappv1.HelmFileParameter
}

// CollectHelmLocalInputPaths mirrors Helm value-file path resolution for the
// persistent render cache. It returns repository-relative committed paths only;
// callers should skip persistence when collection fails.
func CollectHelmLocalInputPaths(input HelmLocalInputOptions) ([]HelmLocalInputPath, error) {
	repoRoot := filepath.Clean(input.RepoRoot)
	if repoRoot == "" || repoRoot == "." {
		return nil, fmt.Errorf("repository root is required")
	}
	opts := input.Options
	opts.RefRoots = anchorHelmLocalInputRefRoots(repoRoot, opts.RefRoots)
	sourcePath, err := cleanSourcePath(input.Source.Path)
	if err != nil {
		return nil, err
	}
	source := input.Source
	if source.RepoRoot == "" {
		source.RepoRoot = repoRoot
	}
	out := &helmLocalInputCollector{
		repoRoot: repoRoot,
		source:   source,
		opts:     opts,
		paths:    map[string]bool{},
		explicit: map[string]struct{}{},
		globSeen: map[string]struct{}{},
	}
	if err := out.addRequiredRepoPath(sourcePath); err != nil {
		return nil, err
	}
	// helm reads only the last file parameter per name (applyHelmParameters).
	// File parameters stay out of the explicit identities: the renderer's
	// value-file glob skips only explicit value files.
	if err := out.collectExplicitIdentities(opts.ValueFiles); err != nil {
		return nil, err
	}
	for _, raw := range opts.ValueFiles {
		if err := out.collectValueFile(raw); err != nil {
			return nil, err
		}
	}
	for _, parameter := range EffectiveHelmFileParameters(opts.HelmFileParameters) {
		if err := out.collectFileParameter(parameter, false); err != nil {
			return nil, err
		}
	}
	for _, parameter := range input.OverriddenFileParameters {
		if err := out.collectFileParameter(parameter, true); err != nil {
			return nil, err
		}
	}
	return out.result(), nil
}

func anchorHelmLocalInputRefRoots(repoRoot string, refRoots map[string]string) map[string]string {
	if len(refRoots) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(refRoots))
	for key, root := range refRoots {
		if filepath.IsAbs(root) {
			out[key] = filepath.Clean(root)
			continue
		}
		out[key] = filepath.Join(repoRoot, filepath.Clean(root))
	}
	return out
}

type helmLocalInputCollector struct {
	repoRoot string
	source   ResolvedSource
	opts     RenderOptions
	paths    map[string]bool
	explicit map[string]struct{}
	globSeen map[string]struct{}
}

func (c *helmLocalInputCollector) collectExplicitIdentities(files []string) error {
	for _, raw := range files {
		file, err := c.resolvedInput(raw)
		if err != nil {
			return err
		}
		if hasHelmValueGlob(file) || isRemoteHelmValueFile(file) {
			continue
		}
		root, resolved, err := resolveHelmValueFile(c.repoRoot, helmValueFilesBaseDir(c.source, c.opts), helmValueFilesBoundaryRoot(c.source, c.opts), c.opts.RefRoots, file)
		if err != nil {
			return err
		}
		if _, err := os.Lstat(resolved); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		identity, err := localHelmValueFileIdentity(root, resolved, file)
		if err != nil {
			return err
		}
		c.explicit[identity] = struct{}{}
	}
	return nil
}

func (c *helmLocalInputCollector) collectValueFile(raw string) error {
	file, err := c.resolvedInput(raw)
	if err != nil {
		return err
	}
	if isRemoteHelmValueFile(file) {
		return fmt.Errorf("remote Helm value file %q is not persistent-cache eligible", remote.RedactURL(file))
	}
	if hasHelmValueGlob(file) {
		return c.collectGlob(file)
	}
	return c.collectLocal(file, c.opts.IgnoreMissingValueFiles)
}

// collectFileParameter adds the inputs of one helm.fileParameters path:
// every symlink that resolving it passes through and the file that helm
// reads (HelmFileParameterInputPaths). The file must exist. helm never
// reads the file of an overridden entry, so such an entry adds its inputs
// only when its path passes through a symlink, which decides whether the
// path resolves, and all of them are optional.
func (c *helmLocalInputCollector) collectFileParameter(parameter argoappv1.HelmFileParameter, overridden bool) error {
	if _, err := c.resolvedInput(parameter.Path); err != nil {
		return err
	}
	p, err := helmFileParameterPathFor(c.source, c.opts, parameter.Path)
	if err != nil {
		return err
	}
	if p.url != "" {
		if overridden {
			return nil
		}
		return fmt.Errorf("remote Helm file parameter %q is not persistent-cache eligible", remote.RedactURL(p.url))
	}
	if hasHelmValueGlob(p.file) {
		return fmt.Errorf("helm file parameter %q uses an unsupported glob", p.file)
	}
	inputs, err := HelmFileParameterInputPaths(parameter.Name, p.appPath, p.root, p.path)
	if err != nil {
		return err
	}
	if inputs.Read == "" {
		return fmt.Errorf("helm file parameter %q does not resolve to a file", p.file)
	}
	if overridden && len(inputs.Symlinks) == 0 {
		return nil
	}
	if !overridden {
		if _, err := os.Lstat(inputs.Read); err != nil {
			return err
		}
	}
	return c.addFileParameterInputs(inputs, overridden)
}

// addFileParameterInputs adds the symlinks and the ends of one file
// parameter path. The path as written ends somewhere else only through a
// symlinked directory, and only the resolved path is read, so that end is
// always optional.
func (c *helmLocalInputCollector) addFileParameterInputs(inputs HelmFileParameterInputs, optional bool) error {
	for _, link := range inputs.Symlinks {
		if err := c.addRealRepoPath(link, optional); err != nil {
			return err
		}
	}
	if err := c.addRealRepoPath(inputs.Read, optional); err != nil {
		return err
	}
	if inputs.Written != "" {
		return c.addRealRepoPath(inputs.Written, true)
	}
	return nil
}

// addRealRepoPath adds path, an absolute path with the symlinks of the
// repository root resolved, as a repository path.
func (c *helmLocalInputCollector) addRealRepoPath(path string, optional bool) error {
	realRoot, err := filepath.EvalSymlinks(c.repoRoot)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(realRoot, path)
	if err != nil || rel == "." || filepath.IsAbs(rel) || pathsafety.RelEscapes(rel) {
		return fmt.Errorf("helm input path %q escapes repository root", path)
	}
	if pathsafety.RelEntersGit(rel) {
		return gitPathRefError("helm input path", filepath.ToSlash(rel))
	}
	return c.addPath(filepath.ToSlash(rel), optional)
}

func (c *helmLocalInputCollector) collectGlob(pattern string) error {
	root, resolvedPattern, err := resolveHelmValueFile(c.repoRoot, helmValueFilesBaseDir(c.source, c.opts), helmValueFilesBoundaryRoot(c.source, c.opts), c.opts.RefRoots, pattern)
	if err != nil {
		return err
	}
	matches, err := doublestar.FilepathGlob(resolvedPattern)
	if err != nil {
		return fmt.Errorf("helm value file glob %q: %w", pattern, err)
	}
	if len(matches) == 0 {
		if c.opts.IgnoreMissingValueFiles {
			return fmt.Errorf("optional missing Helm value file glob %q cannot be represented in committed path digest", pattern)
		}
		return fmt.Errorf("helm value file glob %q matched no files", pattern)
	}
	for _, match := range matches {
		if pathEntersGit(root, match) {
			return fmt.Errorf("helm value file glob %q matched %q, which enters a .git directory", pattern, displayHelmValueGlobMatch(root, match, pattern))
		}
		identity, err := localHelmValueFileIdentity(root, match, pattern)
		if err != nil {
			return err
		}
		if _, ok := c.explicit[identity]; ok {
			continue
		}
		if _, ok := c.globSeen[identity]; ok {
			continue
		}
		c.globSeen[identity] = struct{}{}
		if err := c.addResolvedRepoPath(root, match, false); err != nil {
			return err
		}
	}
	return nil
}

func (c *helmLocalInputCollector) collectLocal(file string, optional bool) error {
	root, resolved, err := resolveHelmValueFile(c.repoRoot, helmValueFilesBaseDir(c.source, c.opts), helmValueFilesBoundaryRoot(c.source, c.opts), c.opts.RefRoots, file)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(resolved); err != nil {
		if optional && os.IsNotExist(err) {
			return c.addResolvedRepoPath(root, resolved, true)
		}
		return err
	}
	if err := rejectSymlinkedPath(root, resolved); err != nil {
		return err
	}
	return c.addResolvedRepoPath(root, resolved, false)
}

func (c *helmLocalInputCollector) resolvedInput(raw string) (string, error) {
	if strings.Contains(raw, "$") {
		ref, rest, ok := helmValueFileRefPrefix(raw, c.opts.RefRoots)
		if !ok || ref == "" {
			return "", fmt.Errorf("helm input path %q uses unsupported env substitution", raw)
		}
		if strings.Contains(rest, "$") {
			return "", fmt.Errorf("helm input path %q uses unsupported env substitution", raw)
		}
	}
	return envsubstHelmValueFilePath(raw, c.opts, c.opts.RefRoots), nil
}

func (c *helmLocalInputCollector) addResolvedRepoPath(root, resolved string, optional bool) error {
	rel, err := filepath.Rel(c.repoRoot, filepath.Clean(resolved))
	if err != nil {
		return err
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("helm input path %q escapes repository root", resolved)
	}
	rootRel, err := filepath.Rel(c.repoRoot, filepath.Clean(root))
	if err != nil {
		return err
	}
	if rootRel == ".." || strings.HasPrefix(rootRel, ".."+string(filepath.Separator)) || filepath.IsAbs(rootRel) {
		return fmt.Errorf("helm input root %q escapes repository root", root)
	}
	return c.addPath(filepath.ToSlash(rel), optional)
}

func (c *helmLocalInputCollector) addRequiredRepoPath(path string) error {
	return c.addPath(filepath.ToSlash(path), false)
}

func (c *helmLocalInputCollector) addPath(path string, optional bool) error {
	clean := filepath.ToSlash(filepath.Clean(path))
	if existingOptional, ok := c.paths[clean]; ok {
		c.paths[clean] = existingOptional && optional
		return nil
	}
	c.paths[clean] = optional
	return nil
}

func (c *helmLocalInputCollector) result() []HelmLocalInputPath {
	paths := make([]HelmLocalInputPath, 0, len(c.paths))
	for path, optional := range c.paths {
		paths = append(paths, HelmLocalInputPath{Path: path, Optional: optional})
	}
	sort.Slice(paths, func(i, j int) bool { return paths[i].Path < paths[j].Path })
	return paths
}

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
	if source.RepoRoot != "" {
		return "."
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

// helmValuesDecoder decodes the bytes of one values file into a values map.
type helmValuesDecoder func(display string, data []byte) (map[string]any, error)

// loadHelmValueFilesWith resolves every value file path and then reads the
// files in order with decode. The Helm renderer runs the two phases itself
// (assembleHelmInputValues), so file parameter paths resolve in between.
func loadHelmValueFilesWith(ctx context.Context, repoRoot, baseDir, boundaryDir string, refRoots map[string]string, files []string, ignoreMissing bool, opts RenderOptions, decode helmValuesDecoder) (map[string]any, error) {
	loader := newHelmValueFileLoader(ctx, repoRoot, baseDir, boundaryDir, refRoots, ignoreMissing, opts, decode)
	plans, err := loader.planAll(files)
	if err != nil {
		return nil, err
	}
	return loader.readAll(plans)
}

func newHelmValueFileLoader(ctx context.Context, repoRoot, baseDir, boundaryDir string, refRoots map[string]string, ignoreMissing bool, opts RenderOptions, decode helmValuesDecoder) *helmValueFileLoader {
	return &helmValueFileLoader{
		ctx:           ctx,
		repoRoot:      repoRoot,
		baseDir:       baseDir,
		boundaryDir:   boundaryDir,
		refRoots:      refRoots,
		ignoreMissing: ignoreMissing,
		opts:          opts,
		decode:        decode,
		explicit:      map[string]struct{}{},
		globSeen:      map[string]struct{}{},
	}
}

type helmValueFileLoader struct {
	ctx           context.Context
	repoRoot      string
	baseDir       string
	boundaryDir   string
	refRoots      map[string]string
	ignoreMissing bool
	opts          RenderOptions
	decode        helmValuesDecoder
	explicit      map[string]struct{}
	globSeen      map[string]struct{}
}

type loadedHelmValueFile struct {
	display string
	values  map[string]any
}

// plannedHelmValueFile is one value file that helm will read: a remote URL,
// or a local path under root.
type plannedHelmValueFile struct {
	display string
	url     string
	root    string
	path    string
}

// planAll resolves every value file path, expanding globs, without reading
// any file, as the repo-server's getResolvedValueFiles does before it runs
// helm (reposerver/repository/repository.go:1468-1581 at v3.6.1).
func (l *helmValueFileLoader) planAll(files []string) ([]plannedHelmValueFile, error) {
	if err := l.collectExplicitIdentities(files); err != nil {
		return nil, err
	}
	var plans []plannedHelmValueFile
	for _, file := range files {
		planned, err := l.plan(file)
		if err != nil {
			return nil, err
		}
		plans = append(plans, planned...)
	}
	return plans, nil
}

// readAll reads and merges the planned value files in order, as helm
// --values does.
func (l *helmValueFileLoader) readAll(plans []plannedHelmValueFile) (map[string]any, error) {
	out := map[string]any{}
	for _, planned := range plans {
		values, ok, err := l.read(planned)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if err := mergeHelmValueMap(out, values.values); err != nil {
			return nil, fmt.Errorf("merge helm value file %q: %w", values.display, err)
		}
	}
	return out, nil
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

func (l *helmValueFileLoader) plan(raw string) ([]plannedHelmValueFile, error) {
	file := envsubstHelmValueFilePath(raw, l.opts, l.refRoots)
	if isRemoteHelmValueFile(file) {
		parsed, err := url.Parse(file)
		if err != nil {
			return nil, fmt.Errorf("parse helm value file URL %q: %w", remote.RedactURL(file), err)
		}
		if !helmValueFileSchemeAllowed(parsed.Scheme, l.opts) {
			return nil, fmt.Errorf("helm value file URL scheme %q is not allowed", parsed.Scheme)
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return nil, fmt.Errorf("helm value file URL scheme %q is configured but not supported by drydock", parsed.Scheme)
		}
		return []plannedHelmValueFile{{display: remote.RedactURL(file), url: file}}, nil
	}
	if hasHelmValueGlob(file) {
		return l.planGlob(file)
	}
	root, resolved, err := resolveHelmValueFile(l.repoRoot, l.baseDir, l.boundaryDir, l.refRoots, file)
	if err != nil {
		return nil, err
	}
	return []plannedHelmValueFile{{display: file, root: root, path: resolved}}, nil
}

func (l *helmValueFileLoader) planGlob(pattern string) ([]plannedHelmValueFile, error) {
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
	out := make([]plannedHelmValueFile, 0, len(matches))
	for _, match := range matches {
		if pathEntersGit(root, match) {
			return nil, fmt.Errorf("helm value file glob %q matched %q, which enters a .git directory", pattern, displayHelmValueGlobMatch(root, match, pattern))
		}
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
		out = append(out, plannedHelmValueFile{display: displayHelmValueGlobMatch(root, match, pattern), root: root, path: match})
	}
	return out, nil
}

func (l *helmValueFileLoader) read(planned plannedHelmValueFile) (loadedHelmValueFile, bool, error) {
	if planned.url != "" {
		values, err := l.loadRemote(planned.url)
		return values, err == nil, err
	}
	return l.loadLocalResolved(planned.root, planned.path, planned.display)
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
	values, err := l.decode(display, data)
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
	values, err := l.decode(remote.RedactURL(file), data)
	if err != nil {
		return loadedHelmValueFile{}, err
	}
	return loadedHelmValueFile{display: remote.RedactURL(file), values: values}, nil
}

// parseHelmValueFile decodes a values file exactly as `helm template --values`
// does (pkg/cli/values Options.MergeValues): every YAML document is decoded
// through sigs.k8s.io/yaml and the documents are merged. Decoding through JSON
// makes every number float64, so a template printing {{ .Values.x }} renders
// 1000000 as 1e+06 in Argo CD; YAML 1.1 scalars (yes/no/on/off) are booleans;
// timestamps stay strings; duplicate keys resolve last-wins.
func parseHelmValueFile(display string, data []byte) (map[string]any, error) {
	values, err := chartv2loader.LoadValues(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("helm value file %q must be a YAML mapping: %w", display, err)
	}
	return values, nil
}

// ParseHelmValuesFile reads the data of one Helm values file as the Helm
// value file loader does (parseHelmValueFile), which merges every YAML
// document of the file.
func ParseHelmValuesFile(display string, data []byte) (map[string]any, error) {
	return parseHelmValueFile(display, data)
}

func hasHelmValueGlob(file string) bool {
	return strings.ContainsAny(file, "*?[")
}

func isRemoteHelmValueFile(file string) bool {
	_, ok := parseRemoteHelmValueFile(file)
	return ok
}

func parseRemoteHelmValueFile(file string) (*url.URL, bool) {
	parsed, err := url.Parse(file)
	if err != nil || !parsed.IsAbs() || parsed.Scheme == "" {
		return nil, false
	}
	return parsed, true
}

func envsubstHelmValueFilePath(raw string, opts RenderOptions, refRoots map[string]string) string {
	ref, rest, ok := helmValueFileRefPrefix(raw, refRoots)
	if !ok {
		file := opts.ArgoEnv.Envsubst(raw)
		if strings.HasPrefix(file, "$") {
			// "$$name" is an escaped "$", not a ref, as in the repo-server
			// (getReferencedSourceName and Env.Envsubst). Keep the
			// substituted path relative so it is not read as a ref.
			return "./" + file
		}
		return file
	}
	return ref + opts.ArgoEnv.Envsubst(rest)
}

// helmValueFileRefPrefix splits raw into the $ref that names a source of
// refRoots and the rest of the path, which starts with "/" and is not
// empty. The ref is decided on raw, before env substitution, as the
// repo-server does. "$$name" is an escaped "$", not a ref.
func helmValueFileRefPrefix(raw string, refRoots map[string]string) (string, string, bool) {
	ref := helmFileParameterRefName(raw)
	if ref == "" {
		return "", "", false
	}
	rest := strings.TrimPrefix(raw, ref)
	if len(rest) < 2 {
		return "", "", false
	}
	if _, ok := helmRefRootFor(ref, refRoots); !ok {
		return "", "", false
	}
	return ref, rest, true
}

func localHelmValueFileIdentity(root, resolved, display string) (string, error) {
	if err := rejectSymlinkedPath(root, resolved); err != nil {
		return "", fmt.Errorf("helm value file %q: %w", display, err)
	}
	realPath, err := filepath.EvalSymlinks(resolved)
	if err != nil {
		return "", err
	}
	return filepath.Clean(realPath), nil
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
		root, refPath, err := helmValueFileRefRoot(file, refRoots)
		if err != nil {
			return "", "", err
		}
		return resolveHelmValueFileUnderRoot(root, strings.TrimPrefix(refPath, "/"), file)
	}

	baseRoot, boundaryRoot, err := helmValueFileRoots(repoRoot, baseDir, boundaryDir)
	if err != nil {
		return "", "", err
	}
	if baseRoot == boundaryRoot {
		return resolveHelmValueFileUnderRoot(baseRoot, file, file)
	}
	return resolveHelmValueFileUnderBoundary(baseRoot, boundaryRoot, file, file)
}

// helmValueFileRoots returns the directory that local value files are
// relative to and the root they must stay under, both joined to repoRoot.
// Neither may have a symlink component.
func helmValueFileRoots(repoRoot, baseDir, boundaryDir string) (string, string, error) {
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
	return filepath.Join(repoRoot, cleanBase), filepath.Join(repoRoot, cleanBoundary), nil
}

// helmValueFileRefRoot returns the root of the $ref source that file names
// and the rest of file after the ref, which starts with "/".
func helmValueFileRefRoot(file string, refRoots map[string]string) (string, string, error) {
	after, _ := strings.CutPrefix(file, "$")
	ref, refPath, ok := strings.Cut(after, "/")
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
	return cleanRoot, "/" + refPath, nil
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
	for component := range strings.SplitSeq(sourcePath, string(filepath.Separator)) {
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
	if pathsafety.RelEntersGit(rel) {
		return "", "", gitPathRefError("helm value file", display)
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
	if pathsafety.RelEntersGit(rel) {
		return "", "", gitPathRefError("helm value file", display)
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
