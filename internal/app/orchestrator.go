package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/home-operations/argocd-local/internal/appset"
	"github.com/home-operations/argocd-local/internal/diagnostic"
	"github.com/home-operations/argocd-local/internal/discovery"
	"github.com/home-operations/argocd-local/internal/render"
)

type BuildRequest struct {
	Path string
}

type BuildResult struct {
	Applications []argoappv1.Application
	Manifests    []render.Manifest
}

type Orchestrator struct{}

func (Orchestrator) Build(ctx context.Context, request BuildRequest) (BuildResult, error) {
	root := request.Path
	if root == "" {
		root = "."
	}

	discovered, err := discovery.Scan(root, discovery.Options{})
	if err != nil {
		return BuildResult{}, err
	}

	var result BuildResult
	for _, appFile := range discovered.Applications {
		result.Applications = append(result.Applications, appFile.Application)
	}

	for _, appSetPath := range discovered.ApplicationSetPath {
		data, err := os.ReadFile(filepath.Join(root, appSetPath))
		if err != nil {
			return result, err
		}
		generated, diags, err := appset.GenerateFromYAML(root, appSetPath, data)
		if err != nil {
			return result, diagnosticsError(diags, err)
		}
		for _, app := range generated {
			result.Applications = append(result.Applications, app.Application)
		}
	}

	provider := localProvider{repoRoot: root}
	for _, application := range result.Applications {
		rendered, err := RenderApplication(ctx, application, provider)
		if err != nil {
			return result, err
		}
		result.Manifests = append(result.Manifests, rendered.Manifests...)
	}

	return result, nil
}

type localProvider struct {
	repoRoot string
}

func (p localProvider) RenderSource(ctx context.Context, source render.ResolvedSource, opts render.RenderOptions) ([]render.Manifest, []diagnostic.Diagnostic, error) {
	source.RepoRoot = p.repoRoot
	if source.Path != "" {
		return render.KustomizeRenderer{}.Render(ctx, source, opts)
	}
	if source.Chart != "" {
		return render.HelmRenderer{}.Render(ctx, source, opts)
	}
	return nil, nil, nil
}

func diagnosticsError(diags []diagnostic.Diagnostic, err error) error {
	if len(diags) == 0 {
		return err
	}
	return fmt.Errorf("%w: %s", err, diags[0].Message)
}
