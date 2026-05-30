package render

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"strings"

	"sigs.k8s.io/kustomize/api/types"
)

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

	for i, patch := range kustomization.PatchesJson6902 { //nolint:staticcheck // Kustomize still accepts patchesJson6902; rewrite remote refs for parity.
		if patch.Path == "" {
			continue
		}
		rewritten, ok, err := w.rewriteKustomizePathRef(ctx, node, "patchesJson6902.path", patch.Path, remotePathFile)
		if err != nil {
			return err
		}
		if ok {
			kustomization.PatchesJson6902[i].Path = rewritten //nolint:staticcheck // Kustomize still accepts patchesJson6902; rewrite remote refs for parity.
		}
	}

	for i, patch := range kustomization.PatchesStrategicMerge { //nolint:staticcheck // Kustomize still accepts patchesStrategicMerge; rewrite remote refs for parity.
		ref := string(patch)
		if isInlineStrategicMergePatch(ref) {
			continue
		}
		rewritten, ok, err := w.rewriteKustomizePathRef(ctx, node, "patchesStrategicMerge", ref, remotePathFile)
		if err != nil {
			return err
		}
		if ok {
			kustomization.PatchesStrategicMerge[i] = types.PatchStrategicMerge(rewritten) //nolint:staticcheck // Kustomize still accepts patchesStrategicMerge; rewrite remote refs for parity.
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
	case kustomizeRemoteNone:
		return "", fmt.Errorf("unsupported remote generator file source kind %q", parsed.Kind)
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

func generatorFileSourcePath(source string) string {
	_, ref, _ := splitGeneratorFileSource(source)
	return ref
}
