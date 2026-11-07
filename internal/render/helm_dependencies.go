package render

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/chart"
	helmchart "helm.sh/helm/v4/pkg/chart"
	"helm.sh/helm/v4/pkg/chart/common"
	"helm.sh/helm/v4/pkg/chart/loader"
	chartv2 "helm.sh/helm/v4/pkg/chart/v2"
	chartv2loader "helm.sh/helm/v4/pkg/chart/v2/loader"
	chartv2util "helm.sh/helm/v4/pkg/chart/v2/util"
	"helm.sh/helm/v4/pkg/provenance"
)

// prepareHelmDependencyWorkspace runs drydock's `helm dependency build` when
// helm template would ask for one. helm template fails only when a
// Chart.yaml dependency has no chart of the same name under charts/
// (action.CheckDependencies, helm v4.3.1 pkg/action/install.go:839-869), and
// the repo-server then runs `helm dependency build` once
// (reposerver/repository/repository.go:1402-1416 and 1251-1271 at v3.6.1).
// A vendored chart whose version does not match Chart.yaml or Chart.lock
// still counts, so nothing is fetched then. The build fetches every remote
// dependency (helmDependencyBuildRequests), also the ones that are vendored,
// into a temporary copy of the chart, and removes the outdated archives
// (removeOutdatedHelmDependencyArchives).
func prepareHelmDependencyWorkspace(ctx context.Context, chartPath, chartSourcePath string, chrt helmchart.Charter, opts RenderOptions) (helmchart.Charter, func(), error) {
	missing, err := missingHelmDependencies(chrt)
	if err != nil || len(missing) == 0 {
		return chrt, nil, err
	}
	requests, keep, err := helmDependencyBuildRequests(chrt, chartPath, missing, opts.OCIChartRepositories)
	if err != nil {
		return nil, nil, fmt.Errorf("helm chart dependencies %s: error building helm chart dependencies: %w", chartSourcePath, err)
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

	if err := acquireHelmDependencies(ctx, tempChartPath, requests, opts); err != nil {
		cleanup()
		return nil, nil, err
	}
	if err := removeOutdatedHelmDependencyArchives(filepath.Join(tempChartPath, "charts"), keep); err != nil {
		cleanup()
		return nil, nil, err
	}

	prepared, err := loader.Load(tempChartPath)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("load helm chart with acquired dependencies %s: %w", chartSourcePath, err)
	}
	return prepared, cleanup, nil
}

// acquireHelmDependencies fetches each request and puts the chart under the
// charts/ directory of the chart copy at tempChartPath.
func acquireHelmDependencies(ctx context.Context, tempChartPath string, requests []chart.Request, opts RenderOptions) error {
	acquirer := opts.ChartAcquirer
	if acquirer == nil {
		acquirer = chart.DefaultAcquirer{}
	}
	for _, request := range requests {
		if err := ctx.Err(); err != nil {
			return err
		}
		result, err := acquirer.Acquire(ctx, request, chart.Options{
			CacheDir:        opts.ChartCacheDir,
			Offline:         opts.OfflineCharts,
			Refresh:         opts.RefreshCharts,
			MaxAge:          opts.ChartCacheMaxAge,
			ForbiddenRoots:  append([]string(nil), opts.ChartForbiddenRoots...),
			PassCredentials: opts.PassCredentials,
			Credentials:     opts.ChartCredentials,
		})
		if err != nil {
			recordChartCacheEvent(opts, request, err, chart.Result{})
			return fmt.Errorf("acquire helm chart dependency %s: %s", request.Name, redactKustomizeChartAcquireError(err, request.Repository, opts.ChartCredentials))
		}
		recordChartCacheEvent(opts, request, nil, result)

		dst, err := replaceHelmDependencyChartTargets(tempChartPath, request.Name)
		if err != nil {
			return err
		}
		if err := copyRegularTree(result.ChartDir, dst); err != nil {
			return fmt.Errorf("copy acquired helm chart dependency %s: %w", request.Name, err)
		}
	}
	return nil
}

// helmDependencyBuildRequests returns the chart requests of `helm dependency
// build` for chrt, whose dependencies missing have no chart under charts/,
// and the names of the dependencies whose vendored archives the build keeps.
//
// With a Chart.lock, the build runs Manager.Build (helm v4.3.1
// pkg/downloader/manager.go:89-155): the lock must match Chart.yaml
// (checkHelmChartLock), every repository in it must be known
// (checkHelmLockRepositories), and every locked dependency is fetched at
// its locked version. Without a Chart.lock, it runs Manager.Update, which
// resolves each Chart.yaml version against its repository
// (chart.VersionHelmDependency) and fetches that version. A dependency
// without a repository must be under charts/, and drydock fetches no
// file:// or repository-alias dependency: those must be vendored, and stay.
// When a missing dependency is one that drydock does not fetch, it fetches
// nothing, and the render fails on the missing dependency. chartPath is the
// chart directory, whose charts/ directory holds the dependencies without a
// repository (checkHelmLocalDependency).
func helmDependencyBuildRequests(chrt helmchart.Charter, chartPath string, missing []helmchart.Dependency, ociRepositories map[string]bool) ([]chart.Request, map[string]bool, error) {
	parent, ok := chrt.(*chartv2.Chart)
	if !ok || parent.Metadata == nil {
		return nil, nil, nil
	}
	dependencies := parent.Metadata.Dependencies
	policy := chart.VersionHelmDependency
	if parent.Lock != nil {
		if err := checkHelmChartLock(parent); err != nil {
			return nil, nil, err
		}
		if err := checkHelmLockRepositories(parent.Lock.Dependencies); err != nil {
			return nil, nil, err
		}
		dependencies = parent.Lock.Dependencies
		policy = chart.VersionHelmLocked
	}
	keep := map[string]bool{}
	fetched := map[string]bool{}
	seen := map[chart.Request]bool{}
	var requests []chart.Request
	for _, dependency := range dependencies {
		if dependency == nil {
			continue
		}
		if strings.TrimSpace(dependency.Repository) == "" {
			if err := checkHelmLocalDependency(chartPath, dependency, parent.Lock != nil); err != nil {
				return nil, nil, err
			}
		}
		request, ok, err := helmDependencyChartRequest(dependency, ociRepositories)
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			keep[dependency.Name] = true
			continue
		}
		request.VersionPolicy = policy
		fetched[dependency.Name] = true
		// helm downloads one chart URL once (downloadAll), so two aliases
		// of one chart fetch it once.
		if !seen[request] {
			seen[request] = true
			requests = append(requests, request)
		}
	}
	if ok, err := allHelmDependenciesFetched(missing, fetched); err != nil || !ok {
		return nil, nil, err
	}
	return requests, keep, nil
}

// allHelmDependenciesFetched reports whether the build fetches every
// missing dependency.
func allHelmDependenciesFetched(missing []helmchart.Dependency, fetched map[string]bool) (bool, error) {
	for _, dependency := range missing {
		accessor, err := helmchart.NewDependencyAccessor(dependency)
		if err != nil {
			return false, err
		}
		if !fetched[accessor.Name()] {
			return false, nil
		}
	}
	return true, nil
}

// checkHelmLocalDependency checks a dependency without a repository as
// `helm dependency build` does. Without a Chart.lock, resolver.Resolve
// requires the path charts/<name> (helm v4.3.1
// internal/resolver/resolver.go:66-77). Then Manager.downloadAll loads
// charts/<name> as a directory and requires its version to match the
// dependency version (pkg/downloader/manager.go:279-300), also with a
// Chart.lock. The errors have helm's text, with the path relative to the
// chart, as the repo-server runs the build in the chart directory.
func checkHelmLocalDependency(chartPath string, dependency *chartv2.Dependency, locked bool) error {
	rel := filepath.ToSlash(filepath.Join("charts", dependency.Name))
	path := filepath.Join(chartPath, "charts", dependency.Name)
	if !locked {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("directory %s not found", rel)
		} else if err != nil {
			return err
		}
	}
	local, err := chartv2loader.LoadDir(path)
	if err != nil {
		return fmt.Errorf("unable to load chart '%s': %w", rel, err)
	}
	constraint, err := semver.NewConstraint(dependency.Version)
	if err != nil {
		return fmt.Errorf("dependency %s has an invalid version/constraint format: %w", dependency.Name, err)
	}
	v, err := semver.NewVersion(local.Metadata.Version)
	if err != nil {
		return fmt.Errorf("invalid version %s for dependency %s: %w", dependency.Version, dependency.Name, err)
	}
	if !constraint.Check(v) {
		return fmt.Errorf("dependency %s at version %s does not satisfy the constraint %s", dependency.Name, local.Metadata.Version, dependency.Version)
	}
	return nil
}

// checkHelmChartLock fails a Chart.lock whose digest does not match the
// dependencies of Chart.yaml, as Manager.Build does (helm v4.3.1
// pkg/downloader/manager.go:105-131). Build replaces a repository alias in
// Chart.yaml with the URL of the repository that Argo CD added before it
// hashes, and drydock does not know those URLs, so it skips the check when
// a dependency names a repository alias.
func checkHelmChartLock(parent *chartv2.Chart) error {
	req := parent.Metadata.Dependencies
	for _, dependency := range req {
		if dependency != nil && (strings.HasPrefix(dependency.Repository, "@") || strings.HasPrefix(dependency.Repository, "alias:")) {
			return nil
		}
	}
	sum, err := helmHashReq(req, parent.Lock.Dependencies)
	if err == nil && sum == parent.Lock.Digest {
		return nil
	}
	if parent.Metadata.APIVersion != chartv2.APIVersionV1 {
		return errors.New("the lock file (Chart.lock) is out of sync with the dependencies file (Chart.yaml). Please update the dependencies with 'helm dependency update'")
	}
	if v2Sum, err := helmHashV2Req(req); err == nil && v2Sum == parent.Lock.Digest {
		return nil
	}
	return errors.New("the lock file (requirements.lock) is out of sync with the dependencies file (requirements.yaml). Please update the dependencies")
}

// helmHashReq is resolver.HashReq (helm v4.3.1
// internal/resolver/resolver.go:213-220), which helm keeps internal.
func helmHashReq(req, lock []*chartv2.Dependency) (string, error) {
	data, err := json.Marshal([2][]*chartv2.Dependency{req, lock})
	if err != nil {
		return "", err
	}
	sum, err := provenance.Digest(bytes.NewBuffer(data))
	return "sha256:" + sum, err
}

// helmHashV2Req is resolver.HashV2Req (helm v4.3.1
// internal/resolver/resolver.go:227-236), the Helm 2 lock digest.
func helmHashV2Req(req []*chartv2.Dependency) (string, error) {
	data, err := json.Marshal(map[string][]*chartv2.Dependency{"dependencies": req})
	if err != nil {
		return "", err
	}
	sum, err := provenance.Digest(bytes.NewBuffer(data))
	return "sha256:" + sum, err
}

// checkHelmLockRepositories fails a locked dependency whose repository helm
// does not know, as Manager.hasAllRepos does (helm v4.3.1
// pkg/downloader/manager.go:463-490). The repo-server adds only the https
// and oci repositories of Chart.yaml before the build (getHelmDependencyRepos,
// reposerver/repository/repository.go:1208-1240 at v3.6.1), and the build
// itself skips oci and file:// repositories. So an http:// repository fails,
// with the text of `helm dependency build` (pkg/cmd/dependency_build.go).
// drydock does not know the repositories that a repository alias names, so
// it lets such an entry pass.
func checkHelmLockRepositories(dependencies []*chartv2.Dependency) error {
	var missing []string
	for _, dependency := range dependencies {
		if dependency == nil {
			continue
		}
		repository := dependency.Repository
		switch {
		case repository == "",
			strings.HasPrefix(repository, "file://"),
			strings.HasPrefix(repository, "oci://"),
			strings.HasPrefix(repository, "@"),
			strings.HasPrefix(repository, "alias:"):
			continue
		}
		if parsed, err := url.Parse(repository); err == nil && parsed.Scheme == "https" {
			continue
		}
		missing = append(missing, repository)
	}
	if len(missing) > 0 {
		return fmt.Errorf("no repository definition for %s. Please add the missing repos via 'helm repo add'", strings.Join(missing, ", "))
	}
	return nil
}

// removeOutdatedHelmDependencyArchives removes the chart archives under
// chartsPath that the build did not fetch, as Manager.safeMoveDeps does
// (helm v4.3.1 pkg/downloader/manager.go:404-460): every file that loads as
// a chart, unless the chart is a dependency that the build keeps (keep).
// drydock puts a fetched dependency in a directory, which already replaced
// the archives of that name (replaceHelmDependencyChartTargets).
// Directories stay, as in helm.
func removeOutdatedHelmDependencyArchives(chartsPath string, keep map[string]bool) error {
	entries, err := os.ReadDir(chartsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read helm chart dependencies %s: %w", chartsPath, err)
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		archivePath := filepath.Join(chartsPath, entry.Name())
		archived, err := chartv2loader.LoadFile(archivePath)
		if err != nil || keep[archived.Name()] {
			continue
		}
		if err := os.Remove(archivePath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove outdated helm chart dependency archive %s: %w", entry.Name(), err)
		}
	}
	return nil
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

// replaceHelmDependencyChartTargets removes the vendored copies of the
// dependency name that the build fetches: the directory charts/<name> and
// each archive under charts/ whose chart is named name. helm decides by
// the chart name inside the archive too (Manager.safeMoveDeps), so an
// archive such as redis-cluster-1.0.0.tgz stays when redis is fetched.
func replaceHelmDependencyChartTargets(parentChartPath, name string) (string, error) {
	dst, err := helmDependencyChartPath(parentChartPath, name)
	if err != nil {
		return "", err
	}
	if err := os.RemoveAll(dst); err != nil {
		return "", fmt.Errorf("replace acquired helm chart dependency %s: %w", name, err)
	}
	chartsPath := filepath.Dir(dst)
	entries, err := os.ReadDir(chartsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return dst, nil
		}
		return "", fmt.Errorf("find stale helm chart dependency archives for %s: %w", name, err)
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || filepath.Ext(entry.Name()) != ".tgz" {
			continue
		}
		archivePath := filepath.Join(chartsPath, entry.Name())
		archived, err := chartv2loader.LoadFile(archivePath)
		if err != nil || archived.Name() != name {
			continue
		}
		if err := removeHelmDependencyArchive(chartsPath, archivePath); err != nil {
			return "", err
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

// missingHelmDependencies returns the Chart.yaml dependencies that have no
// chart of the same name under charts/, as action.CheckDependencies finds
// them (helm v4.3.1 pkg/action/install.go:839-869). It matches names only:
// a vendored chart of another version still counts.
func missingHelmDependencies(chrt helmchart.Charter) ([]helmchart.Dependency, error) {
	accessor, err := helmchart.NewAccessor(chrt)
	if err != nil {
		return nil, err
	}

	var missing []helmchart.Dependency
dependencies:
	for _, required := range accessor.MetaDependencies() {
		requiredAccessor, err := helmchart.NewDependencyAccessor(required)
		if err != nil {
			return nil, err
		}
		for _, dependency := range accessor.Dependencies() {
			dependencyAccessor, err := helmchart.NewAccessor(dependency)
			if err != nil {
				return nil, err
			}
			if dependencyAccessor.Name() == requiredAccessor.Name() {
				continue dependencies
			}
		}
		missing = append(missing, required)
	}
	return missing, nil
}

func recordChartCacheEvent(opts RenderOptions, request chart.Request, acquireErr error, acquired chart.Result) {
	if acquireErr == nil && opts.AcquisitionCollector != nil {
		opts.AcquisitionCollector.Record(ChartAcquisitionRecord(request, acquired, opts.ChartCacheMaxAge))
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

// ChartAcquisitionRecord is the acquisition record of one successful chart
// acquisition: its pinned revisions and the chart cache entry it was served
// from, which the persistent render cache checks under a chart cache max age.
func ChartAcquisitionRecord(request chart.Request, acquired chart.Result, maxAge time.Duration) cacheevent.AcquisitionRecord {
	kind := acquired.Kind
	if kind == "" {
		kind = request.Kind
	}
	return cacheevent.AcquisitionRecord{
		Kind:              cacheevent.AcquisitionChart,
		RequestedRevision: request.Version,
		ResolvedRevision:  acquired.Version,
		Chart: cacheevent.ChartAcquisition{
			Target:    cacheevent.RedactTarget(request.Repository),
			Name:      strings.TrimSpace(request.Name),
			Version:   strings.TrimSpace(request.Version),
			CacheKind: string(kind),
			CacheKey:  acquired.CacheKey,
			FetchedAt: acquired.FetchedAt,
			MaxAge:    maxAge,
			Stale:     acquired.Stale,
		},
	}
}
