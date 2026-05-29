package app

import (
	"fmt"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/pathsafety"
	"github.com/sholdee/drydock/internal/render"

	"os"
	"path/filepath"
	"strings"
)

func anchorLocalRefRoots(repoRoot string, refRoots map[string]string) (map[string]string, error) {
	if len(refRoots) == 0 {
		return map[string]string{}, nil
	}
	out := make(map[string]string, len(refRoots))
	for key, root := range refRoots {
		if filepath.IsAbs(root) {
			return nil, fmt.Errorf("absolute ref root %s %q must be supplied through repo-map or source resolution", key, root)
		}
		clean, err := cleanLocalSourcePath(root)
		if err != nil {
			return nil, fmt.Errorf("ref root %s %q: %w", key, root, err)
		}
		out[key] = filepath.Join(repoRoot, clean)
	}
	return out, nil
}

func selectLocalRenderer(source render.ResolvedSource) (render.Renderer, error) {
	sourcePath, err := cleanLocalSourcePath(source.Path)
	if err != nil {
		return nil, err
	}
	if err := rejectLocalSymlinkComponents(source.RepoRoot, sourcePath); err != nil {
		return nil, err
	}

	root := filepath.Join(source.RepoRoot, sourcePath)

	switch source.ExplicitType {
	case "":
	case argoappv1.ApplicationSourceTypeDirectory:
		return render.DirectoryRenderer{}, nil
	case argoappv1.ApplicationSourceTypeHelm:
		return render.HelmRenderer{}, nil
	case argoappv1.ApplicationSourceTypeKustomize:
		return render.KustomizeRenderer{}, nil
	case argoappv1.ApplicationSourceTypePlugin:
	default:
		return nil, fmt.Errorf("unsupported explicit source type %q", source.ExplicitType)
	}

	for _, name := range []string{"kustomization.yaml", "kustomization.yml", "Kustomization"} {
		if exists, err := localPathExists(filepath.Join(root, name)); err != nil {
			return nil, err
		} else if exists {
			return render.KustomizeRenderer{}, nil
		}
	}
	if exists, err := localPathExists(filepath.Join(root, "Chart.yaml")); err != nil {
		return nil, err
	} else if exists {
		return render.HelmRenderer{}, nil
	}
	return render.DirectoryRenderer{}, nil
}

func cleanLocalSourcePath(path string) (string, error) {
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("source path %q must be relative", path)
	}

	if filepath.Clean(path) == "." {
		return ".", nil
	}
	clean, ok := pathsafety.CleanRelative(path)
	if !ok {
		return "", fmt.Errorf("source path %q escapes repository root", path)
	}
	return clean, nil
}

func rejectLocalSymlinkComponents(repoRoot, sourcePath string) error {
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
				return fmt.Errorf("source path %q does not exist", sourcePath)
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("source path %q includes symlink component %q", sourcePath, component)
		}
	}
	return nil
}

func localPathExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}
