package requestopts

import (
	"time"

	"github.com/sholdee/drydock/internal/app"
	"github.com/sholdee/drydock/internal/appset"
	"github.com/sholdee/drydock/internal/chart"
	"github.com/sholdee/drydock/internal/remote"
	"github.com/sholdee/drydock/internal/source"
)

type Options struct {
	Path                           string
	LeftPath                       string
	RightPath                      string
	ChangedOnly                    *bool
	StrictChangedOnly              bool
	Strict                         bool
	ValidateLuaHealth              bool
	Unified                        int
	StripAttrs                     []string
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
	PluginTimeout                  time.Duration
	Parallelism                    int
	SkipKinds                      []string
	SkipCRDs                       bool
	SkipSecrets                    bool
	ApplicationSetProviderFixtures []string
	ApplicationSetProviderData     appset.ProviderData
	RecordCacheEvents              bool
}

func (options Options) Build() app.BuildRequest {
	return app.BuildRequest{
		Path:                           options.Path,
		Strict:                         options.Strict,
		ValidateLuaHealth:              options.ValidateLuaHealth,
		Offline:                        options.Offline,
		RefreshCharts:                  options.RefreshCharts,
		ChartCacheDir:                  options.ChartCacheDir,
		ChartCredentials:               options.ChartCredentials,
		RepoMaps:                       append([]source.RepoMap(nil), options.RepoMaps...),
		GitCacheDir:                    options.GitCacheDir,
		RefreshGit:                     options.RefreshGit,
		GitCredentials:                 options.GitCredentials,
		RefreshRemoteResources:         options.RefreshRemoteResources,
		RemoteResourceCacheDir:         options.RemoteResourceCacheDir,
		RemoteResourceForbiddenRoots:   append([]string(nil), options.RemoteResourceForbiddenRoots...),
		RemoteResourceCredentials:      options.RemoteResourceCredentials,
		RemoteResourceGitCredentials:   options.RemoteResourceGitCredentials,
		PluginTimeout:                  options.PluginTimeout,
		Parallelism:                    options.Parallelism,
		SkipKinds:                      append([]string(nil), options.SkipKinds...),
		SkipCRDs:                       options.SkipCRDs,
		SkipSecrets:                    options.SkipSecrets,
		ApplicationSetProviderFixtures: append([]string(nil), options.ApplicationSetProviderFixtures...),
		ApplicationSetProviderData:     cloneProviderData(options.ApplicationSetProviderData),
		RecordCacheEvents:              options.RecordCacheEvents,
	}
}

func (options Options) Diff() app.DiffRequest {
	changedOnly := true
	if options.ChangedOnly != nil {
		changedOnly = *options.ChangedOnly
	}
	return app.DiffRequest{
		LeftPath:                       options.LeftPath,
		RightPath:                      options.RightPath,
		ChangedOnly:                    changedOnly,
		StrictChangedOnly:              options.StrictChangedOnly,
		Strict:                         options.Strict,
		Unified:                        options.Unified,
		StripAttrs:                     append([]string(nil), options.StripAttrs...),
		Offline:                        options.Offline,
		RefreshCharts:                  options.RefreshCharts,
		ChartCacheDir:                  options.ChartCacheDir,
		ChartCredentials:               options.ChartCredentials,
		RepoMaps:                       append([]source.RepoMap(nil), options.RepoMaps...),
		GitCacheDir:                    options.GitCacheDir,
		RefreshGit:                     options.RefreshGit,
		GitCredentials:                 options.GitCredentials,
		RefreshRemoteResources:         options.RefreshRemoteResources,
		RemoteResourceCacheDir:         options.RemoteResourceCacheDir,
		RemoteResourceCredentials:      options.RemoteResourceCredentials,
		RemoteResourceGitCredentials:   options.RemoteResourceGitCredentials,
		PluginTimeout:                  options.PluginTimeout,
		Parallelism:                    options.Parallelism,
		SkipKinds:                      append([]string(nil), options.SkipKinds...),
		SkipCRDs:                       options.SkipCRDs,
		SkipSecrets:                    options.SkipSecrets,
		ApplicationSetProviderFixtures: append([]string(nil), options.ApplicationSetProviderFixtures...),
		ApplicationSetProviderData:     cloneProviderData(options.ApplicationSetProviderData),
		RecordCacheEvents:              options.RecordCacheEvents,
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
	for key, value := range input {
		out[key] = value
	}
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
