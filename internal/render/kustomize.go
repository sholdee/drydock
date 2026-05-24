package render

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/home-operations/argocd-local/internal/chart"
	"github.com/home-operations/argocd-local/internal/diagnostic"
	"github.com/home-operations/argocd-local/internal/manifest"
	"github.com/home-operations/argocd-local/internal/remote"
	goyaml "go.yaml.in/yaml/v4"
	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/api/types"
	"sigs.k8s.io/kustomize/kyaml/filesys"
)

type KustomizeRenderer struct{}

func (KustomizeRenderer) Render(ctx context.Context, source ResolvedSource, opts RenderOptions) ([]Manifest, []diagnostic.Diagnostic, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if len(opts.BuildOptions) != 0 {
		return nil, nil, fmt.Errorf("kustomize build options are not supported yet")
	}

	root, err := sourceRoot(source)
	if err != nil {
		return nil, nil, err
	}

	_, graph, err := collectKustomizeGraphForPreparation(ctx, source.RepoRoot, root)
	if err != nil {
		return nil, nil, err
	}
	if kustomizeGraphHasHelmCharts(graph) || hasAcquirableRemoteKustomizeGraphRefs(graph) {
		return renderKustomizeWithPreparedWorkspace(ctx, source, opts)
	}

	return renderPlainKustomize(ctx, source, root)
}

var kustomizationFileNames = []string{"kustomization.yaml", "kustomization.yml", "Kustomization"}

func renderPlainKustomize(ctx context.Context, source ResolvedSource, root string) ([]Manifest, []diagnostic.Diagnostic, error) {
	manifestPath, err := validateKustomizeGraph(ctx, source.RepoRoot, root)
	if err != nil {
		return nil, nil, err
	}

	kustomizer := krusty.MakeKustomizer(krusty.MakeDefaultOptions())
	resMap, err := kustomizer.Run(filesys.MakeFsOnDisk(), root)
	if err != nil {
		return nil, nil, fmt.Errorf("kustomize build %s: %w", root, err)
	}

	rendered, err := resMap.AsYaml()
	if err != nil {
		return nil, nil, fmt.Errorf("kustomize build %s: serialize manifests: %w", root, err)
	}

	docs, err := manifest.DecodeDocuments(manifestPath, bytes.NewReader(rendered))
	if err != nil {
		return nil, nil, err
	}

	out := make([]Manifest, 0, len(docs))
	for _, doc := range docs {
		out = append(out, Manifest{
			Path:   doc.Path,
			Object: doc.Object,
		})
	}
	return out, nil, nil
}

func kustomizeGraphHasHelmCharts(graph []kustomizeGraphNode) bool {
	for _, node := range graph {
		if len(node.Kustomization.HelmCharts) != 0 {
			return true
		}
	}
	return false
}

func hasAcquirableRemoteKustomizeGraphRefs(graph []kustomizeGraphNode) bool {
	for _, node := range graph {
		for _, resource := range node.Kustomization.Resources {
			if isAcquirableRemoteKustomizeResource(resource) {
				return true
			}
		}
		for _, base := range node.Kustomization.Bases {
			if isAcquirableRemoteKustomizeResource(base) {
				return true
			}
		}
		for _, component := range node.Kustomization.Components {
			if isAcquirableRemoteKustomizeResource(component) {
				return true
			}
		}
		if hasAcquirableRemoteKustomizePathRefs(node.Kustomization) {
			return true
		}
	}
	return false
}

func hasAcquirableRemoteKustomizePathRefs(kustomization types.Kustomization) bool {
	if isAcquirableRemoteKustomizePathRef(kustomization.OpenAPI["path"]) {
		return true
	}
	for _, ref := range kustomization.Configurations {
		if isAcquirableRemoteKustomizePathRef(ref) {
			return true
		}
	}
	for _, ref := range kustomization.Generators {
		if isAcquirableRemoteKustomizePathRef(ref) {
			return true
		}
	}
	for _, ref := range kustomization.Transformers {
		if isAcquirableRemoteKustomizePathRef(ref) {
			return true
		}
	}
	for _, ref := range kustomization.Validators {
		if isAcquirableRemoteKustomizePathRef(ref) {
			return true
		}
	}
	for _, ref := range kustomization.Crds {
		if isAcquirableRemoteKustomizePathRef(ref) {
			return true
		}
	}
	for _, replacement := range kustomization.Replacements {
		if isAcquirableRemoteKustomizePathRef(replacement.Path) {
			return true
		}
	}
	for _, patch := range kustomization.Patches {
		if isAcquirableRemoteKustomizePathRef(patch.Path) {
			return true
		}
	}
	//nolint:staticcheck // Kustomize still accepts patchesJson6902; scan it for remote refs.
	for _, patch := range kustomization.PatchesJson6902 {
		if isAcquirableRemoteKustomizePathRef(patch.Path) {
			return true
		}
	}
	//nolint:staticcheck // Kustomize still accepts patchesStrategicMerge; scan it for remote refs.
	for _, patch := range kustomization.PatchesStrategicMerge {
		ref := string(patch)
		if !isInlineStrategicMergePatch(ref) && isAcquirableRemoteKustomizePathRef(ref) {
			return true
		}
	}
	for _, generator := range kustomization.ConfigMapGenerator {
		if hasAcquirableRemoteGeneratorRefs(generator.KvPairSources) {
			return true
		}
	}
	for _, generator := range kustomization.SecretGenerator {
		if hasAcquirableRemoteGeneratorRefs(generator.KvPairSources) {
			return true
		}
	}
	return false
}

func hasAcquirableRemoteGeneratorRefs(sources types.KvPairSources) bool {
	for _, source := range sources.FileSources {
		if isAcquirableRemoteKustomizePathRef(generatorFileSourcePath(source)) {
			return true
		}
	}
	for _, source := range sources.EnvSources {
		if isAcquirableRemoteKustomizePathRef(source) {
			return true
		}
	}
	return isAcquirableRemoteKustomizePathRef(sources.EnvSource)
}

func isAcquirableRemoteKustomizePathRef(ref string) bool {
	_, _, ok, err := remoteRequestForKustomizeRef(ref)
	return err == nil && ok
}

type remotePathMode int

const (
	remotePathFile remotePathMode = iota
	remotePathDir
	remotePathAny
)

type kustomizePathRefNode struct {
	dir          string
	boundaryRoot string
	graphIndex   int
}

type kustomizeWorkspace struct {
	originalRepoRoot string
	tempRepoRoot     string
	opts             RenderOptions
	visited          map[string]struct{}
	nextGraphIndex   int
	nextPathIndex    int
}

func renderKustomizeWithPreparedWorkspace(ctx context.Context, source ResolvedSource, opts RenderOptions) ([]Manifest, []diagnostic.Diagnostic, error) {
	tempDir, err := os.MkdirTemp("", "argocd-local-kustomize-*")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(tempDir)

	tempRepoRoot := filepath.Join(tempDir, "repo")
	if err := copyWorkspaceTree(source.RepoRoot, tempRepoRoot); err != nil {
		return nil, nil, fmt.Errorf("copy repository to temp workspace: %w", err)
	}

	tempSource := ResolvedSource{
		RepoRoot: tempRepoRoot,
		Path:     source.Path,
	}
	tempRoot, err := sourceRoot(tempSource)
	if err != nil {
		return nil, nil, err
	}

	workspace := &kustomizeWorkspace{
		originalRepoRoot: filepath.Clean(source.RepoRoot),
		tempRepoRoot:     tempRepoRoot,
		opts:             opts,
		visited:          make(map[string]struct{}),
	}
	if err := workspace.prepareKustomizationDir(ctx, tempRoot, tempRepoRoot, ""); err != nil {
		return nil, nil, err
	}

	return renderPlainKustomize(ctx, tempSource, tempRoot)
}

func (w *kustomizeWorkspace) prepareKustomizationDir(ctx context.Context, dir, boundaryRoot, inheritedHelmNamespace string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir = filepath.Clean(dir)
	boundaryRoot = filepath.Clean(boundaryRoot)
	if err := rejectPathOutsideBoundary("kustomization directory", dir, boundaryRoot); err != nil {
		return err
	}
	key := boundaryRoot + "\x00" + dir
	if _, ok := w.visited[key]; ok {
		return nil
	}
	w.visited[key] = struct{}{}

	kustomizationFile, err := findKustomizationFile(dir)
	if err != nil {
		return err
	}
	manifestPath, err := relativeManifestPath(w.tempRepoRoot, kustomizationFile)
	if err != nil {
		manifestPath = kustomizationFile
	}
	content, err := os.ReadFile(kustomizationFile)
	if err != nil {
		return err
	}
	var kustomization types.Kustomization
	if err := goyaml.Unmarshal(content, &kustomization); err != nil {
		return fmt.Errorf("decode kustomization %s: %w", manifestPath, err)
	}

	graphIndex := w.nextGraphIndex
	w.nextGraphIndex++
	childInheritedHelmNamespace := inheritedHelmNamespace
	if kustomization.Namespace != "" {
		childInheritedHelmNamespace = kustomization.Namespace
	}

	if err := validateWorkspaceHelmFields(boundaryRoot, dir, &kustomization); err != nil {
		return fmt.Errorf("%s: %w", manifestPath, err)
	}

	if len(kustomization.HelmCharts) != 0 {
		nodeRelDir, err := relativeManifestPath(w.tempRepoRoot, dir)
		if err != nil {
			return err
		}
		generatedResources, err := renderKustomizeHelmCharts(ctx, w.tempRepoRoot, dir, nodeRelDir, childInheritedHelmNamespace, kustomizationChartHome(kustomization), graphIndex, kustomization.HelmCharts, w.opts)
		if err != nil {
			return err
		}
		kustomization.HelmCharts = nil
		kustomization.Resources = append(kustomization.Resources, generatedResources...)
	}

	resources, err := w.prepareKustomizeRefs(ctx, dir, boundaryRoot, "resources", graphIndex, kustomization.Resources, childInheritedHelmNamespace, true)
	if err != nil {
		return fmt.Errorf("%s: %w", manifestPath, err)
	}
	kustomization.Resources = resources

	bases, err := w.prepareKustomizeRefs(ctx, dir, boundaryRoot, "bases", graphIndex, kustomization.Bases, childInheritedHelmNamespace, false)
	if err != nil {
		return fmt.Errorf("%s: %w", manifestPath, err)
	}
	kustomization.Bases = bases

	components, err := w.prepareKustomizeRefs(ctx, dir, boundaryRoot, "components", graphIndex, kustomization.Components, childInheritedHelmNamespace, false)
	if err != nil {
		return fmt.Errorf("%s: %w", manifestPath, err)
	}
	kustomization.Components = components

	node := kustomizePathRefNode{
		dir:          dir,
		boundaryRoot: boundaryRoot,
		graphIndex:   graphIndex,
	}
	if err := w.rewriteKustomizePathBearingRefs(ctx, node, &kustomization); err != nil {
		return fmt.Errorf("%s: %w", manifestPath, err)
	}

	if err := validateWorkspacePathBearingRefs(boundaryRoot, dir, &kustomization); err != nil {
		return fmt.Errorf("%s: %w", manifestPath, err)
	}

	data, err := goyaml.Marshal(&kustomization)
	if err != nil {
		return fmt.Errorf("encode temp kustomization %s: %w", manifestPath, err)
	}
	if err := os.WriteFile(kustomizationFile, data, 0o644); err != nil {
		return fmt.Errorf("write temp kustomization %s: %w", manifestPath, err)
	}
	return nil
}

func (w *kustomizeWorkspace) prepareKustomizeRefs(ctx context.Context, dir, boundaryRoot, field string, graphIndex int, refs []string, inheritedHelmNamespace string, allowFileRefs bool) ([]string, error) {
	out := append([]string(nil), refs...)
	for i, ref := range refs {
		request, parsed, ok, err := remoteRequestForKustomizeRef(ref)
		if err != nil {
			return nil, err
		}
		if ok {
			if !allowFileRefs && parsed.Kind == kustomizeRemoteHTTPFile {
				return nil, fmt.Errorf("kustomize %s %q is a remote file ref; it must resolve to a Kustomization directory", field, redactKustomizeRef(parsed.Original))
			}
			mode := remotePathDir
			if allowFileRefs {
				mode = remotePathAny
			}
			rewritten, recurseDir, recurseBoundaryRoot, err := w.acquireAndCopyKustomizeRef(ctx, dir, field, graphIndex, i, request, parsed, mode, true)
			if err != nil {
				return nil, err
			}
			out[i] = rewritten
			if recurseDir != "" {
				if err := w.prepareKustomizationDir(ctx, recurseDir, recurseBoundaryRoot, inheritedHelmNamespace); err != nil {
					return nil, err
				}
			}
			continue
		}

		localPath, info, err := validateWorkspaceLocalRef(boundaryRoot, dir, field, ref)
		if err != nil {
			return nil, err
		}
		if info != nil && info.IsDir() {
			if err := w.prepareKustomizationDir(ctx, localPath, boundaryRoot, inheritedHelmNamespace); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

func (w *kustomizeWorkspace) rewriteKustomizePathBearingRefs(ctx context.Context, node kustomizePathRefNode, kustomization *types.Kustomization) error {
	if path := kustomization.OpenAPI["path"]; path != "" {
		rewritten, ok, err := w.rewriteKustomizePathRef(ctx, node, "openapi.path", path, remotePathFile)
		if err != nil {
			return err
		}
		if ok {
			if kustomization.OpenAPI == nil {
				kustomization.OpenAPI = map[string]string{}
			}
			kustomization.OpenAPI["path"] = rewritten
		}
	}
	var err error
	if kustomization.Configurations, err = w.rewriteKustomizePathRefs(ctx, node, "configurations", kustomization.Configurations, remotePathFile); err != nil {
		return err
	}
	if kustomization.Generators, err = w.rewriteKustomizePathRefs(ctx, node, "generators", kustomization.Generators, remotePathFile); err != nil {
		return err
	}
	if kustomization.Transformers, err = w.rewriteKustomizePathRefs(ctx, node, "transformers", kustomization.Transformers, remotePathFile); err != nil {
		return err
	}
	if kustomization.Validators, err = w.rewriteKustomizePathRefs(ctx, node, "validators", kustomization.Validators, remotePathFile); err != nil {
		return err
	}
	if kustomization.Crds, err = w.rewriteKustomizePathRefs(ctx, node, "crds", kustomization.Crds, remotePathFile); err != nil {
		return err
	}
	if err := w.rewriteKustomizeReplacementRefs(ctx, node, kustomization); err != nil {
		return err
	}
	if err := w.rewriteKustomizePatchRefs(ctx, node, kustomization); err != nil {
		return err
	}
	return w.rewriteKustomizeGeneratorListRefs(ctx, node, kustomization)
}

func (w *kustomizeWorkspace) rewriteKustomizePathRefs(ctx context.Context, node kustomizePathRefNode, field string, refs []string, mode remotePathMode) ([]string, error) {
	out := append([]string(nil), refs...)
	for i, ref := range refs {
		rewritten, ok, err := w.rewriteKustomizePathRef(ctx, node, field, ref, mode)
		if err != nil {
			return nil, err
		}
		if ok {
			out[i] = rewritten
		}
	}
	return out, nil
}

func (w *kustomizeWorkspace) rewriteKustomizeReplacementRefs(ctx context.Context, node kustomizePathRefNode, kustomization *types.Kustomization) error {
	for i, replacement := range kustomization.Replacements {
		if replacement.Path == "" {
			continue
		}
		rewritten, ok, err := w.rewriteKustomizePathRef(ctx, node, "replacements.path", replacement.Path, remotePathFile)
		if err != nil {
			return err
		}
		if ok {
			kustomization.Replacements[i].Path = rewritten
		}
	}
	return nil
}

func (w *kustomizeWorkspace) rewriteKustomizePatchRefs(ctx context.Context, node kustomizePathRefNode, kustomization *types.Kustomization) error {
	for i, patch := range kustomization.Patches {
		if patch.Path == "" {
			continue
		}
		rewritten, ok, err := w.rewriteKustomizePathRef(ctx, node, "patches.path", patch.Path, remotePathFile)
		if err != nil {
			return err
		}
		if ok {
			kustomization.Patches[i].Path = rewritten
		}
	}
	//nolint:staticcheck // Kustomize still accepts patchesJson6902; rewrite it for parity.
	for i, patch := range kustomization.PatchesJson6902 {
		if patch.Path == "" {
			continue
		}
		rewritten, ok, err := w.rewriteKustomizePathRef(ctx, node, "patchesJson6902.path", patch.Path, remotePathFile)
		if err != nil {
			return err
		}
		if ok {
			kustomization.PatchesJson6902[i].Path = rewritten
		}
	}
	//nolint:staticcheck // Kustomize still accepts patchesStrategicMerge; rewrite it for parity.
	for i, patch := range kustomization.PatchesStrategicMerge {
		ref := string(patch)
		if isInlineStrategicMergePatch(ref) {
			continue
		}
		rewritten, ok, err := w.rewriteKustomizePathRef(ctx, node, "patchesStrategicMerge", ref, remotePathFile)
		if err != nil {
			return err
		}
		if ok {
			kustomization.PatchesStrategicMerge[i] = types.PatchStrategicMerge(rewritten)
		}
	}
	return nil
}

func (w *kustomizeWorkspace) rewriteKustomizeGeneratorListRefs(ctx context.Context, node kustomizePathRefNode, kustomization *types.Kustomization) error {
	var err error
	for i, generator := range kustomization.ConfigMapGenerator {
		kustomization.ConfigMapGenerator[i].KvPairSources, err = w.rewriteKustomizeGeneratorRefs(ctx, node, "configMapGenerator", generator.KvPairSources)
		if err != nil {
			return err
		}
	}
	for i, generator := range kustomization.SecretGenerator {
		kustomization.SecretGenerator[i].KvPairSources, err = w.rewriteKustomizeGeneratorRefs(ctx, node, "secretGenerator", generator.KvPairSources)
		if err != nil {
			return err
		}
	}
	return nil
}

func (w *kustomizeWorkspace) rewriteKustomizeGeneratorRefs(ctx context.Context, node kustomizePathRefNode, field string, sources types.KvPairSources) (types.KvPairSources, error) {
	out := sources
	for i, source := range sources.FileSources {
		key, ref, hasKey := splitGeneratorFileSource(source)
		rewritten, ok, err := w.rewriteKustomizePathRef(ctx, node, field+".files", ref, remotePathFile)
		if err != nil {
			return types.KvPairSources{}, err
		}
		if ok {
			if !hasKey {
				key, err = generatorRemoteFileSourceKey(ref)
				if err != nil {
					return types.KvPairSources{}, err
				}
				hasKey = true
			}
			out.FileSources[i] = joinGeneratorFileSource(key, rewritten, hasKey)
		}
	}
	for i, source := range sources.EnvSources {
		rewritten, ok, err := w.rewriteKustomizePathRef(ctx, node, field+".envs", source, remotePathFile)
		if err != nil {
			return types.KvPairSources{}, err
		}
		if ok {
			out.EnvSources[i] = rewritten
		}
	}
	if sources.EnvSource != "" {
		rewritten, ok, err := w.rewriteKustomizePathRef(ctx, node, field+".env", sources.EnvSource, remotePathFile)
		if err != nil {
			return types.KvPairSources{}, err
		}
		if ok {
			out.EnvSource = rewritten
		}
	}
	return out, nil
}

func (w *kustomizeWorkspace) rewriteKustomizePathRef(ctx context.Context, node kustomizePathRefNode, field, ref string, mode remotePathMode) (string, bool, error) {
	request, parsed, ok, err := remoteRequestForKustomizeRef(ref)
	if err != nil || !ok {
		return ref, false, err
	}
	refIndex := w.nextPathIndex
	w.nextPathIndex++
	rewritten, _, _, err := w.acquireAndCopyKustomizeRef(ctx, node.dir, field, node.graphIndex, refIndex, request, parsed, mode, false)
	if err != nil {
		return "", false, err
	}
	return rewritten, true, nil
}

func (w *kustomizeWorkspace) acquireAndCopyKustomizeRef(ctx context.Context, dir, field string, graphIndex, refIndex int, request remote.Request, ref kustomizeRemoteRef, mode remotePathMode, recurseDirs bool) (string, string, string, error) {
	acquirer := w.opts.RemoteResourceAcquirer
	if acquirer == nil {
		acquirer = remote.DefaultAcquirer{}
	}
	forbiddenRoots := w.opts.RemoteResourceForbiddenRoots
	if len(forbiddenRoots) == 0 {
		forbiddenRoots = []string{w.originalRepoRoot}
	}
	acquired, err := acquirer.Acquire(ctx, request, remote.Options{
		CacheDir:       w.opts.RemoteResourceCacheDir,
		Offline:        w.opts.OfflineRemoteResources,
		Refresh:        w.opts.RefreshRemoteResources,
		ForbiddenRoots: forbiddenRoots,
		Credentials:    w.opts.RemoteResourceCredentials,
		GitCredentials: w.opts.RemoteResourceGitCredentials,
	})
	if err != nil {
		return "", "", "", fmt.Errorf("acquire remote kustomize resource %s: %s", redactKustomizeRef(ref.Original), redactRemoteKustomizeAcquireError(err, ref, w.opts))
	}
	acquiredPath, err := acquiredRemoteKustomizePath(acquired, ref)
	if err != nil {
		return "", "", "", err
	}
	info, err := os.Lstat(acquiredPath)
	if err != nil {
		return "", "", "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", "", "", fmt.Errorf("remote kustomize resource %s is a symlink", redactKustomizeRef(ref.Original))
	}

	generatedName := generatedRemoteRefName(fmt.Sprintf("%03d-%03d", graphIndex, refIndex), ref)
	if info.IsDir() {
		if mode == remotePathFile {
			return "", "", "", fmt.Errorf("kustomize %s %q must resolve to a regular file", field, redactKustomizeRef(ref.Original))
		}
		generatedKind := "remotes"
		if recurseDirs {
			generatedKind = "git"
		}
		generatedRel := filepath.ToSlash(filepath.Join(".argocd-local", generatedKind, generatedName))
		generatedRoot, err := generatedKustomizeWorkspacePath(dir, generatedRel)
		if err != nil {
			return "", "", "", err
		}
		if !recurseDirs {
			if err := copyRegularTree(acquiredPath, generatedRoot); err != nil {
				return "", "", "", fmt.Errorf("copy remote kustomize resource %s: %w", redactKustomizeRef(ref.Original), err)
			}
			return generatedRel, "", "", nil
		}
		recurseDir := generatedRoot
		rewritten := generatedRel
		if ref.Kind == kustomizeRemoteGit {
			repoRoot := filepath.Clean(acquired.Path)
			if err := copyWorkspaceTree(repoRoot, generatedRoot); err != nil {
				return "", "", "", fmt.Errorf("copy remote kustomize resource %s: %w", redactKustomizeRef(ref.Original), err)
			}
			subpath := path.Clean(strings.TrimPrefix(ref.Subpath, "/"))
			rewritten = path.Join(generatedRel, filepath.ToSlash(filepath.FromSlash(subpath)))
			recurseDir = filepath.Join(generatedRoot, filepath.FromSlash(subpath))
		} else {
			if err := copyRegularTree(acquiredPath, generatedRoot); err != nil {
				return "", "", "", fmt.Errorf("copy remote kustomize resource %s: %w", redactKustomizeRef(ref.Original), err)
			}
		}
		return rewritten, recurseDir, generatedRoot, nil
	}
	if mode == remotePathDir {
		return "", "", "", fmt.Errorf("kustomize %s %q must resolve to a Kustomization directory", field, redactKustomizeRef(ref.Original))
	}
	if !info.Mode().IsRegular() {
		return "", "", "", fmt.Errorf("remote kustomize resource %s is not a regular file or directory", redactKustomizeRef(ref.Original))
	}
	generatedRel := filepath.ToSlash(filepath.Join(".argocd-local", "remotes", generatedName))
	generatedPath, err := generatedKustomizeWorkspacePath(dir, generatedRel)
	if err != nil {
		return "", "", "", err
	}
	if err := copyAcquiredRemoteKustomizeResource(acquiredPath, generatedPath); err != nil {
		return "", "", "", fmt.Errorf("copy remote kustomize resource %s: %w", redactKustomizeRef(ref.Original), err)
	}
	return generatedRel, "", "", nil
}

func splitGeneratorFileSource(source string) (string, string, bool) {
	_, _, sourceIsRemote, sourceRemoteErr := remoteRequestForKustomizeRef(source)
	if sourceIsRemote && sourceRemoteErr == nil {
		return "", source, false
	}
	if before, after, ok := strings.Cut(source, "="); ok && before != "" {
		if _, _, afterIsRemote, afterRemoteErr := remoteRequestForKustomizeRef(after); afterIsRemote || afterRemoteErr != nil {
			return before, after, true
		}
		if sourceRemoteErr != nil {
			return "", source, false
		}
		return before, after, true
	}
	if sourceIsRemote {
		return "", source, false
	}
	return "", source, false
}

func joinGeneratorFileSource(key, ref string, hasKey bool) string {
	if !hasKey {
		return ref
	}
	return key + "=" + ref
}

func generatorRemoteFileSourceKey(ref string) (string, error) {
	parsed, ok, err := parseKustomizeRemoteRef(ref)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("generator file source %q is not a remote ref", ref)
	}

	var name string
	switch parsed.Kind {
	case kustomizeRemoteHTTPFile:
		parsedURL, err := url.Parse(parsed.URL)
		if err != nil {
			return "", err
		}
		name = path.Base(parsedURL.Path)
	case kustomizeRemoteGit:
		name = path.Base(path.Clean(strings.TrimPrefix(parsed.Subpath, "/")))
	default:
		return "", fmt.Errorf("unsupported remote generator file source kind %q", parsed.Kind)
	}
	if name == "" || name == "." || name == "/" {
		return "", fmt.Errorf("remote generator file source %q has no basename", redactKustomizeRef(ref))
	}
	return name, nil
}

func validateWorkspaceLocalRef(boundaryRoot, dir, field, ref string) (string, os.FileInfo, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", nil, nil
	}
	if isRemoteKustomizeRef(ref) {
		return "", nil, unsupportedRemoteKustomizeRefError(field, ref)
	}
	if filepath.IsAbs(ref) {
		return "", nil, fmt.Errorf("kustomize %s %q must be relative", field, ref)
	}

	path := filepath.Clean(filepath.Join(dir, filepath.FromSlash(ref)))
	if err := rejectPathOutsideBoundary("kustomize "+field, path, boundaryRoot); err != nil {
		return "", nil, err
	}
	if err := rejectSymlinkedPath(boundaryRoot, path); err != nil {
		return "", nil, fmt.Errorf("kustomize %s %q: %w", field, ref, err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return path, nil, nil
		}
		return "", nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", nil, fmt.Errorf("kustomize %s %q is a symlink", field, ref)
	}
	return path, info, nil
}

func validateWorkspaceHelmFields(boundaryRoot, dir string, kustomization *types.Kustomization) error {
	if len(kustomization.HelmChartInflationGenerator) != 0 {
		return fmt.Errorf("helmChartInflationGenerator is deprecated and unsupported")
	}
	if kustomization.HelmGlobals != nil && kustomization.HelmGlobals.ConfigHome != "" {
		return fmt.Errorf("helmGlobals.configHome is unsupported")
	}
	if len(kustomization.HelmCharts) == 0 {
		return nil
	}
	chartHome := kustomizationChartHome(*kustomization)
	if err := validateWorkspacePathRef(boundaryRoot, dir, "helmGlobals.chartHome", chartHome); err != nil {
		return err
	}
	for _, helmChart := range kustomization.HelmCharts {
		if helmChart.NameTemplate != "" {
			return fmt.Errorf("helmCharts.nameTemplate is unsupported")
		}
		if helmChart.Devel {
			return fmt.Errorf("helmCharts.devel is unsupported")
		}
		if helmChart.Debug {
			return fmt.Errorf("helmCharts.debug is unsupported")
		}
		if err := validateWorkspaceHelmChartPath(boundaryRoot, dir, chartHome, helmChart); err != nil {
			return err
		}
		if err := validateWorkspaceHelmValueRefs(boundaryRoot, dir, helmChart); err != nil {
			return err
		}
	}
	return nil
}

func validateWorkspaceHelmChartPath(boundaryRoot, dir, chartHome string, helmChart types.HelmChart) error {
	chartPath := filepath.FromSlash(helmChart.Name)
	if helmChart.Repo != "" && helmChart.Version != "" {
		chartPath = filepath.Join(filepath.FromSlash(helmChart.Name+"-"+helmChart.Version), filepath.FromSlash(helmChart.Name))
	}
	localChartPath := filepath.Clean(filepath.Join(dir, filepath.FromSlash(chartHome), chartPath))
	if err := rejectPathOutsideBoundary("kustomize helmCharts.name", localChartPath, boundaryRoot); err != nil {
		return err
	}
	if err := rejectSymlinkedPath(boundaryRoot, localChartPath); err != nil {
		return fmt.Errorf("kustomize helmCharts.name %q: %w", helmChart.Name, err)
	}
	return nil
}

func validateWorkspaceHelmValueRefs(boundaryRoot, dir string, helmChart types.HelmChart) error {
	if helmChart.ValuesFile != "" {
		if err := validateWorkspacePathRef(boundaryRoot, dir, "helmCharts.valuesFile", helmChart.ValuesFile); err != nil {
			return err
		}
	}
	for _, valuesFile := range helmChart.AdditionalValuesFiles {
		if err := validateWorkspacePathRef(boundaryRoot, dir, "helmCharts.additionalValuesFiles", valuesFile); err != nil {
			return err
		}
	}
	return nil
}

func validateWorkspacePathBearingRefs(boundaryRoot, dir string, kustomization *types.Kustomization) error {
	if path := kustomization.OpenAPI["path"]; path != "" {
		if err := validateWorkspacePathRef(boundaryRoot, dir, "openapi.path", path); err != nil {
			return err
		}
	}
	for _, configuration := range kustomization.Configurations {
		if err := validateWorkspacePathRef(boundaryRoot, dir, "configurations", configuration); err != nil {
			return err
		}
	}
	for _, generator := range kustomization.Generators {
		if err := validateWorkspacePathRef(boundaryRoot, dir, "generators", generator); err != nil {
			return err
		}
	}
	for _, transformer := range kustomization.Transformers {
		if err := validateWorkspacePathRef(boundaryRoot, dir, "transformers", transformer); err != nil {
			return err
		}
	}
	for _, validator := range kustomization.Validators {
		if err := validateWorkspacePathRef(boundaryRoot, dir, "validators", validator); err != nil {
			return err
		}
	}
	for _, replacement := range kustomization.Replacements {
		if replacement.Path == "" {
			continue
		}
		if err := validateWorkspacePathRef(boundaryRoot, dir, "replacements.path", replacement.Path); err != nil {
			return err
		}
	}
	for _, crd := range kustomization.Crds {
		if err := validateWorkspacePathRef(boundaryRoot, dir, "crds", crd); err != nil {
			return err
		}
	}
	if err := validateWorkspacePatchRefs(boundaryRoot, dir, kustomization); err != nil {
		return err
	}
	if err := validateWorkspaceGeneratorListRefs(boundaryRoot, dir, kustomization); err != nil {
		return err
	}
	return nil
}

func validateWorkspacePatchRefs(boundaryRoot, dir string, kustomization *types.Kustomization) error {
	for _, patch := range kustomization.Patches {
		if patch.Path == "" {
			continue
		}
		if err := validateWorkspacePathRef(boundaryRoot, dir, "patches.path", patch.Path); err != nil {
			return err
		}
	}
	//nolint:staticcheck // Kustomize still accepts patchesJson6902; validate it to block unsafe refs.
	for _, patch := range kustomization.PatchesJson6902 {
		if patch.Path == "" {
			continue
		}
		if err := validateWorkspacePathRef(boundaryRoot, dir, "patchesJson6902.path", patch.Path); err != nil {
			return err
		}
	}
	//nolint:staticcheck // Kustomize still accepts patchesStrategicMerge; validate it to block unsafe refs.
	for _, patch := range kustomization.PatchesStrategicMerge {
		path := string(patch)
		if isInlineStrategicMergePatch(path) {
			continue
		}
		if err := validateWorkspacePathRef(boundaryRoot, dir, "patchesStrategicMerge", path); err != nil {
			return err
		}
	}
	return nil
}

func validateWorkspaceGeneratorListRefs(boundaryRoot, dir string, kustomization *types.Kustomization) error {
	for _, generator := range kustomization.ConfigMapGenerator {
		if err := validateWorkspaceGeneratorRefs(boundaryRoot, dir, "configMapGenerator", generator.KvPairSources); err != nil {
			return err
		}
	}
	for _, generator := range kustomization.SecretGenerator {
		if err := validateWorkspaceGeneratorRefs(boundaryRoot, dir, "secretGenerator", generator.KvPairSources); err != nil {
			return err
		}
	}
	return nil
}

func validateWorkspaceGeneratorRefs(boundaryRoot, dir, field string, sources types.KvPairSources) error {
	for _, source := range sources.FileSources {
		if err := validateWorkspacePathRef(boundaryRoot, dir, field+".files", generatorFileSourcePath(source)); err != nil {
			return err
		}
	}
	for _, source := range sources.EnvSources {
		if err := validateWorkspacePathRef(boundaryRoot, dir, field+".envs", source); err != nil {
			return err
		}
	}
	if sources.EnvSource != "" {
		if err := validateWorkspacePathRef(boundaryRoot, dir, field+".env", sources.EnvSource); err != nil {
			return err
		}
	}
	return nil
}

func validateWorkspacePathRef(boundaryRoot, dir, field, ref string) error {
	_, _, err := validateWorkspaceLocalRef(boundaryRoot, dir, field, ref)
	return err
}

func redactRemoteKustomizeAcquireError(err error, ref kustomizeRemoteRef, opts RenderOptions) string {
	message := remote.RedactCredentialError(err.Error(), opts.RemoteResourceCredentials, opts.RemoteResourceGitCredentials)
	replacements := []struct {
		raw      string
		redacted string
	}{
		{raw: ref.Original, redacted: redactKustomizeRef(ref.Original)},
		{raw: strings.TrimPrefix(ref.Original, "git::"), redacted: redactKustomizeRef(ref.Original)},
		{raw: ref.URL, redacted: redactKustomizeRef(ref.URL)},
		{raw: ref.RepoURL, redacted: remote.RedactGitRepoURL(ref.RepoURL)},
	}
	for _, replacement := range replacements {
		raw := strings.TrimSpace(replacement.raw)
		if raw == "" || replacement.redacted == "" {
			continue
		}
		message = strings.ReplaceAll(message, raw, replacement.redacted)
	}
	if revision := strings.TrimSpace(ref.Revision); revision != "" && revision != "HEAD" {
		message = strings.ReplaceAll(message, revision, "[redacted]")
	}
	for _, value := range rawKustomizeRemoteQueryValues(ref.Original, "ref") {
		if value == "" {
			continue
		}
		message = strings.ReplaceAll(message, value, "[redacted]")
	}
	return message
}

func rawKustomizeRemoteQueryValues(ref, key string) []string {
	withoutPrefix := strings.TrimPrefix(strings.TrimSpace(ref), "git::")
	_, rawQuery, ok := strings.Cut(withoutPrefix, "?")
	if !ok {
		return nil
	}
	rawQuery, _, _ = strings.Cut(rawQuery, "#")
	var out []string
	for _, part := range strings.Split(rawQuery, "&") {
		rawKey, rawValue, hasValue := strings.Cut(part, "=")
		decodedKey, err := url.QueryUnescape(rawKey)
		if err != nil || decodedKey != key || !hasValue {
			continue
		}
		out = append(out, rawValue)
		if decodedValue, err := url.QueryUnescape(rawValue); err == nil {
			out = append(out, decodedValue)
		}
	}
	return out
}

func copyAcquiredRemoteKustomizeResource(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("source path %q is a symlink", src)
	}
	if info.IsDir() {
		return copyRegularTree(src, dst)
	}
	if info.Mode().IsRegular() {
		return copyRegularFile(src, dst)
	}
	return fmt.Errorf("source path %q is not a regular file or directory", src)
}

func renderKustomizeHelmCharts(ctx context.Context, tempRepoRoot, tempSourceRoot, valueFilesBaseDir, namespaceFallback, chartHome string, graphIndex int, helmCharts []types.HelmChart, opts RenderOptions) ([]string, error) {
	acquirer := opts.ChartAcquirer
	if acquirer == nil {
		acquirer = chart.DefaultAcquirer{}
	}

	generatedResources := make([]string, 0, len(helmCharts))
	for i, helmChart := range helmCharts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		baseName := safeGeneratedKustomizeHelmBaseName(helmChart.Name, helmChart.Version)
		generatedName := fmt.Sprintf("%03d-%03d-%s", graphIndex, i, baseName)
		chartRel, err := resolveKustomizeHelmChart(ctx, tempRepoRoot, tempSourceRoot, chartHome, generatedName, helmChart, opts, acquirer)
		if err != nil {
			return nil, err
		}

		helmOpts, err := renderOptionsForKustomizeHelmChart(helmChart, tempRepoRoot, tempSourceRoot, chartRel, valueFilesBaseDir, namespaceFallback, generatedName, opts, acquirer)
		if err != nil {
			return nil, err
		}

		rendered, _, err := (HelmRenderer{}).Render(ctx, ResolvedSource{
			RepoRoot: tempRepoRoot,
			Path:     chartRel,
		}, helmOpts)
		if err != nil {
			return nil, err
		}
		if len(rendered) == 0 {
			continue
		}

		generatedResource := filepath.ToSlash(filepath.Join(".argocd-local", "helm", generatedName+".yaml"))
		generatedPath, err := generatedKustomizeWorkspacePath(tempSourceRoot, generatedResource)
		if err != nil {
			return nil, err
		}
		if err := writeGeneratedHelmManifests(generatedPath, rendered); err != nil {
			return nil, err
		}
		generatedResources = append(generatedResources, generatedResource)
	}
	return generatedResources, nil
}

func resolveKustomizeHelmChart(ctx context.Context, tempRepoRoot, tempSourceRoot, chartHome, generatedName string, helmChart types.HelmChart, opts RenderOptions, acquirer chart.Acquirer) (string, error) {
	if chartRel, ok, err := resolveLocalKustomizeHelmChart(tempRepoRoot, tempSourceRoot, chartHome, helmChart); ok || err != nil {
		return chartRel, err
	}
	if helmChart.Repo == "" {
		return "", fmt.Errorf("kustomize helm chart %q has no local chart and no repo", helmChart.Name)
	}

	request := chart.Request{
		Repository: helmChart.Repo,
		Name:       helmChart.Name,
		Version:    helmChart.Version,
		Kind:       kustomizeHelmChartRepositoryKind(helmChart.Repo),
	}
	result, err := acquirer.Acquire(ctx, request, chart.Options{
		CacheDir:    opts.ChartCacheDir,
		Offline:     opts.OfflineCharts,
		Refresh:     opts.RefreshCharts,
		Credentials: opts.ChartCredentials,
	})
	if err != nil {
		return "", fmt.Errorf("acquire kustomize helm chart %s: %w", helmChart.Name, err)
	}

	chartRel := filepath.ToSlash(filepath.Join(".argocd-local", "charts", generatedName))
	chartDst, err := generatedKustomizeWorkspacePath(tempRepoRoot, chartRel)
	if err != nil {
		return "", err
	}
	if err := copyRegularTree(result.ChartDir, chartDst); err != nil {
		return "", fmt.Errorf("copy acquired helm chart %s: %w", helmChart.Name, err)
	}
	return chartRel, nil
}

func resolveLocalKustomizeHelmChart(repoRoot, kustomizationDir, chartHome string, helmChart types.HelmChart) (string, bool, error) {
	chartPath := filepath.FromSlash(helmChart.Name)
	if helmChart.Repo != "" && helmChart.Version != "" {
		chartPath = filepath.Join(filepath.FromSlash(helmChart.Name+"-"+helmChart.Version), filepath.FromSlash(helmChart.Name))
	}

	path := filepath.Join(kustomizationDir, filepath.FromSlash(chartHome), chartPath)
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	if !info.IsDir() {
		return "", false, nil
	}
	rel, err := relativeManifestPath(repoRoot, path)
	if err != nil {
		return "", false, err
	}
	return rel, true, nil
}

func renderOptionsForKustomizeHelmChart(helmChart types.HelmChart, tempRepoRoot, tempSourceRoot, chartRel, valueFilesBaseDir, namespaceFallback, generatedName string, opts RenderOptions, acquirer chart.Acquirer) (RenderOptions, error) {
	valueFiles := make([]string, 0, 1+len(helmChart.AdditionalValuesFiles))
	valuesObject := cloneValues(helmChart.ValuesInline)
	valuesMergeMode := helmChart.ValuesMerge
	if helmChart.ValuesFile != "" {
		valueFiles = append(valueFiles, helmChart.ValuesFile)
	}
	valueFiles = append(valueFiles, helmChart.AdditionalValuesFiles...)
	if len(helmChart.ValuesInline) != 0 {
		generatedValuesFile, err := writeKustomizeHelmGeneratedValuesFile(tempRepoRoot, tempSourceRoot, chartRel, valueFilesBaseDir, generatedName, helmChart)
		if err != nil {
			return RenderOptions{}, err
		}
		valueFiles = append([]string{generatedValuesFile}, helmChart.AdditionalValuesFiles...)
		valuesObject = nil
		valuesMergeMode = ""
	}

	namespace := helmChart.Namespace
	if namespace == "" {
		namespace = namespaceFallback
	}

	return RenderOptions{
		AppName:           helmChart.Name,
		ReleaseName:       helmChart.ReleaseName,
		Namespace:         namespace,
		KubeVersion:       helmChart.KubeVersion,
		APIVersions:       append([]string(nil), helmChart.ApiVersions...),
		ValueFiles:        valueFiles,
		ValueFilesBaseDir: valueFilesBaseDir,
		ValuesObject:      valuesObject,
		ValuesMergeMode:   valuesMergeMode,
		ChartCacheDir:     opts.ChartCacheDir,
		OfflineCharts:     opts.OfflineCharts,
		RefreshCharts:     opts.RefreshCharts,
		ChartCredentials:  opts.ChartCredentials,
		ChartAcquirer:     acquirer,
		IncludeCRDs:       helmChart.IncludeCRDs,
		IncludeCRDsSet:    true,
		SkipHooks:         helmChart.SkipHooks,
		SkipTests:         helmChart.SkipTests,
	}, nil
}

func writeKustomizeHelmGeneratedValuesFile(tempRepoRoot, tempSourceRoot, chartRel, valueFilesBaseDir, generatedName string, helmChart types.HelmChart) (string, error) {
	primaryValues := map[string]any{}
	loadPrimaryValues, err := shouldLoadHelmValueFiles(helmChart.ValuesMerge, helmChart.ValuesInline)
	if err != nil {
		return "", err
	}
	if loadPrimaryValues {
		valueFilesBase := valueFilesBaseDir
		valueFile := helmChart.ValuesFile
		ignoreMissing := false
		if valueFile == "" {
			valueFilesBase = chartRel
			valueFile = "values.yaml"
			ignoreMissing = true
		}
		primaryValues, err = loadHelmValueFiles(tempRepoRoot, valueFilesBase, nil, []string{valueFile}, ignoreMissing)
		if err != nil {
			return "", err
		}
	}
	values, err := mergeHelmValues(primaryValues, cloneValues(helmChart.ValuesInline), helmChart.ValuesMerge)
	if err != nil {
		return "", err
	}

	generatedRel := filepath.ToSlash(filepath.Join(".argocd-local", "values", generatedName+".yaml"))
	generatedPath, err := generatedKustomizeWorkspacePath(tempSourceRoot, generatedRel)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(generatedPath), 0o755); err != nil {
		return "", err
	}
	data, err := goyaml.Marshal(values)
	if err != nil {
		return "", fmt.Errorf("encode generated helm values %s: %w", generatedRel, err)
	}
	if err := os.WriteFile(generatedPath, data, 0o644); err != nil {
		return "", fmt.Errorf("write generated helm values %s: %w", generatedRel, err)
	}
	return generatedRel, nil
}

func kustomizeHelmChartRepositoryKind(repository string) chart.RepositoryKind {
	if strings.HasPrefix(strings.TrimSpace(repository), "oci://") {
		return chart.RepositoryOCI
	}
	return chart.RepositoryHTTP
}

func safeGeneratedKustomizeHelmBaseName(name, version string) string {
	joined := strings.Trim(strings.TrimSpace(name)+"-"+strings.TrimSpace(version), "-")
	if joined == "" {
		joined = "chart"
	}
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_")
	return replacer.Replace(joined)
}

func kustomizationChartHome(kustomization types.Kustomization) string {
	if kustomization.HelmGlobals != nil && kustomization.HelmGlobals.ChartHome != "" {
		return kustomization.HelmGlobals.ChartHome
	}
	return types.HelmDefaultHome
}

func writeGeneratedHelmManifests(path string, manifests []Manifest) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var buffer bytes.Buffer
	for _, manifest := range manifests {
		if manifest.Object == nil {
			continue
		}
		data, err := goyaml.Marshal(manifest.Object.Object)
		if err != nil {
			return fmt.Errorf("encode generated helm manifest: %w", err)
		}
		if _, err := buffer.WriteString("---\n"); err != nil {
			return err
		}
		if _, err := buffer.Write(data); err != nil {
			return err
		}
	}
	if err := os.WriteFile(path, buffer.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write generated helm manifests %s: %w", path, err)
	}
	return nil
}

func copyRegularTree(srcRoot, dstRoot string) error {
	return copyTree(srcRoot, dstRoot, false)
}

func copyWorkspaceTree(srcRoot, dstRoot string) error {
	return copyTree(srcRoot, dstRoot, true)
}

func copyTree(srcRoot, dstRoot string, preserveSymlinks bool) error {
	srcRoot = filepath.Clean(srcRoot)
	dstRoot = filepath.Clean(dstRoot)

	return filepath.WalkDir(srcRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcRoot, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("copy source path %q escapes source root %q", path, srcRoot)
		}
		if entry.Name() == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		dstPath := filepath.Clean(filepath.Join(dstRoot, rel))
		dstRel, err := filepath.Rel(dstRoot, dstPath)
		if err != nil || dstRel == ".." || strings.HasPrefix(dstRel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("copy destination path %q escapes destination root %q", dstPath, dstRoot)
		}

		if entry.Type()&os.ModeSymlink != 0 {
			if !preserveSymlinks {
				return fmt.Errorf("copy source path %q is a symlink", path)
			}
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
				return err
			}
			return os.Symlink(target, dstPath)
		}

		if entry.IsDir() {
			return os.MkdirAll(dstPath, 0o755)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("copy source path %q is not a regular file", path)
		}
		return copyRegularFile(path, dstPath)
	})
}

func copyRegularFile(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("source path %q is a symlink", src)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("source path %q is not a regular file", src)
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return nil
}

type kustomizeGraphValidator struct {
	repoRoot                  string
	sourceRoot                string
	allowAcquirableRemoteRefs bool
	visited                   map[string]struct{}
	nodes                     []kustomizeGraphNode
}

type kustomizeGraphNode struct {
	Dir                    string
	File                   string
	ManifestPath           string
	InheritedHelmNamespace string
	Kustomization          types.Kustomization
}

func (node kustomizeGraphNode) effectiveHelmNamespace() string {
	if node.Kustomization.Namespace != "" {
		return node.Kustomization.Namespace
	}
	return node.InheritedHelmNamespace
}

func validateKustomizeGraph(ctx context.Context, repoRoot, sourceRoot string) (string, error) {
	manifestPath, _, err := collectKustomizeGraph(ctx, repoRoot, sourceRoot)
	return manifestPath, err
}

func collectKustomizeGraph(ctx context.Context, repoRoot, sourceRoot string) (string, []kustomizeGraphNode, error) {
	validator := kustomizeGraphValidator{
		repoRoot:   filepath.Clean(repoRoot),
		sourceRoot: filepath.Clean(sourceRoot),
		visited:    make(map[string]struct{}),
	}
	manifestPath, err := validator.validateKustomizationDir(ctx, sourceRoot, "")
	return manifestPath, validator.nodes, err
}

func collectKustomizeGraphForPreparation(ctx context.Context, repoRoot, sourceRoot string) (string, []kustomizeGraphNode, error) {
	validator := kustomizeGraphValidator{
		repoRoot:                  filepath.Clean(repoRoot),
		sourceRoot:                filepath.Clean(sourceRoot),
		allowAcquirableRemoteRefs: true,
		visited:                   make(map[string]struct{}),
	}
	manifestPath, err := validator.validateKustomizationDir(ctx, sourceRoot, "")
	return manifestPath, validator.nodes, err
}

func (v *kustomizeGraphValidator) validateKustomizationDir(ctx context.Context, dir, inheritedHelmNamespace string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	dir = filepath.Clean(dir)
	if err := v.rejectRepoRootEscape("kustomization directory", dir); err != nil {
		return "", err
	}
	if _, ok := v.visited[dir]; ok {
		return "", nil
	}
	v.visited[dir] = struct{}{}

	kustomizationFile, err := findKustomizationFile(dir)
	if err != nil {
		return "", err
	}
	manifestPath, err := relativeManifestPath(v.repoRoot, kustomizationFile)
	if err != nil {
		return "", err
	}

	content, err := os.ReadFile(kustomizationFile)
	if err != nil {
		return "", err
	}
	var kustomization types.Kustomization
	if err := goyaml.Unmarshal(content, &kustomization); err != nil {
		return "", fmt.Errorf("decode kustomization %s: %w", manifestPath, err)
	}
	if err := v.validateKustomization(ctx, filepath.Dir(kustomizationFile), manifestPath, &kustomization, inheritedHelmNamespace); err != nil {
		return "", err
	}
	v.nodes = append(v.nodes, kustomizeGraphNode{
		Dir:                    filepath.Dir(kustomizationFile),
		File:                   kustomizationFile,
		ManifestPath:           manifestPath,
		InheritedHelmNamespace: inheritedHelmNamespace,
		Kustomization:          kustomization,
	})
	return manifestPath, nil
}

func findKustomizationFile(root string) (string, error) {
	for _, name := range kustomizationFileNames {
		path := filepath.Join(root, name)
		info, err := os.Lstat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("kustomization file %q is a symlink", path)
		}
		return path, nil
	}
	return "", fmt.Errorf("kustomization file not found in %q", root)
}

func (v *kustomizeGraphValidator) validateKustomization(ctx context.Context, dir, manifestPath string, kustomization *types.Kustomization, inheritedHelmNamespace string) error {
	if err := v.validateHelmFields(dir, kustomization); err != nil {
		return fmt.Errorf("%s: %w", manifestPath, err)
	}
	if err := v.validateOperandRefs(ctx, dir, kustomization, inheritedHelmNamespace); err != nil {
		return fmt.Errorf("%s: %w", manifestPath, err)
	}
	if err := v.validateAuxiliaryRefs(dir, kustomization); err != nil {
		return fmt.Errorf("%s: %w", manifestPath, err)
	}
	if err := v.validatePatchRefs(dir, kustomization); err != nil {
		return fmt.Errorf("%s: %w", manifestPath, err)
	}
	if err := v.validateGeneratorListRefs(dir, kustomization); err != nil {
		return fmt.Errorf("%s: %w", manifestPath, err)
	}
	return nil
}

func (v *kustomizeGraphValidator) validateHelmFields(dir string, kustomization *types.Kustomization) error {
	if len(kustomization.HelmChartInflationGenerator) != 0 {
		return fmt.Errorf("helmChartInflationGenerator is deprecated and unsupported")
	}
	if kustomization.HelmGlobals != nil && kustomization.HelmGlobals.ConfigHome != "" {
		return fmt.Errorf("helmGlobals.configHome is unsupported")
	}
	if len(kustomization.HelmCharts) == 0 {
		return nil
	}
	if _, _, err := v.validateLocalRef(dir, "helmGlobals.chartHome", kustomizationChartHome(*kustomization)); err != nil {
		return err
	}
	for _, helmChart := range kustomization.HelmCharts {
		if helmChart.NameTemplate != "" {
			return fmt.Errorf("helmCharts.nameTemplate is unsupported")
		}
		if helmChart.Devel {
			return fmt.Errorf("helmCharts.devel is unsupported")
		}
		if helmChart.Debug {
			return fmt.Errorf("helmCharts.debug is unsupported")
		}
		if err := v.validateHelmValueRefs(dir, helmChart); err != nil {
			return err
		}
	}
	return nil
}

func (v *kustomizeGraphValidator) validateHelmValueRefs(dir string, helmChart types.HelmChart) error {
	if helmChart.ValuesFile != "" {
		if err := v.validatePathRef(dir, "helmCharts.valuesFile", helmChart.ValuesFile); err != nil {
			return err
		}
	}
	for _, valuesFile := range helmChart.AdditionalValuesFiles {
		if err := v.validatePathRef(dir, "helmCharts.additionalValuesFiles", valuesFile); err != nil {
			return err
		}
	}
	return nil
}

func (v *kustomizeGraphValidator) validateOperandRefs(ctx context.Context, dir string, kustomization *types.Kustomization, inheritedHelmNamespace string) error {
	childInheritedHelmNamespace := inheritedHelmNamespace
	if kustomization.Namespace != "" {
		childInheritedHelmNamespace = kustomization.Namespace
	}
	for _, resource := range kustomization.Resources {
		if err := v.validateResourceRef(ctx, dir, "resources", resource, childInheritedHelmNamespace); err != nil {
			return err
		}
	}
	//nolint:staticcheck // Kustomize still accepts bases; validate it to block unsafe refs.
	for _, base := range kustomization.Bases {
		if err := v.validateKustomizationRef(ctx, dir, "bases", base, childInheritedHelmNamespace); err != nil {
			return err
		}
	}
	for _, component := range kustomization.Components {
		if err := v.validateKustomizationRef(ctx, dir, "components", component, childInheritedHelmNamespace); err != nil {
			return err
		}
	}
	for _, crd := range kustomization.Crds {
		if err := v.validatePathRef(dir, "crds", crd); err != nil {
			return err
		}
	}
	return nil
}

func (v *kustomizeGraphValidator) validateAuxiliaryRefs(dir string, kustomization *types.Kustomization) error {
	if path := kustomization.OpenAPI["path"]; path != "" {
		if err := v.validatePathRef(dir, "openapi.path", path); err != nil {
			return err
		}
	}
	for _, configuration := range kustomization.Configurations {
		if err := v.validatePathRef(dir, "configurations", configuration); err != nil {
			return err
		}
	}
	for _, generator := range kustomization.Generators {
		if err := v.validatePathRef(dir, "generators", generator); err != nil {
			return err
		}
	}
	for _, transformer := range kustomization.Transformers {
		if err := v.validatePathRef(dir, "transformers", transformer); err != nil {
			return err
		}
	}
	for _, validator := range kustomization.Validators {
		if err := v.validatePathRef(dir, "validators", validator); err != nil {
			return err
		}
	}
	for _, replacement := range kustomization.Replacements {
		if replacement.Path == "" {
			continue
		}
		if err := v.validatePathRef(dir, "replacements.path", replacement.Path); err != nil {
			return err
		}
	}
	return nil
}

func (v *kustomizeGraphValidator) validatePatchRefs(dir string, kustomization *types.Kustomization) error {
	for _, patch := range kustomization.Patches {
		if patch.Path == "" {
			continue
		}
		if err := v.validatePathRef(dir, "patches.path", patch.Path); err != nil {
			return err
		}
	}
	//nolint:staticcheck // Kustomize still accepts patchesJson6902; validate it to block unsafe refs.
	for _, patch := range kustomization.PatchesJson6902 {
		if patch.Path == "" {
			continue
		}
		if err := v.validatePathRef(dir, "patchesJson6902.path", patch.Path); err != nil {
			return err
		}
	}
	//nolint:staticcheck // Kustomize still accepts patchesStrategicMerge; validate it to block unsafe refs.
	for _, patch := range kustomization.PatchesStrategicMerge {
		path := string(patch)
		if isInlineStrategicMergePatch(path) {
			continue
		}
		if err := v.validatePathRef(dir, "patchesStrategicMerge", path); err != nil {
			return err
		}
	}
	return nil
}

func (v *kustomizeGraphValidator) validateGeneratorListRefs(dir string, kustomization *types.Kustomization) error {
	for _, generator := range kustomization.ConfigMapGenerator {
		if err := v.validateGeneratorRefs(dir, "configMapGenerator", generator.KvPairSources); err != nil {
			return err
		}
	}
	for _, generator := range kustomization.SecretGenerator {
		if err := v.validateGeneratorRefs(dir, "secretGenerator", generator.KvPairSources); err != nil {
			return err
		}
	}
	return nil
}

func (v *kustomizeGraphValidator) validateResourceRef(ctx context.Context, dir, field, ref, inheritedHelmNamespace string) error {
	if isAcquirableRemoteKustomizeResource(ref) {
		return nil
	}
	path, info, err := v.validateLocalRef(dir, field, ref)
	if err != nil || info == nil || !info.IsDir() {
		return err
	}
	_, err = v.validateKustomizationDir(ctx, path, inheritedHelmNamespace)
	return err
}

func (v *kustomizeGraphValidator) validateKustomizationRef(ctx context.Context, dir, field, ref, inheritedHelmNamespace string) error {
	if v.allowAcquirableRemoteRefs && isAcquirableRemoteKustomizeResource(ref) {
		return nil
	}
	path, info, err := v.validateLocalRef(dir, field, ref)
	if err != nil || info == nil || !info.IsDir() {
		return err
	}
	_, err = v.validateKustomizationDir(ctx, path, inheritedHelmNamespace)
	return err
}

func (v *kustomizeGraphValidator) validatePathRef(dir, field, ref string) error {
	if v.allowAcquirableRemoteRefs {
		if _, _, ok, err := remoteRequestForKustomizeRef(ref); err == nil && ok {
			return nil
		}
	}
	_, _, err := v.validateLocalRef(dir, field, ref)
	return err
}

func (v *kustomizeGraphValidator) validateGeneratorRefs(dir, field string, sources types.KvPairSources) error {
	for _, source := range sources.FileSources {
		if err := v.validatePathRef(dir, field+".files", generatorFileSourcePath(source)); err != nil {
			return err
		}
	}
	for _, source := range sources.EnvSources {
		if err := v.validatePathRef(dir, field+".envs", source); err != nil {
			return err
		}
	}
	if sources.EnvSource != "" {
		if err := v.validatePathRef(dir, field+".env", sources.EnvSource); err != nil {
			return err
		}
	}
	return nil
}

func (v *kustomizeGraphValidator) validateLocalRef(dir, field, ref string) (string, os.FileInfo, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", nil, nil
	}
	if isRemoteKustomizeRef(ref) {
		return "", nil, unsupportedRemoteKustomizeRefError(field, ref)
	}
	if filepath.IsAbs(ref) {
		return "", nil, fmt.Errorf("kustomize %s %q must be relative", field, ref)
	}

	path := filepath.Clean(filepath.Join(dir, filepath.FromSlash(ref)))
	if err := v.rejectRepoRootEscape("kustomize "+field, path); err != nil {
		return "", nil, err
	}
	if err := rejectSymlinkedPath(v.repoRoot, path); err != nil {
		return "", nil, fmt.Errorf("kustomize %s %q: %w", field, ref, err)
	}

	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return path, nil, nil
		}
		return "", nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", nil, fmt.Errorf("kustomize %s %q is a symlink", field, ref)
	}
	return path, info, nil
}

func (v *kustomizeGraphValidator) rejectRepoRootEscape(kind, path string) error {
	return rejectPathOutsideBoundary(kind, path, v.repoRoot)
}

func rejectPathOutsideBoundary(kind, path, boundaryRoot string) error {
	rel, err := filepath.Rel(boundaryRoot, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%s %q escapes repository root %q", kind, path, boundaryRoot)
	}
	return nil
}

func rejectSymlinkedPath(root, path string) error {
	rel, err := filepath.Rel(filepath.Clean(root), path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path %q escapes source root %q", path, root)
	}
	if rel == "." {
		return nil
	}

	current := filepath.Clean(root)
	for _, component := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path %q includes symlink component %q", path, component)
		}
	}
	return nil
}

func generatedKustomizeWorkspacePath(root, rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return "", fmt.Errorf("generated kustomize path must not be empty")
	}
	cleanRel := filepath.Clean(filepath.FromSlash(rel))
	if filepath.IsAbs(cleanRel) || cleanRel == "." || cleanRel == ".." || strings.HasPrefix(cleanRel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("generated kustomize path %q must be relative inside %q", rel, root)
	}
	generatedPath := filepath.Join(root, cleanRel)
	if err := rejectPathOutsideBoundary("generated kustomize path", generatedPath, root); err != nil {
		return "", err
	}
	if err := rejectSymlinkedPath(root, generatedPath); err != nil {
		return "", fmt.Errorf("generated kustomize path %q: %w", rel, err)
	}
	return generatedPath, nil
}

func isSupportedRemoteKustomizeFileResource(ref string) bool {
	parsed, ok, err := parseKustomizeRemoteRef(ref)
	return err == nil && ok && parsed.Kind == kustomizeRemoteHTTPFile
}

func isAcquirableRemoteKustomizeResource(ref string) bool {
	_, _, ok, err := remoteRequestForKustomizeRef(ref)
	return err == nil && ok
}

func remoteRequestForKustomizeRef(ref string) (remote.Request, kustomizeRemoteRef, bool, error) {
	parsed, ok, err := parseKustomizeRemoteRef(ref)
	if err != nil || !ok {
		return remote.Request{}, parsed, ok, err
	}
	switch parsed.Kind {
	case kustomizeRemoteHTTPFile:
		return remote.Request{
			URL:  parsed.URL,
			Kind: remote.RequestHTTPFile,
		}, parsed, true, nil
	case kustomizeRemoteGit:
		return remote.Request{
			URL:      parsed.Original,
			Kind:     remote.RequestGitRepo,
			RepoURL:  parsed.RepoURL,
			Revision: parsed.Revision,
		}, parsed, true, nil
	default:
		return remote.Request{}, parsed, false, nil
	}
}

func acquiredRemoteKustomizePath(acquired remote.Result, ref kustomizeRemoteRef) (string, error) {
	acquiredPath := strings.TrimSpace(acquired.Path)
	if acquiredPath == "" {
		return "", fmt.Errorf("remote kustomize resource %s returned an empty path", redactKustomizeRef(ref.Original))
	}

	switch ref.Kind {
	case kustomizeRemoteHTTPFile:
		return acquiredPath, nil
	case kustomizeRemoteGit:
		subpath := path.Clean(strings.TrimPrefix(ref.Subpath, "/"))
		if subpath == "." || subpath == ".." || strings.HasPrefix(subpath, "../") {
			return "", fmt.Errorf("remote kustomize resource %s subpath %q escapes acquired repository", redactKustomizeRef(ref.Original), ref.Subpath)
		}
		repoRoot := filepath.Clean(acquiredPath)
		info, err := os.Lstat(repoRoot)
		if err != nil {
			return "", fmt.Errorf("remote kustomize resource %s acquired repository %q: %w", redactKustomizeRef(ref.Original), repoRoot, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("remote kustomize resource %s returned symlinked repository root %q", redactKustomizeRef(ref.Original), repoRoot)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("remote kustomize resource %s acquired repository %q is not a directory", redactKustomizeRef(ref.Original), repoRoot)
		}
		target := filepath.Clean(filepath.Join(repoRoot, filepath.FromSlash(subpath)))
		if err := rejectSymlinkedPath(repoRoot, target); err != nil {
			return "", fmt.Errorf("remote kustomize resource %s subpath %q: %w", redactKustomizeRef(ref.Original), ref.Subpath, err)
		}
		inside, _, err := remote.IsPathInsideAny(target, []string{repoRoot})
		if err != nil {
			return "", err
		}
		if !inside {
			return "", fmt.Errorf("remote kustomize resource %s subpath %q escapes acquired repository %q", redactKustomizeRef(ref.Original), ref.Subpath, repoRoot)
		}
		return target, nil
	default:
		return "", fmt.Errorf("unsupported remote kustomize resource kind %q", ref.Kind)
	}
}

func unsupportedRemoteKustomizeRefError(field, ref string) error {
	return fmt.Errorf("kustomize %s %q is a remote ref; remote Kustomize refs are unsupported", field, redactKustomizeRef(ref))
}

func redactKustomizeRef(ref string) string {
	return redactKustomizeRemoteRef(ref)
}

func generatorFileSourcePath(source string) string {
	_, ref, _ := splitGeneratorFileSource(source)
	return ref
}

func isInlineStrategicMergePatch(patch string) bool {
	return strings.Contains(patch, "\n")
}

func isRemoteKustomizeRef(ref string) bool {
	trimmed := strings.TrimSpace(ref)
	if _, ok, err := parseKustomizeRemoteRef(trimmed); ok || err != nil {
		return true
	}
	lower := strings.ToLower(trimmed)
	if strings.Contains(trimmed, "://") {
		return true
	}
	if strings.HasPrefix(lower, "git::") || strings.HasPrefix(lower, "git@") {
		return true
	}
	if isColonStyleKustomizeRemoteRef(trimmed) {
		return true
	}
	parsed, err := url.Parse(trimmed)
	if err == nil && parsed.Scheme != "" {
		return true
	}
	if hasRemoteQueryOrFragmentSyntax(trimmed) {
		return true
	}
	if strings.Contains(lower, "?ref=") && strings.Contains(lower, "//") {
		return true
	}
	for _, host := range []string{"github.com/", "gitlab.com/", "bitbucket.org/"} {
		if strings.HasPrefix(lower, host) {
			return true
		}
	}
	return false
}

func hasRemoteQueryOrFragmentSyntax(ref string) bool {
	if !strings.ContainsAny(ref, "?#") {
		return false
	}
	refPath := ref
	if before, _, ok := strings.Cut(refPath, "?"); ok {
		refPath = before
	}
	if before, _, ok := strings.Cut(refPath, "#"); ok {
		refPath = before
	}
	if !strings.Contains(refPath, "/") {
		return false
	}
	hostCandidate, _, _ := strings.Cut(refPath, "/")
	if user, host, ok := strings.Cut(hostCandidate, "@"); ok {
		return user != "" && host != "" && looksLikeRemoteHost(strings.ToLower(host))
	}
	hostCandidate = strings.ToLower(hostCandidate)
	return isKnownGitHost(hostCandidate) || looksLikeRemoteHost(hostCandidate)
}

func isColonStyleKustomizeRemoteRef(ref string) bool {
	beforeColon, afterColon, ok := strings.Cut(ref, ":")
	if !ok || beforeColon == "" || afterColon == "" {
		return false
	}
	if strings.ContainsAny(beforeColon, `/\`) {
		return false
	}

	host := beforeColon
	if user, afterAt, ok := strings.Cut(beforeColon, "@"); ok {
		return user != "" && afterAt != "" && !strings.ContainsAny(afterAt, `/\`)
	}
	host = strings.ToLower(host)
	return isKnownGitHost(host) || looksLikeRemoteHost(host)
}

func isKnownGitHost(host string) bool {
	for _, known := range []string{"github.com", "gitlab.com", "bitbucket.org"} {
		if host == known {
			return true
		}
	}
	return false
}

func looksLikeRemoteHost(host string) bool {
	return strings.Contains(host, ".")
}
