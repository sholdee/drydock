package render

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/chart"
	helmchart "helm.sh/helm/v4/pkg/chart"
	"helm.sh/helm/v4/pkg/chart/common"
	"helm.sh/helm/v4/pkg/chart/loader"
	chartv2 "helm.sh/helm/v4/pkg/chart/v2"
	chartv2util "helm.sh/helm/v4/pkg/chart/v2/util"
)

func prepareHelmDependencyWorkspace(ctx context.Context, chartPath, chartSourcePath string, chrt helmchart.Charter, opts RenderOptions) (helmchart.Charter, func(), error) {
	requests, err := missingAcquirableHelmDependencyRequests(chrt, opts.OCIChartRepositories)
	if err != nil {
		return nil, nil, err
	}
	if len(requests) == 0 {
		return chrt, nil, nil
	}

	tempRepoRoot, err := os.MkdirTemp("", "drydock-helm-*")
	if err != nil {
		return nil, nil, fmt.Errorf("create temporary helm dependency workspace: %w", err)
	}
	cleanup := func() {
		_ = os.RemoveAll(tempRepoRoot)
	}

	tempChartPath := filepath.Join(tempRepoRoot, filepath.FromSlash(filepath.ToSlash(chartSourcePath)))
	if chartSourcePath == "." {
		tempChartPath = tempRepoRoot
	}
	if err := copyRegularTree(chartPath, tempChartPath); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("copy helm chart for dependency acquisition: %w", err)
	}

	acquirer := opts.ChartAcquirer
	if acquirer == nil {
		acquirer = chart.DefaultAcquirer{}
	}
	for _, request := range requests {
		if err := ctx.Err(); err != nil {
			cleanup()
			return nil, nil, err
		}
		result, err := acquirer.Acquire(ctx, request, chart.Options{
			CacheDir:        opts.ChartCacheDir,
			Offline:         opts.OfflineCharts,
			Refresh:         opts.RefreshCharts,
			ForbiddenRoots:  append([]string(nil), opts.ChartForbiddenRoots...),
			PassCredentials: opts.PassCredentials,
			Credentials:     opts.ChartCredentials,
		})
		if err != nil {
			recordChartCacheEvent(opts, request, err, chart.Result{})
			cleanup()
			return nil, nil, fmt.Errorf("acquire helm chart dependency %s: %s", request.Name, redactKustomizeChartAcquireError(err, request.Repository, opts.ChartCredentials))
		}
		recordChartCacheEvent(opts, request, nil, result)

		dst, err := replaceHelmDependencyChartTargets(tempChartPath, request.Name)
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		if err := copyRegularTree(result.ChartDir, dst); err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("copy acquired helm chart dependency %s: %w", request.Name, err)
		}
	}

	prepared, err := loader.Load(tempChartPath)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("load helm chart with acquired dependencies %s: %w", chartSourcePath, err)
	}
	return prepared, cleanup, nil
}

func missingAcquirableHelmDependencyRequests(chrt helmchart.Charter, ociRepositories map[string]bool) ([]chart.Request, error) {
	missing, err := missingHelmDependencies(chrt)
	if err != nil {
		return nil, err
	}
	requests := make([]chart.Request, 0, len(missing))
	for _, dependency := range missing {
		dependency = helmLockedDependency(chrt, dependency)
		request, ok, err := helmDependencyChartRequest(dependency, ociRepositories)
		if err != nil {
			return nil, err
		}
		if ok {
			requests = append(requests, request)
		}
	}
	return requests, nil
}

func helmDependencyChartRequest(dependency helmchart.Dependency, ociRepositories map[string]bool) (chart.Request, bool, error) {
	accessor, err := helmchart.NewDependencyAccessor(dependency)
	if err != nil {
		return chart.Request{}, false, err
	}
	name := accessor.Name()
	version, repository, ok := helmDependencyVersionAndRepository(dependency)
	if !ok {
		return chart.Request{}, false, nil
	}
	repository = strings.TrimSpace(repository)
	switch {
	case repository == "":
		return chart.Request{}, false, nil
	case strings.HasPrefix(repository, "file://"):
		return chart.Request{}, false, nil
	case strings.HasPrefix(repository, "@"), strings.HasPrefix(repository, "alias:"):
		return chart.Request{}, false, nil
	}
	kind := ChartRepositoryKind(repository, ociRepositories)
	if _, err := chart.NormalizeRepository(repository, kind); err != nil {
		return chart.Request{}, false, fmt.Errorf("helm chart dependency %q repository %q: %w", name, repository, err)
	}
	return chart.Request{
		Repository: repository,
		Name:       name,
		Version:    strings.TrimSpace(version),
		Kind:       kind,
	}, true, nil
}

func helmDependencyChartPath(parentChartPath, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\*?[`) {
		return "", fmt.Errorf("helm chart dependency name %q is not a safe chart path segment", name)
	}
	chartsPath := filepath.Join(parentChartPath, "charts")
	dst := filepath.Join(chartsPath, name)
	rel, err := filepath.Rel(chartsPath, dst)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("helm chart dependency path %q escapes charts directory", name)
	}
	return dst, nil
}

func replaceHelmDependencyChartTargets(parentChartPath, name string) (string, error) {
	dst, err := helmDependencyChartPath(parentChartPath, name)
	if err != nil {
		return "", err
	}
	if err := os.RemoveAll(dst); err != nil {
		return "", fmt.Errorf("replace acquired helm chart dependency %s: %w", name, err)
	}
	chartsPath := filepath.Dir(dst)
	for _, archive := range []string{
		filepath.Join(chartsPath, name+".tgz"),
		filepath.Join(chartsPath, name+"-*.tgz"),
	} {
		matches, err := filepath.Glob(archive)
		if err != nil {
			return "", fmt.Errorf("find stale helm chart dependency archives for %s: %w", name, err)
		}
		for _, match := range matches {
			if err := removeHelmDependencyArchive(chartsPath, match); err != nil {
				return "", err
			}
		}
	}
	return dst, nil
}

func removeHelmDependencyArchive(chartsPath, archivePath string) error {
	rel, err := filepath.Rel(chartsPath, archivePath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.Dir(rel) != "." {
		return fmt.Errorf("helm chart dependency archive %q escapes charts directory", archivePath)
	}
	if filepath.Ext(archivePath) != ".tgz" {
		return fmt.Errorf("helm chart dependency archive %q is not a .tgz file", archivePath)
	}
	if err := os.Remove(archivePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale helm chart dependency archive %s: %w", filepath.Base(archivePath), err)
	}
	return nil
}

func helmDependencyVersionAndRepository(dependency helmchart.Dependency) (string, string, bool) {
	switch dep := dependency.(type) {
	case chartv2.Dependency:
		return dep.Version, dep.Repository, true
	case *chartv2.Dependency:
		if dep == nil {
			return "", "", false
		}
		return dep.Version, dep.Repository, true
	default:
		return "", "", false
	}
}

func helmLockedDependency(chrt helmchart.Charter, dependency helmchart.Dependency) helmchart.Dependency {
	chart, ok := chrt.(*chartv2.Chart)
	if !ok || chart.Lock == nil {
		return dependency
	}
	name, repository, ok := helmDependencyNameAndRepository(dependency)
	if !ok {
		return dependency
	}
	for _, locked := range chart.Lock.Dependencies {
		if locked == nil || locked.Name != name {
			continue
		}
		if repository != "" && locked.Repository != "" && locked.Repository != repository {
			continue
		}
		return locked
	}
	return dependency
}

func helmDependencyNameAndRepository(dependency helmchart.Dependency) (string, string, bool) {
	accessor, err := helmchart.NewDependencyAccessor(dependency)
	if err != nil {
		return "", "", false
	}
	_, repository, ok := helmDependencyVersionAndRepository(dependency)
	if !ok {
		return "", "", false
	}
	return accessor.Name(), repository, true
}

func processHelmDependencies(chrt helmchart.Charter, values map[string]any, manifestPath string) error {
	if err := checkHelmDependencies(chrt); err != nil {
		return fmt.Errorf("helm chart dependencies %s require vendored charts: %w", manifestPath, err)
	}
	chart, ok := chrt.(*chartv2.Chart)
	if !ok {
		return nil
	}
	if err := chartv2util.ProcessDependencies(chart, common.Values(values)); err != nil {
		return fmt.Errorf("helm chart dependencies %s: %w", manifestPath, err)
	}
	return nil
}

func checkHelmDependencies(chrt helmchart.Charter) error {
	missingDependencies, err := missingHelmDependencies(chrt)
	if err != nil {
		return err
	}
	if len(missingDependencies) == 0 {
		return nil
	}
	missing := make([]string, 0, len(missingDependencies))
	for _, dependency := range missingDependencies {
		dependencyAccessor, err := helmchart.NewDependencyAccessor(dependency)
		if err != nil {
			return err
		}
		missing = append(missing, dependencyAccessor.Name())
	}
	return fmt.Errorf("found in Chart.yaml, but missing in charts/ directory: %s", strings.Join(missing, ", "))
}

func missingHelmDependencies(chrt helmchart.Charter) ([]helmchart.Dependency, error) {
	accessor, err := helmchart.NewAccessor(chrt)
	if err != nil {
		return nil, err
	}

	var missing []helmchart.Dependency
dependencies:
	for _, required := range accessor.MetaDependencies() {
		required = helmLockedDependency(chrt, required)
		for _, dependency := range accessor.Dependencies() {
			ok, err := helmDependencySatisfied(required, dependency)
			if err != nil {
				return nil, err
			}
			if ok {
				continue dependencies
			}
		}
		missing = append(missing, required)
	}
	return missing, nil
}

func helmDependencySatisfied(required helmchart.Dependency, dependency helmchart.Charter) (bool, error) {
	requiredAccessor, err := helmchart.NewDependencyAccessor(required)
	if err != nil {
		return false, err
	}
	dependencyAccessor, err := helmchart.NewAccessor(dependency)
	if err != nil {
		return false, err
	}
	if dependencyAccessor.Name() != requiredAccessor.Name() {
		return false, nil
	}

	requiredVersion, _, ok := helmDependencyVersionAndRepository(required)
	if !ok || strings.TrimSpace(requiredVersion) == "" {
		return true, nil
	}
	dependencyVersion := helmChartVersion(dependency)
	if dependencyVersion == "" {
		return false, nil
	}
	return chartv2util.IsCompatibleRange(requiredVersion, dependencyVersion), nil
}

func helmChartVersion(chrt helmchart.Charter) string {
	chart, ok := chrt.(*chartv2.Chart)
	if !ok || chart.Metadata == nil {
		return ""
	}
	return chart.Metadata.Version
}

func recordChartCacheEvent(opts RenderOptions, request chart.Request, acquireErr error, acquired chart.Result) {
	if acquireErr == nil && opts.AcquisitionCollector != nil {
		opts.AcquisitionCollector.Record(cacheevent.AcquisitionRecord{
			Kind:              cacheevent.AcquisitionChart,
			RequestedRevision: request.Version,
			ResolvedRevision:  acquired.Version,
		})
	}
	if opts.CacheEventRecorder == nil {
		return
	}
	input := cacheevent.AcquisitionEventInput{
		Source:            cacheevent.SourceChart,
		Target:            request.Repository,
		RequestedRevision: request.Version,
		Offline:           opts.OfflineCharts,
		Refresh:           opts.RefreshCharts,
		SensitiveValues:   chartSensitiveValues(opts.ChartCredentials),
	}
	if acquireErr != nil {
		input.Err = acquireErr
		opts.CacheEventRecorder.Record(cacheevent.NewAcquisitionError(input).Event)
		return
	}
	input.Revision = acquired.Version
	input.FromCache = acquired.FromCache
	input.Network = !acquired.FromCache
	opts.CacheEventRecorder.Record(cacheevent.NewAcquisitionEvent(input))
}
