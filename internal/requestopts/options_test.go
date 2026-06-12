package requestopts

import (
	"reflect"
	"testing"
	"time"

	"github.com/sholdee/drydock/internal/appset"
	"github.com/sholdee/drydock/internal/chart"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/remote"
	"github.com/sholdee/drydock/internal/rendercache"
	"github.com/sholdee/drydock/internal/source"
)

func TestOptionsBuildCopiesSharedFields(t *testing.T) {
	options := fixtureOptions()

	request := options.Build()

	assertDeepEqual(t, "Path", request.Path, "repo")
	assertDeepEqual(t, "Strict", request.Strict, true)
	assertDeepEqual(t, "ProjectDiagnosticsMode", request.ProjectDiagnosticsMode, diagnostic.ProjectDiagnosticsModeAll)
	assertDeepEqual(t, "DiscoveryMode", request.DiscoveryMode, "fleet")
	assertDeepEqual(t, "MaxDiscoveryDepth", request.MaxDiscoveryDepth, 2)
	assertDeepEqual(t, "MaxDiscoveryDepthSet", request.MaxDiscoveryDepthSet, true)
	assertDeepEqual(t, "DiscoverKustomizePaths", request.DiscoverKustomizePaths, []string{"argocd/overlays/prod"})
	assertDeepEqual(t, "Offline", request.Offline, true)
	assertDeepEqual(t, "RefreshCharts", request.RefreshCharts, true)
	assertDeepEqual(t, "ChartCacheDir", request.ChartCacheDir, "chart-cache")
	assertDeepEqual(t, "ChartCredentials", request.ChartCredentials, chart.ChartCredentials{Username: "helm-user"})
	assertDeepEqual(t, "RepoMaps", request.RepoMaps, []source.RepoMap{{URL: "https://example.test/repo.git", Path: "/repo"}})
	assertDeepEqual(t, "GitCacheDir", request.GitCacheDir, "git-cache")
	assertDeepEqual(t, "RefreshGit", request.RefreshGit, true)
	assertDeepEqual(t, "GitCredentials", request.GitCredentials, source.GitCredentials{Username: "git-user"})
	assertDeepEqual(t, "RefreshRemoteResources", request.RefreshRemoteResources, true)
	assertDeepEqual(t, "RemoteResourceCacheDir", request.RemoteResourceCacheDir, "remote-cache")
	assertDeepEqual(t, "RemoteResourceForbiddenRoots", request.RemoteResourceForbiddenRoots, []string{"/repo"})
	assertDeepEqual(t, "RemoteResourceCredentials", request.RemoteResourceCredentials, remote.Credentials{Username: "remote-user"})
	assertDeepEqual(t, "RemoteResourceGitCredentials", request.RemoteResourceGitCredentials, remote.GitCredentials{Username: "remote-git-user"})
	assertDeepEqual(t, "EnableAVPCompat", request.EnableAVPCompat, true)
	assertDeepEqual(t, "EnablePlugins", request.EnablePlugins, true)
	assertDeepEqual(t, "PluginCacheDir", request.PluginCacheDir, "plugin-cache")
	assertDeepEqual(t, "PluginPolicyPath", request.PluginPolicyPath, ".drydock/custom-plugins.yaml")
	assertDeepEqual(t, "PluginPolicyPathExplicit", request.PluginPolicyPathExplicit, true)
	assertDeepEqual(t, "PluginPolicyRef", request.PluginPolicyRef, "main")
	assertDeepEqual(t, "PluginPolicyRepo", request.PluginPolicyRepo, "policy-repo")
	assertDeepEqual(t, "DisablePluginPolicy", request.DisablePluginPolicy, false)
	assertDeepEqual(t, "PluginTimeout", request.PluginTimeout, time.Second)
	assertDeepEqual(t, "Parallelism", request.Parallelism, 7)
	assertDeepEqual(t, "SkipKinds", request.SkipKinds, []string{"Secret"})
	assertDeepEqual(t, "SkipCRDs", request.SkipCRDs, true)
	assertDeepEqual(t, "SkipSecrets", request.SkipSecrets, true)
	assertDeepEqual(t, "ApplicationSetProviderFixtures", request.ApplicationSetProviderFixtures, []string{"fixtures.yaml"})
	assertDeepEqual(t, "ApplicationSetProviderData", request.ApplicationSetProviderData, providerDataFixture())
	assertDeepEqual(t, "RecordCacheEvents", request.RecordCacheEvents, true)
	assertDeepEqual(t, "RenderCacheEnabled", request.RenderCacheEnabled, true)
	assertDeepEqual(t, "RenderCacheDir", request.RenderCacheDir, "render-cache")
	assertDeepEqual(t, "RenderCacheMaxBytes", request.RenderCacheMaxBytes, int64(123456))
	assertDeepEqual(t, "RefreshRenders", request.RefreshRenders, true)
	assertDeepEqual(t, "EngineFingerprint", request.EngineFingerprint, rendercache.EngineFingerprint{Version: "1.2.3", Commit: "abc"})

	mutateOptions(&options)
	assertDeepEqual(t, "copied DiscoverKustomizePaths", request.DiscoverKustomizePaths, []string{"argocd/overlays/prod"})
	assertDeepEqual(t, "copied RepoMaps", request.RepoMaps, []source.RepoMap{{URL: "https://example.test/repo.git", Path: "/repo"}})
	assertDeepEqual(t, "copied RemoteResourceForbiddenRoots", request.RemoteResourceForbiddenRoots, []string{"/repo"})
	assertDeepEqual(t, "copied SkipKinds", request.SkipKinds, []string{"Secret"})
	assertDeepEqual(t, "copied ApplicationSetProviderFixtures", request.ApplicationSetProviderFixtures, []string{"fixtures.yaml"})
	assertDeepEqual(t, "copied ApplicationSetProviderData", request.ApplicationSetProviderData, providerDataFixture())
}

func TestOptionsDiffCopiesSharedFields(t *testing.T) {
	options := fixtureOptions()

	request := options.Diff()

	assertDeepEqual(t, "LeftPath", request.LeftPath, "left")
	assertDeepEqual(t, "RightPath", request.RightPath, "right")
	assertDeepEqual(t, "Repo", request.Repo, "repo-root")
	assertDeepEqual(t, "Ref", request.Ref, "feature")
	assertDeepEqual(t, "RefOrig", request.RefOrig, "main")
	assertDeepEqual(t, "DiscoveryMode", request.DiscoveryMode, "fleet")
	assertDeepEqual(t, "MaxDiscoveryDepth", request.MaxDiscoveryDepth, 2)
	assertDeepEqual(t, "MaxDiscoveryDepthSet", request.MaxDiscoveryDepthSet, true)
	assertDeepEqual(t, "DiscoverKustomizePaths", request.DiscoverKustomizePaths, []string{"argocd/overlays/prod"})
	assertDeepEqual(t, "ChangedOnly", request.ChangedOnly, true)
	assertDeepEqual(t, "ChangedOnlyIncludeGlobs", request.ChangedOnlyIncludeGlobs, []string{"apps/**"})
	assertDeepEqual(t, "ChangedOnlyIgnoreGlobs", request.ChangedOnlyIgnoreGlobs, []string{".github/**"})
	assertDeepEqual(t, "StrictChangedOnly", request.StrictChangedOnly, true)
	assertDeepEqual(t, "Strict", request.Strict, true)
	assertDeepEqual(t, "ProjectDiagnosticsMode", request.ProjectDiagnosticsMode, diagnostic.ProjectDiagnosticsModeAll)
	assertDeepEqual(t, "Unified", request.Unified, 0)
	assertDeepEqual(t, "StripAttrs", request.StripAttrs, []string{"metadata.annotations.checksum/config"})
	assertDeepEqual(t, "ShowIgnoredFields", request.ShowIgnoredFields, true)
	assertDeepEqual(t, "Offline", request.Offline, true)
	assertDeepEqual(t, "RefreshCharts", request.RefreshCharts, true)
	assertDeepEqual(t, "ChartCacheDir", request.ChartCacheDir, "chart-cache")
	assertDeepEqual(t, "ChartCredentials", request.ChartCredentials, chart.ChartCredentials{Username: "helm-user"})
	assertDeepEqual(t, "RepoMaps", request.RepoMaps, []source.RepoMap{{URL: "https://example.test/repo.git", Path: "/repo"}})
	assertDeepEqual(t, "GitCacheDir", request.GitCacheDir, "git-cache")
	assertDeepEqual(t, "RefreshGit", request.RefreshGit, true)
	assertDeepEqual(t, "GitCredentials", request.GitCredentials, source.GitCredentials{Username: "git-user"})
	assertDeepEqual(t, "RefreshRemoteResources", request.RefreshRemoteResources, true)
	assertDeepEqual(t, "RemoteResourceCacheDir", request.RemoteResourceCacheDir, "remote-cache")
	assertDeepEqual(t, "RemoteResourceCredentials", request.RemoteResourceCredentials, remote.Credentials{Username: "remote-user"})
	assertDeepEqual(t, "RemoteResourceGitCredentials", request.RemoteResourceGitCredentials, remote.GitCredentials{Username: "remote-git-user"})
	assertDeepEqual(t, "EnableAVPCompat", request.EnableAVPCompat, true)
	assertDeepEqual(t, "EnablePlugins", request.EnablePlugins, true)
	assertDeepEqual(t, "PluginCacheDir", request.PluginCacheDir, "plugin-cache")
	assertDeepEqual(t, "PluginPolicyPath", request.PluginPolicyPath, ".drydock/custom-plugins.yaml")
	assertDeepEqual(t, "PluginPolicyPathExplicit", request.PluginPolicyPathExplicit, true)
	assertDeepEqual(t, "PluginPolicyRef", request.PluginPolicyRef, "main")
	assertDeepEqual(t, "PluginPolicyRepo", request.PluginPolicyRepo, "policy-repo")
	assertDeepEqual(t, "DisablePluginPolicy", request.DisablePluginPolicy, false)
	assertDeepEqual(t, "PluginTimeout", request.PluginTimeout, time.Second)
	assertDeepEqual(t, "Parallelism", request.Parallelism, 7)
	assertDeepEqual(t, "SkipKinds", request.SkipKinds, []string{"Secret"})
	assertDeepEqual(t, "SkipCRDs", request.SkipCRDs, true)
	assertDeepEqual(t, "SkipSecrets", request.SkipSecrets, true)
	assertDeepEqual(t, "ApplicationSetProviderFixtures", request.ApplicationSetProviderFixtures, []string{"fixtures.yaml"})
	assertDeepEqual(t, "ApplicationSetProviderData", request.ApplicationSetProviderData, providerDataFixture())
	assertDeepEqual(t, "RecordCacheEvents", request.RecordCacheEvents, true)
	assertDeepEqual(t, "RenderCacheEnabled", request.RenderCacheEnabled, true)
	assertDeepEqual(t, "RenderCacheDir", request.RenderCacheDir, "render-cache")
	assertDeepEqual(t, "RenderCacheMaxBytes", request.RenderCacheMaxBytes, int64(123456))
	assertDeepEqual(t, "RefreshRenders", request.RefreshRenders, true)
	assertDeepEqual(t, "EngineFingerprint", request.EngineFingerprint, rendercache.EngineFingerprint{Version: "1.2.3", Commit: "abc"})

	mutateOptions(&options)
	assertDeepEqual(t, "copied DiscoverKustomizePaths", request.DiscoverKustomizePaths, []string{"argocd/overlays/prod"})
	assertDeepEqual(t, "copied ChangedOnlyIncludeGlobs", request.ChangedOnlyIncludeGlobs, []string{"apps/**"})
	assertDeepEqual(t, "copied ChangedOnlyIgnoreGlobs", request.ChangedOnlyIgnoreGlobs, []string{".github/**"})
	assertDeepEqual(t, "copied StripAttrs", request.StripAttrs, []string{"metadata.annotations.checksum/config"})
	assertDeepEqual(t, "copied RepoMaps", request.RepoMaps, []source.RepoMap{{URL: "https://example.test/repo.git", Path: "/repo"}})
	assertDeepEqual(t, "copied SkipKinds", request.SkipKinds, []string{"Secret"})
	assertDeepEqual(t, "copied ApplicationSetProviderFixtures", request.ApplicationSetProviderFixtures, []string{"fixtures.yaml"})
	assertDeepEqual(t, "copied ApplicationSetProviderData", request.ApplicationSetProviderData, providerDataFixture())
}

func TestOptionsDiffHonorsExplicitChangedOnlyFalse(t *testing.T) {
	changedOnly := false
	request := Options{ChangedOnly: &changedOnly, Unified: 9}.Diff()

	assertDeepEqual(t, "ChangedOnly", request.ChangedOnly, false)
	assertDeepEqual(t, "Unified", request.Unified, 9)
}

func fixtureOptions() Options {
	return Options{
		Path:                         "repo",
		LeftPath:                     "left",
		RightPath:                    "right",
		Repo:                         "repo-root",
		Ref:                          "feature",
		RefOrig:                      "main",
		DiscoveryMode:                "fleet",
		MaxDiscoveryDepth:            2,
		MaxDiscoveryDepthSet:         true,
		DiscoverKustomizePaths:       []string{"argocd/overlays/prod"},
		ChangedOnlyIncludes:          []string{"apps/**"},
		ChangedOnlyIgnores:           []string{".github/**"},
		StrictChangedOnly:            true,
		Strict:                       true,
		ProjectDiagnosticsMode:       diagnostic.ProjectDiagnosticsModeAll,
		StripAttrs:                   []string{"metadata.annotations.checksum/config"},
		ShowIgnoredFields:            true,
		Offline:                      true,
		RefreshCharts:                true,
		ChartCacheDir:                "chart-cache",
		ChartCredentials:             chart.ChartCredentials{Username: "helm-user"},
		RepoMaps:                     []source.RepoMap{{URL: "https://example.test/repo.git", Path: "/repo"}},
		GitCacheDir:                  "git-cache",
		RefreshGit:                   true,
		GitCredentials:               source.GitCredentials{Username: "git-user"},
		RefreshRemoteResources:       true,
		RemoteResourceCacheDir:       "remote-cache",
		RemoteResourceForbiddenRoots: []string{"/repo"},
		RemoteResourceCredentials:    remote.Credentials{Username: "remote-user"},
		RemoteResourceGitCredentials: remote.GitCredentials{Username: "remote-git-user"},
		EnableAVPCompat:              true,
		EnablePlugins:                true,
		PluginCacheDir:               "plugin-cache",
		PluginPolicyPath:             ".drydock/custom-plugins.yaml",
		PluginPolicyPathExplicit:     true,
		PluginPolicyRef:              "main",
		PluginPolicyRepo:             "policy-repo",
		PluginTimeout:                time.Second,
		Parallelism:                  7,
		SkipKinds:                    []string{"Secret"},
		SkipCRDs:                     true,
		SkipSecrets:                  true,
		ApplicationSetProviderFixtures: []string{
			"fixtures.yaml",
		},
		ApplicationSetProviderData: providerDataFixture(),
		RecordCacheEvents:          true,
		RenderCacheEnabled:         true,
		RenderCacheDir:             "render-cache",
		RenderCacheMaxBytes:        123456,
		RefreshRenders:             true,
		EngineFingerprint:          rendercache.EngineFingerprint{Version: "1.2.3", Commit: "abc"},
	}
}

func providerDataFixture() appset.ProviderData {
	return appset.ProviderData{
		Clusters: []appset.ClusterInput{{
			Name:   "prod",
			Labels: map[string]string{"environment": "prod"},
		}},
	}
}

func mutateOptions(options *Options) {
	options.StripAttrs[0] = "mutated"
	options.DiscoverKustomizePaths[0] = "mutated"
	options.ChangedOnlyIncludes[0] = "mutated"
	options.ChangedOnlyIgnores[0] = "mutated"
	options.RepoMaps[0].Path = "/mutated"
	options.RemoteResourceForbiddenRoots[0] = "/mutated"
	options.SkipKinds[0] = "ConfigMap"
	options.ApplicationSetProviderFixtures[0] = "mutated.yaml"
	options.ApplicationSetProviderData.Clusters[0].Name = "mutated"
	options.ApplicationSetProviderData.Clusters[0].Labels["environment"] = "mutated"
}

func assertDeepEqual(t *testing.T, name string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s = %#v, want %#v", name, got, want)
	}
}
