package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/home-operations/argocd-local/internal/appset"
	"github.com/home-operations/argocd-local/internal/diagnostic"
	"github.com/home-operations/argocd-local/internal/discovery"
	"github.com/home-operations/argocd-local/internal/render"
)

type BuildRequest struct {
	Path   string
	Strict bool
}

type BuildResult struct {
	Applications []argoappv1.Application
	Manifests    []render.Manifest
	Diagnostics  []diagnostic.Diagnostic
}

type Orchestrator struct{}

func (o Orchestrator) ListApplications(_ context.Context, request BuildRequest) (BuildResult, error) {
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
			if errors.Is(err, appset.ErrUnsupportedGenerator) && len(diags) > 0 {
				diags = normalizeDiagnostics(diags, request.Strict, true)
				result.Diagnostics = append(result.Diagnostics, diags...)
				if request.Strict {
					return result, diagnosticsError(diags, err)
				}
				continue
			}
			return result, diagnosticsError(diags, err)
		}
		diags = normalizeDiagnostics(diags, request.Strict, false)
		result.Diagnostics = append(result.Diagnostics, diags...)
		if err := diagnosticFailure(diags, request.Strict); err != nil {
			return result, err
		}
		for _, app := range generated {
			result.Applications = append(result.Applications, app.Application)
		}
	}

	return result, nil
}

func (o Orchestrator) Build(ctx context.Context, request BuildRequest) (BuildResult, error) {
	result, err := o.ListApplications(ctx, request)
	if err != nil {
		return result, err
	}

	root := request.Path
	if root == "" {
		root = "."
	}

	provider := localProvider{repoRoot: root}
	for _, application := range result.Applications {
		rendered, err := RenderApplication(ctx, application, provider)
		if err != nil {
			return result, err
		}
		rendered.Diagnostics = normalizeDiagnostics(rendered.Diagnostics, request.Strict, false)
		result.Diagnostics = append(result.Diagnostics, rendered.Diagnostics...)
		if err := diagnosticFailure(rendered.Diagnostics, request.Strict); err != nil {
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
		renderer, err := selectLocalRenderer(source)
		if err != nil {
			return nil, nil, err
		}
		return renderer.Render(ctx, source, opts)
	}
	if source.Chart != "" {
		return nil, nil, fmt.Errorf("remote chart source %q requires a local chart path; repository chart fetching is not wired", source.Chart)
	}
	return nil, nil, nil
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
	if exists, err := localPathExists(filepath.Join(root, "Chart.yaml")); err != nil {
		return nil, err
	} else if exists {
		return render.HelmRenderer{}, nil
	}
	for _, name := range []string{"kustomization.yaml", "kustomization.yml", "Kustomization"} {
		if exists, err := localPathExists(filepath.Join(root, name)); err != nil {
			return nil, err
		} else if exists {
			return render.KustomizeRenderer{}, nil
		}
	}
	return render.DirectoryRenderer{}, nil
}

func cleanLocalSourcePath(path string) (string, error) {
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("source path %q must be relative", path)
	}

	clean := filepath.Clean(path)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
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

func diagnosticsError(diags []diagnostic.Diagnostic, err error) error {
	if len(diags) == 0 {
		return err
	}
	return fmt.Errorf("%w: %s", err, diags[0].Message)
}

func normalizeDiagnostics(diags []diagnostic.Diagnostic, strict, forceWarning bool) []diagnostic.Diagnostic {
	if len(diags) == 0 {
		return nil
	}
	out := make([]diagnostic.Diagnostic, len(diags))
	copy(out, diags)
	for i := range out {
		if forceWarning {
			out[i].Severity = diagnostic.SeverityWarning
		}
		if strict {
			out[i].Severity = diagnostic.SeverityError
		}
	}
	return out
}

func diagnosticFailure(diags []diagnostic.Diagnostic, strict bool) error {
	for _, diag := range diags {
		if strict || diag.Severity == diagnostic.SeverityError {
			return fmt.Errorf("diagnostic %s: %s", diag.Category, diag.Message)
		}
	}
	return nil
}
