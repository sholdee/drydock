package requestopts

import (
	"maps"
	"time"

	"github.com/sholdee/drydock/internal/app"
	"github.com/sholdee/drydock/internal/appset"
	"github.com/sholdee/drydock/internal/chart"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/remote"
	"github.com/sholdee/drydock/internal/rendercache"
	"github.com/sholdee/drydock/internal/source"
)

type Options struct {
	Path                           string
	LeftPath                       string
	RightPath                      string
	Repo                           string
	Ref                            string
	RefOrig                        string
	DiscoveryMode                  string
	MaxDiscoveryDepth              int
	MaxDiscoveryDepthSet           bool
	DiscoverKustomizePaths         []string
	DiscoverIgnores                []string
	ChangedOnly                    *bool
	ChangedOnlyIncludes            []string
	ChangedOnlyIgnores             []string
	StrictChangedOnly              bool
	Strict                         bool
	ProjectDiagnosticsMode         diagnostic.ProjectDiagnosticsMode
	ValidateLuaHealth              bool
	Unified                        int
	StripAttrs                     []string
	ShowIgnoredFields              bool
	Offline                        bool
	RefreshCharts                  bool
	ChartCacheDir                  string
	ChartCredentials               chart.ChartCredentials
	RepoMaps                       []source.RepoMap
	GitCacheDir                    string
	RefreshGit                     bool
	GitCredentials                 source.GitCredentials
	RefreshRemoteResources         bool
	RemoteResourceCacheDir         string
	RemoteResourceForbiddenRoots   []string
	RemoteResourceCredentials      remote.Credentials
	RemoteResourceGitCredentials   remote.GitCredentials
	EnableAVPCompat                bool
	EnableKSOPSCompat              bool
	EnablePlugins                  bool
	PluginCacheDir                 string
	PluginPolicyPath               string
	PluginPolicyPathExplicit       bool
	PluginPolicyRef                string
	PluginPolicyRepo               string
	DisablePluginPolicy            bool
	PluginTimeout                  time.Duration
	Parallelism                    int
	SkipKinds                      []string
	SkipCRDs                       bool
	SkipSecrets                    bool
	ApplicationSetProviderFixtures []string
	ApplicationSetProviderData     appset.ProviderData
	RecordCacheEvents              bool
	RenderCacheEnabled             bool
	RenderCacheDir                 string
	RenderCacheMaxBytes            int64
	RefreshRenders                 bool
	EngineFingerprint              rendercache.EngineFingerprint
	KubeVersion                    string
	APIVersions                    []string
	NoCRDScope                     bool
}

func (options Options) Build() app.BuildRequest {
	return app.BuildRequest{
		Path:                   options.Path,
		Strict:                 options.Strict,
		ProjectDiagnosticsMode: options.ProjectDiagnosticsMode,
		DiscoveryOptions:       options.discoveryOptions(),
		ValidateLuaHealth:      options.ValidateLuaHealth,
		AcquisitionOptions:     options.acquisitionOptions(),
		RenderCacheOptions:     options.renderCacheOptions(),
		PluginOptions:          options.pluginOptions(),
		ExecutionOptions:       options.executionOptions(),
		FilterOptions:          options.filterOptions(),
		ApplicationSetOptions:  options.applicationSetOptions(),
		CapabilityOptions:      options.capabilityOptions(),
		CRDScopeOptions:        app.CRDScopeOptions{Disabled: options.NoCRDScope},
	}
}

func (options Options) Diff() app.DiffRequest {
	changedOnly := true
	if options.ChangedOnly != nil {
		changedOnly = *options.ChangedOnly
	}
	return app.DiffRequest{
		LeftPath:                options.LeftPath,
		RightPath:               options.RightPath,
		Repo:                    options.Repo,
		Ref:                     options.Ref,
		RefOrig:                 options.RefOrig,
		DiscoveryOptions:        options.discoveryOptions(),
		ChangedOnly:             changedOnly,
		ChangedOnlyIncludeGlobs: append([]string(nil), options.ChangedOnlyIncludes...),
		ChangedOnlyIgnoreGlobs:  append([]string(nil), options.ChangedOnlyIgnores...),
		StrictChangedOnly:       options.StrictChangedOnly,
		Strict:                  options.Strict,
		ProjectDiagnosticsMode:  options.ProjectDiagnosticsMode,
		Unified:                 options.Unified,
		StripAttrs:              append([]string(nil), options.StripAttrs...),
		ShowIgnoredFields:       options.ShowIgnoredFields,
		AcquisitionOptions:      options.acquisitionOptions(),
		RenderCacheOptions:      options.renderCacheOptions(),
		PluginOptions:           options.pluginOptions(),
		ExecutionOptions:        options.executionOptions(),
		FilterOptions:           options.filterOptions(),
		ApplicationSetOptions:   options.applicationSetOptions(),
		CapabilityOptions:       options.capabilityOptions(),
		CRDScopeOptions:         app.CRDScopeOptions{Disabled: options.NoCRDScope},
	}
}

func (options Options) discoveryOptions() app.DiscoveryOptions {
	return app.DiscoveryOptions{
		DiscoveryMode:          options.DiscoveryMode,
		MaxDiscoveryDepth:      options.MaxDiscoveryDepth,
		MaxDiscoveryDepthSet:   options.MaxDiscoveryDepthSet,
		DiscoverKustomizePaths: append([]string(nil), options.DiscoverKustomizePaths...),
		DiscoverIgnoreGlobs:    append([]string(nil), options.DiscoverIgnores...),
	}
}

func (options Options) acquisitionOptions() app.AcquisitionOptions {
	return app.AcquisitionOptions{
		Offline:                      options.Offline,
		RefreshCharts:                options.RefreshCharts,
		ChartCacheDir:                options.ChartCacheDir,
		ChartCredentials:             options.ChartCredentials,
		RepoMaps:                     append([]source.RepoMap(nil), options.RepoMaps...),
		GitCacheDir:                  options.GitCacheDir,
		RefreshGit:                   options.RefreshGit,
		GitCredentials:               options.GitCredentials,
		RefreshRemoteResources:       options.RefreshRemoteResources,
		RemoteResourceCacheDir:       options.RemoteResourceCacheDir,
		RemoteResourceForbiddenRoots: append([]string(nil), options.RemoteResourceForbiddenRoots...),
		RemoteResourceCredentials:    options.RemoteResourceCredentials,
		RemoteResourceGitCredentials: options.RemoteResourceGitCredentials,
		RecordCacheEvents:            options.RecordCacheEvents,
	}
}

func (options Options) renderCacheOptions() app.RenderCacheOptions {
	return app.RenderCacheOptions{
		RenderCacheEnabled:  options.RenderCacheEnabled,
		RenderCacheDir:      options.RenderCacheDir,
		RenderCacheMaxBytes: options.RenderCacheMaxBytes,
		RefreshRenders:      options.RefreshRenders,
		EngineFingerprint:   options.EngineFingerprint,
	}
}

func (options Options) pluginOptions() app.PluginOptions {
	return app.PluginOptions{
		PluginTimeout:            options.PluginTimeout,
		EnableAVPCompat:          options.EnableAVPCompat,
		EnableKSOPSCompat:        options.EnableKSOPSCompat,
		EnablePlugins:            options.EnablePlugins,
		PluginCacheDir:           options.PluginCacheDir,
		PluginPolicyPath:         options.PluginPolicyPath,
		PluginPolicyPathExplicit: options.PluginPolicyPathExplicit,
		PluginPolicyRef:          options.PluginPolicyRef,
		PluginPolicyRepo:         options.PluginPolicyRepo,
		DisablePluginPolicy:      options.DisablePluginPolicy,
	}
}

func (options Options) executionOptions() app.ExecutionOptions {
	return app.ExecutionOptions{Parallelism: options.Parallelism}
}

func (options Options) filterOptions() app.FilterOptions {
	return app.FilterOptions{
		SkipKinds:   append([]string(nil), options.SkipKinds...),
		SkipCRDs:    options.SkipCRDs,
		SkipSecrets: options.SkipSecrets,
	}
}

func (options Options) capabilityOptions() app.CapabilityOptions {
	return app.CapabilityOptions{KubeVersion: options.KubeVersion, APIVersions: append([]string(nil), options.APIVersions...)}
}

func (options Options) applicationSetOptions() app.ApplicationSetOptions {
	return app.ApplicationSetOptions{
		ApplicationSetProviderFixtures: append([]string(nil), options.ApplicationSetProviderFixtures...),
		ApplicationSetProviderData:     cloneProviderData(options.ApplicationSetProviderData),
	}
}

func cloneProviderData(input appset.ProviderData) appset.ProviderData {
	return appset.ProviderData{
		Clusters:         cloneClusterInputs(input.Clusters),
		ClusterDecisions: cloneClusterDecisionInputs(input.ClusterDecisions),
		SCMRepositories:  cloneSCMRepositoryInputs(input.SCMRepositories),
		PullRequests:     clonePullRequestInputs(input.PullRequests),
		Plugins:          clonePluginInputs(input.Plugins),
	}
}

func cloneClusterInputs(input []appset.ClusterInput) []appset.ClusterInput {
	if input == nil {
		return nil
	}
	out := make([]appset.ClusterInput, 0, len(input))
	for _, item := range input {
		item.Labels = cloneStringMap(item.Labels)
		item.Annotations = cloneStringMap(item.Annotations)
		item.Values = cloneStringMap(item.Values)
		out = append(out, item)
	}
	return out
}

func cloneClusterDecisionInputs(input []appset.ClusterDecisionInput) []appset.ClusterDecisionInput {
	if input == nil {
		return nil
	}
	out := make([]appset.ClusterDecisionInput, 0, len(input))
	for _, item := range input {
		item.Labels = cloneStringMap(item.Labels)
		item.Decisions = cloneAnyMaps(item.Decisions)
		item.Values = cloneStringMap(item.Values)
		out = append(out, item)
	}
	return out
}

func cloneSCMRepositoryInputs(input []appset.SCMRepositoryInput) []appset.SCMRepositoryInput {
	if input == nil {
		return nil
	}
	out := make([]appset.SCMRepositoryInput, 0, len(input))
	for _, item := range input {
		item.Tags = cloneStringMap(item.Tags)
		item.Labels = append([]string(nil), item.Labels...)
		item.Paths = append([]string(nil), item.Paths...)
		item.Values = cloneStringMap(item.Values)
		out = append(out, item)
	}
	return out
}

func clonePullRequestInputs(input []appset.PullRequestInput) []appset.PullRequestInput {
	if input == nil {
		return nil
	}
	out := make([]appset.PullRequestInput, 0, len(input))
	for _, item := range input {
		item.Labels = append([]string(nil), item.Labels...)
		item.Values = cloneStringMap(item.Values)
		out = append(out, item)
	}
	return out
}

func clonePluginInputs(input []appset.PluginInput) []appset.PluginInput {
	if input == nil {
		return nil
	}
	out := make([]appset.PluginInput, 0, len(input))
	for _, item := range input {
		item.Outputs = cloneAnySlice(item.Outputs)
		item.Values = cloneStringMap(item.Values)
		out = append(out, item)
	}
	return out
}

func cloneStringMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	out := make(map[string]string, len(input))
	maps.Copy(out, input)
	return out
}

func cloneAnyMaps(input []map[string]any) []map[string]any {
	if input == nil {
		return nil
	}
	out := make([]map[string]any, 0, len(input))
	for _, item := range input {
		out = append(out, cloneAnyMap(item))
	}
	return out
}

func cloneAnyMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = cloneAnyValue(value)
	}
	return out
}

func cloneAnySlice(input []any) []any {
	if input == nil {
		return nil
	}
	out := make([]any, len(input))
	for index, value := range input {
		out[index] = cloneAnyValue(value)
	}
	return out
}

func cloneAnyValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneAnyMap(typed)
	case []any:
		return cloneAnySlice(typed)
	default:
		return typed
	}
}
