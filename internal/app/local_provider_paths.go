package app

import (
	"fmt"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/pathsafety"
	"github.com/sholdee/drydock/internal/render"

	"os"
	"path/filepath"
	"slices"
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

// selectLocalRenderer picks the renderer of a local source as the Argo CD
// v3.6.1 repo-server picks its type (GetAppSourceType,
// reposerver/repository/repository.go:1955-1975). A Helm or Kustomize source
// whose tool gates turn off renders as a plain directory, also when the
// spec names the type.
func selectLocalRenderer(source render.ResolvedSource, gates sourceTypeGates) (render.Renderer, error) {
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
		if !gates.enabled(source.ExplicitType) {
			return render.DirectoryRenderer{}, nil
		}
		return render.HelmRenderer{}, nil
	case argoappv1.ApplicationSourceTypeKustomize:
		if !gates.enabled(source.ExplicitType) {
			return render.DirectoryRenderer{}, nil
		}
		return render.KustomizeRenderer{}, nil
	case argoappv1.ApplicationSourceTypePlugin:
	default:
		return nil, fmt.Errorf("unsupported explicit source type %q", source.ExplicitType)
	}

	sourceType, err := discoverLocalSourceType(root, gates)
	if err != nil {
		return nil, err
	}
	switch sourceType {
	case argoappv1.ApplicationSourceTypeHelm:
		return render.HelmRenderer{}, nil
	case argoappv1.ApplicationSourceTypeKustomize:
		return render.KustomizeRenderer{}, nil
	case argoappv1.ApplicationSourceTypeDirectory, argoappv1.ApplicationSourceTypePlugin:
		return render.DirectoryRenderer{}, nil
	default:
		return render.DirectoryRenderer{}, nil
	}
}

// discoverLocalSourceType picks the source type of a source directory
// without an explicit type, as the Argo CD v3.6.1 repo-server does
// (discovery.Discover, util/app/discovery/discovery.go:50-70). The
// repo-server walks the directory in lexical order and records a type for
// each file in it: Helm for any name that ends in "Chart.yaml", such as
// "traefik-HelmChart.yaml", and Kustomize for a kustomization file name.
// A later file overwrites an earlier one, so the last match in name order
// wins. Directories never match, also when their name does, and a symlink
// counts as a file because the walk does not follow it. Only the files
// directly in the directory decide its type; the walk records the files of
// each subdirectory under that subdirectory. A file of a type that gates
// turn off records nothing. An empty result means a directory source.
func discoverLocalSourceType(root string, gates sourceTypeGates) (argoappv1.ApplicationSourceType, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	var sourceType argoappv1.ApplicationSourceType
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, "Chart.yaml") && gates.enabled(argoappv1.ApplicationSourceTypeHelm) {
			sourceType = argoappv1.ApplicationSourceTypeHelm
		}
		if slices.Contains(render.KustomizationFileNames, name) && gates.enabled(argoappv1.ApplicationSourceTypeKustomize) {
			sourceType = argoappv1.ApplicationSourceTypeKustomize
		}
	}
	return sourceType, nil
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
	for component := range strings.SplitSeq(sourcePath, string(filepath.Separator)) {
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
