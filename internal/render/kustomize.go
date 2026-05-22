package render

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/home-operations/argocd-local/internal/diagnostic"
	"github.com/home-operations/argocd-local/internal/manifest"
	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/kyaml/filesys"
)

type KustomizeRenderer struct{}

func (KustomizeRenderer) Render(ctx context.Context, source ResolvedSource, _ RenderOptions) ([]Manifest, []diagnostic.Diagnostic, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}

	root, err := sourceRoot(source)
	if err != nil {
		return nil, nil, err
	}

	manifestPath, err := kustomizationManifestPath(source.RepoRoot, root)
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

func kustomizationManifestPath(repoRoot, root string) (string, error) {
	for _, name := range []string{"kustomization.yaml", "kustomization.yml", "Kustomization"} {
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
		return relativeManifestPath(repoRoot, path)
	}
	return relativeManifestPath(repoRoot, filepath.Join(root, "kustomization.yaml"))
}
