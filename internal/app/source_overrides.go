package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	jsonpatch "github.com/evanphx/json-patch"
	"github.com/sholdee/drydock/internal/pathsafety"
	"github.com/sholdee/drydock/internal/render"
	"sigs.k8s.io/yaml"
)

const (
	repoSourceFile = ".argocd-source.yaml"
	appSourceFile  = ".argocd-source-%s.yaml"
)

func (p localProvider) PrepareSource(ctx context.Context, application argoappv1.Application, sourcePlan SourcePlan) (SourcePlan, error) {
	if sourcePlan.RefOnly {
		return sourcePlan, nil
	}
	// Argo CD merges .argocd-source*.yaml at the OCI app path
	// (repository.go:1920-1923 at v3.5.4), which is the artifact root when
	// path is empty, so the pathless oci:// shape prepares exactly like
	// `path: .` and chart is ignored. The default applies to this local copy
	// only: sourcePlan.Source.Path stays as written, so renderSourceContext
	// and mergeArgocdSourceOverrides' re-pin keep reading the spec.
	source := normalizeOCISource(render.ResolvedSource{
		Path:           sourcePlan.Source.Path,
		Chart:          sourcePlan.Source.Chart,
		RepoURL:        sourcePlan.Source.RepoURL,
		TargetRevision: sourcePlan.Source.TargetRevision,
		ExplicitType:   sourcePlan.ExplicitType,
	})
	if strings.TrimSpace(source.Path) == "" {
		return sourcePlan, nil
	}

	sourceRoot, err := p.resolveSourceRoot(ctx, source)
	if err != nil {
		return sourcePlan, err
	}
	// The repo-server resolves the $ref value files of the spec before it
	// checks the tree, so an unknown $ref fails first
	// (resolveReferencedSources, reposerver/repository/repository.go:391
	// and 586-614 at v3.6.1).
	if err := specHelmRefError(application, sourcePlan); err != nil {
		return sourcePlan, err
	}
	// The repo-server checks the tree and then the $ref repositories
	// before GenerateManifests reads the overrides
	// (reposerver/repository/repository.go:499-514, 870-959, and 967 at
	// v3.6.1). A chart-only source has no path, so it never gets here.
	if err := p.checkOutOfBoundsSymlinks(ctx, sourceRoot, sourceOutOfBoundsSymlinkScope(source)); err != nil {
		return sourcePlan, err
	}
	if err := p.checkSpecHelmRefRootsOutOfBoundsSymlinks(ctx, application, sourcePlan); err != nil {
		return sourcePlan, err
	}

	sourcePath, err := cleanLocalSourcePath(source.Path)
	if err != nil {
		return sourcePlan, err
	}
	sourceDir := filepath.Join(sourceRoot, sourcePath)
	merged, err := mergeArgocdSourceOverrides(sourcePlan.Source, sourceRoot, sourceDir, p.overrideInstanceName(application))
	if err != nil {
		return sourcePlan, err
	}
	explicitType, err := merged.ExplicitType()
	if err != nil {
		return sourcePlan, err
	}

	sourcePlan.Source = merged
	sourcePlan.SourceRoot = sourceRoot
	sourcePlan.ExplicitType = ""
	if explicitType != nil {
		sourcePlan.ExplicitType = *explicitType
	}
	return sourcePlan, nil
}

// mergeArgocdSourceOverrides merges the .argocd-source.yaml and
// .argocd-source-<app>.yaml files in sourceDir into source, as the Argo CD
// v3.6.1 repo-server's mergeSourceParameters does
// (reposerver/repository/repository.go:1903-1955). A missing file, a
// dangling symlink, and a directory are skipped there, and so they are here.
// drydock reads an override file only when it is a regular file whose real
// path is under root and outside .git (readArgocdSourceOverride).
func mergeArgocdSourceOverrides(source argoappv1.ApplicationSource, root, sourceDir, appName string) (argoappv1.ApplicationSource, error) {
	merged := *source.DeepCopy()
	overrides := []string{filepath.Join(sourceDir, repoSourceFile)}
	if strings.TrimSpace(appName) != "" {
		overrides = append(overrides, filepath.Join(sourceDir, fmt.Sprintf(appSourceFile, appName)))
	}

	for _, filename := range overrides {
		patch, ok, err := readArgocdSourceOverride(root, filename)
		if err != nil {
			return source, err
		}
		if !ok {
			continue
		}

		data, err := json.Marshal(merged)
		if err != nil {
			return source, fmt.Errorf("%s: %w", filename, err)
		}
		patch, err = yaml.YAMLToJSON(patch)
		if err != nil {
			return source, fmt.Errorf("%s: %w", filename, err)
		}
		data, err = jsonpatch.MergePatch(data, patch)
		if err != nil {
			return source, fmt.Errorf("%s: %w", filename, err)
		}
		if err := json.Unmarshal(data, &merged); err != nil {
			return source, fmt.Errorf("%s: %w", filename, err)
		}
	}

	merged.RepoURL = source.RepoURL
	merged.Path = source.Path
	merged.Chart = source.Chart
	merged.TargetRevision = source.TargetRevision
	merged.Ref = source.Ref
	merged.Name = source.Name
	return merged, nil
}

// overrideInstanceName is the name the Argo CD repo-server uses for the
// per-Application override file: the Application instance name, so an
// Application outside the controller namespace reads
// .argocd-source-<namespace>_<name>.yaml and a bare-name file is ignored.
func (p localProvider) overrideInstanceName(application argoappv1.Application) string {
	controllerNamespace := p.controllerNamespace
	if controllerNamespace == "" {
		controllerNamespace = defaultArgoCDControllerNamespace
	}
	return application.InstanceName(controllerNamespace)
}

// readArgocdSourceOverride reads one override file. It reports false for a
// file the repo-server skips: a missing file, a dangling symlink, or a
// directory. drydock reads the file only when its real path is under root
// and outside .git, also through a symlink or a symlinked directory, and
// only when it is a regular file, so a named pipe or a device cannot block
// or flood the render. The repo-server would read such a file, but its
// out-of-bounds symlink check fails a repository with a symlink that leaves
// it unless reposerver.allow.oob.symlinks is true.
func readArgocdSourceOverride(root, filename string) ([]byte, bool, error) {
	if _, err := os.Lstat(filename); err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	real, err := filepath.EvalSymlinks(filename)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", filename, err)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", filename, err)
	}
	rel, err := filepath.Rel(realRoot, real)
	if err != nil || filepath.IsAbs(rel) || pathsafety.RelEscapes(rel) {
		return nil, false, fmt.Errorf("%s: override file resolves to a path outside the repository, which drydock never reads", filename)
	}
	if pathsafety.RelEntersGit(rel) {
		return nil, false, fmt.Errorf("%s: override file resolves into a .git directory, which drydock never reads", filename)
	}
	info, err := os.Stat(real)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", filename, err)
	}
	if info.IsDir() {
		return nil, false, nil
	}
	if !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("%s: override file is not a regular file", filename)
	}
	data, err := os.ReadFile(real)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", filename, err)
	}
	return data, true, nil
}
