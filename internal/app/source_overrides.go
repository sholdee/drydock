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

	sourcePath, err := cleanLocalSourcePath(source.Path)
	if err != nil {
		return sourcePlan, err
	}
	sourceDir := filepath.Join(sourceRoot, sourcePath)
	merged, err := mergeArgocdSourceOverrides(sourcePlan.Source, sourceDir, p.overrideInstanceName(application))
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

func mergeArgocdSourceOverrides(source argoappv1.ApplicationSource, sourceDir, appName string) (argoappv1.ApplicationSource, error) {
	merged := *source.DeepCopy()
	overrides := []string{filepath.Join(sourceDir, repoSourceFile)}
	if strings.TrimSpace(appName) != "" {
		overrides = append(overrides, filepath.Join(sourceDir, fmt.Sprintf(appSourceFile, appName)))
	}

	for _, filename := range overrides {
		info, err := os.Stat(filename)
		switch {
		case os.IsNotExist(err):
			continue
		case err != nil:
			return source, err
		case info.IsDir():
			continue
		}

		data, err := json.Marshal(merged)
		if err != nil {
			return source, fmt.Errorf("%s: %w", filename, err)
		}
		patch, err := os.ReadFile(filename)
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
