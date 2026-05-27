package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/acquisition"
	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/chart"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/discovery"
	"github.com/sholdee/drydock/internal/render"
	sourcepkg "github.com/sholdee/drydock/internal/source"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func (o Orchestrator) discoverRepository(ctx context.Context, root string, request BuildRequest) (discovery.Result, []diagnostic.Diagnostic, []cacheevent.Event, error) {
	discovered, err := discovery.Scan(root, discovery.Options{})
	if err != nil {
		return discovery.Result{}, nil, nil, err
	}
	if len(request.DiscoverKustomizePaths) == 0 {
		return discovered, nil, nil, nil
	}

	settings, _, err := loadSettingsFromDiscovery(root, discovered)
	if err != nil {
		return discovered, nil, nil, err
	}
	rendered, diags, events, err := o.discoverRenderedKustomize(ctx, root, settings, request)
	if err != nil {
		return discovered, diags, events, err
	}
	return mergeDiscoveryResults(discovered, rendered), diags, events, nil
}

func (o Orchestrator) discoverRenderedKustomize(ctx context.Context, root string, settings config.ArgoSettings, request BuildRequest) (discovery.Result, []diagnostic.Diagnostic, []cacheevent.Event, error) {
	recorder := cacheevent.NewRecorder(request.RecordCacheEvents)
	provider, cleanup, err := o.discoveryProvider(root, settings, request, recorder)
	if err != nil {
		return discovery.Result{}, nil, recorder.Events(), err
	}
	defer cleanup()

	var out discovery.Result
	var allDiags []diagnostic.Diagnostic
	seenPaths := map[string]struct{}{}
	for _, rawPath := range request.DiscoverKustomizePaths {
		clean, err := cleanDiscoverKustomizePath(root, rawPath)
		if err != nil {
			return out, allDiags, recorder.Events(), err
		}
		displayPath := filepath.ToSlash(clean)
		if _, ok := seenPaths[displayPath]; ok {
			continue
		}
		seenPaths[displayPath] = struct{}{}

		manifests, diags, err := provider.RenderSource(ctx, render.ResolvedSource{
			RepoRoot: root,
			Path:     clean,
		}, render.RenderOptions{})
		allDiags = append(allDiags, diags...)
		if err != nil {
			return out, allDiags, recorder.Events(), fmt.Errorf("discover kustomize %q: %w", displayPath, err)
		}
		next, err := discovery.ScanObjects(displayPath, manifestObjects(manifests))
		if err != nil {
			return out, allDiags, recorder.Events(), fmt.Errorf("discover kustomize %q: %w", displayPath, err)
		}
		out = mergeDiscoveryResults(out, next)
	}
	return out, allDiags, recorder.Events(), nil
}

func (o Orchestrator) discoveryProvider(root string, settings config.ArgoSettings, request BuildRequest, recorder *cacheevent.Recorder) (localProvider, func(), error) {
	acquirer := o.ChartAcquirer
	if acquirer == nil {
		acquirer = chart.DefaultAcquirer{}
	}
	gitAcquirer := o.GitAcquirer
	if gitAcquirer == nil {
		gitAcquirer = sourcepkg.DefaultGitAcquirer{}
	}
	forbiddenRoots := append([]string(nil), request.RemoteResourceForbiddenRoots...)
	forbiddenRoots = append(forbiddenRoots, root)
	provider := localProvider{
		repoRoot:                     root,
		sourceResolver:               sourcepkg.NewResolver(sourcepkg.Options{RepoMaps: request.RepoMaps, Offline: request.Offline}),
		chartAcquirer:                acquirer,
		gitAcquirer:                  gitAcquirer,
		remoteResourceAcquirer:       o.RemoteResourceAcquirer,
		pluginRenderer:               o.pluginRenderer(request),
		offline:                      request.Offline,
		refreshCharts:                request.RefreshCharts,
		chartCacheDir:                request.ChartCacheDir,
		chartCredentials:             request.ChartCredentials,
		ociChartRepositories:         ociChartRepositoriesFromSettings(settings),
		gitCacheDir:                  request.GitCacheDir,
		refreshGit:                   request.RefreshGit,
		gitCredentials:               request.GitCredentials,
		refreshRemoteResources:       request.RefreshRemoteResources,
		remoteResourceCacheDir:       request.RemoteResourceCacheDir,
		remoteResourceForbiddenRoots: forbiddenRoots,
		remoteResourceCredentials:    request.RemoteResourceCredentials,
		remoteResourceGitCredentials: request.RemoteResourceGitCredentials,
		pluginTimeout:                request.PluginTimeout,
		kustomizeBuildOptions:        settingsBuildOptions(settings),
		configManagementPlugins:      settings.ConfigManagementPlugins,
		cacheEvents:                  recorder,
	}
	snapshotRoot, err := os.MkdirTemp("", "drydock-discovery-cache-snapshots-*")
	if err != nil {
		return provider, func() {}, err
	}
	provider.acquisition = acquisition.Session{
		Locks:              processCacheTargetLocks,
		SnapshotRoot:       snapshotRoot,
		SnapshotCacheReads: shouldSnapshotCacheReads(request),
		SnapshotCache:      acquisition.NewSnapshotCache(),
	}
	return provider, func() { _ = os.RemoveAll(snapshotRoot) }, nil
}

func cleanDiscoverKustomizePath(root, rawPath string) (string, error) {
	clean, err := cleanLocalSourcePath(rawPath)
	if err != nil {
		return "", fmt.Errorf("discover-kustomize path %q: %w", rawPath, err)
	}
	if err := rejectLocalSymlinkComponents(root, clean); err != nil {
		return "", fmt.Errorf("discover-kustomize path %q: %w", rawPath, err)
	}
	if !hasKustomizationFile(filepath.Join(root, clean)) {
		return "", fmt.Errorf("discover-kustomize path %q does not contain a kustomization file", rawPath)
	}
	return clean, nil
}

func hasKustomizationFile(root string) bool {
	for _, name := range []string{"kustomization.yaml", "kustomization.yml", "Kustomization"} {
		exists, err := localPathExists(filepath.Join(root, name))
		if err == nil && exists {
			return true
		}
	}
	return false
}

func manifestObjects(manifests []render.Manifest) []*unstructured.Unstructured {
	objects := make([]*unstructured.Unstructured, 0, len(manifests))
	for _, manifest := range manifests {
		if manifest.Object == nil {
			continue
		}
		objects = append(objects, manifest.Object.DeepCopy())
	}
	return objects
}

func mergeDiscoveryResults(base, overlay discovery.Result) discovery.Result {
	out := base
	out.Applications = mergeApplications(out.Applications, overlay.Applications)
	out.ApplicationSets = mergeApplicationSets(out.ApplicationSets, overlay.ApplicationSets)
	out.Projects = mergeProjects(out.Projects, overlay.Projects)
	out.SettingsCandidates = mergeSettingsCandidates(out.SettingsCandidates, overlay.SettingsCandidates)
	return out
}

func mergeApplications(base, overlay []discovery.ApplicationFile) []discovery.ApplicationFile {
	out := append([]discovery.ApplicationFile(nil), base...)
	indexes := make(map[string]int, len(out))
	for i, item := range out {
		indexes[applicationDiscoveryKey(item.Application)] = i
	}
	for _, item := range overlay {
		key := applicationDiscoveryKey(item.Application)
		if index, ok := indexes[key]; ok {
			out[index] = item
			continue
		}
		indexes[key] = len(out)
		out = append(out, item)
	}
	return out
}

func mergeApplicationSets(base, overlay []discovery.ApplicationSetFile) []discovery.ApplicationSetFile {
	out := append([]discovery.ApplicationSetFile(nil), base...)
	indexes := make(map[string]int, len(out))
	for i, item := range out {
		indexes[applicationSetDiscoveryKey(item.ApplicationSet)] = i
	}
	for _, item := range overlay {
		key := applicationSetDiscoveryKey(item.ApplicationSet)
		if index, ok := indexes[key]; ok {
			out[index] = item
			continue
		}
		indexes[key] = len(out)
		out = append(out, item)
	}
	return out
}

func mergeProjects(base, overlay []discovery.ProjectFile) []discovery.ProjectFile {
	out := append([]discovery.ProjectFile(nil), base...)
	indexes := make(map[string]int, len(out))
	for i, item := range out {
		indexes[projectDiscoveryKey(item.Project)] = i
	}
	for _, item := range overlay {
		key := projectDiscoveryKey(item.Project)
		if index, ok := indexes[key]; ok {
			out[index] = item
			continue
		}
		indexes[key] = len(out)
		out = append(out, item)
	}
	return out
}

func mergeSettingsCandidates(base, overlay []discovery.SettingsCandidate) []discovery.SettingsCandidate {
	out := append([]discovery.SettingsCandidate(nil), base...)
	indexes := make(map[string]int, len(out))
	for i, item := range out {
		indexes[settingsDiscoveryKey(item)] = i
	}
	for _, item := range overlay {
		key := settingsDiscoveryKey(item)
		if index, ok := indexes[key]; ok {
			out[index] = item
			continue
		}
		indexes[key] = len(out)
		out = append(out, item)
	}
	return out
}

func applicationDiscoveryKey(app argoappv1.Application) string {
	return objectDiscoveryKey("argoproj.io/v1alpha1", "Application", app.Namespace, app.Name)
}

func applicationSetDiscoveryKey(appSet argoappv1.ApplicationSet) string {
	return objectDiscoveryKey("argoproj.io/v1alpha1", "ApplicationSet", appSet.Namespace, appSet.Name)
}

func projectDiscoveryKey(project argoappv1.AppProject) string {
	return objectDiscoveryKey("argoproj.io/v1alpha1", "AppProject", project.Namespace, project.Name)
}

func settingsDiscoveryKey(candidate discovery.SettingsCandidate) string {
	if candidate.Object != nil && candidate.Object.GetKind() != "" && candidate.Object.GetName() != "" {
		return objectDiscoveryKey(candidate.Object.GetAPIVersion(), candidate.Object.GetKind(), candidate.Object.GetNamespace(), candidate.Object.GetName())
	}
	if candidate.APIVersion != "" && candidate.Name != "" {
		return objectDiscoveryKey(candidate.APIVersion, settingsObjectKind(candidate.Kind), candidate.Namespace, candidate.Name)
	}
	return fmt.Sprintf("settings\x00%s\x00%s\x00%d", candidate.Kind, filepath.ToSlash(candidate.Path), candidate.DocumentIndex)
}

func settingsObjectKind(kind string) string {
	switch kind {
	case "argocd-cm", "argocd-cmp-cm":
		return "ConfigMap"
	case "repository-secret":
		return "Secret"
	default:
		return kind
	}
}

func objectDiscoveryKey(apiVersion, kind, namespace, name string) string {
	return apiVersion + "\x00" + kind + "\x00" + namespace + "\x00" + name
}

func emptyApplicationSetDiagnostic(appSetFile discovery.ApplicationSetFile) diagnostic.Diagnostic {
	name := appSetFile.ApplicationSet.Name
	if appSetFile.ApplicationSet.Namespace != "" {
		name = appSetFile.ApplicationSet.Namespace + "/" + name
	}
	return diagnostic.Diagnostic{
		Severity:   diagnostic.SeverityWarning,
		Category:   "appset",
		Message:    fmt.Sprintf("ApplicationSet %s generated zero Applications", name),
		Provenance: diagnostic.Provenance{Path: appSetFile.Path, Pointer: "spec.generators"},
	}
}
