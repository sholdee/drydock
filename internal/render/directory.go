package render

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/home-operations/argocd-local/internal/diagnostic"
	"github.com/home-operations/argocd-local/internal/manifest"
)

type DirectoryRenderer struct{}

func (DirectoryRenderer) Render(ctx context.Context, source ResolvedSource, _ RenderOptions) ([]Manifest, []diagnostic.Diagnostic, error) {
	root := filepath.Join(source.RepoRoot, source.Path)
	var out []Manifest

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			if path != root && strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !isManifestFile(path) {
			return nil
		}

		file, err := os.Open(path)
		if err != nil {
			return err
		}
		docs, decodeErr := manifest.DecodeDocuments(renderPath(source.RepoRoot, path), file)
		closeErr := file.Close()
		if decodeErr != nil {
			return decodeErr
		}
		if closeErr != nil {
			return closeErr
		}

		for _, doc := range docs {
			out = append(out, Manifest{
				Path:   doc.Path,
				Object: doc.Object,
			})
		}
		return nil
	})
	return out, nil, err
}

func isManifestFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".yaml" || ext == ".yml" || ext == ".json"
}

func renderPath(repoRoot, path string) string {
	rel, err := filepath.Rel(repoRoot, path)
	if err != nil || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return path
	}
	return rel
}
