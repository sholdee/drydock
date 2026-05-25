package requestopts

import (
	"testing"
	"time"

	"github.com/sholdee/drydock/internal/appset"
	"github.com/sholdee/drydock/internal/chart"
	"github.com/sholdee/drydock/internal/remote"
	"github.com/sholdee/drydock/internal/source"
)

func TestOptionsBuildCopiesSharedFields(t *testing.T) {
	options := Options{
		Path:                         "repo",
		Strict:                       true,
		Offline:                      true,
		RefreshCharts:                true,
		ChartCacheDir:                "chart-cache",
		ChartCredentials:             chart.ChartCredentials{Username: "helm-user"},
		RepoMaps:                     []source.RepoMap{{URL: "https://example.test/repo.git", Path: "/repo"}},
		AllowNetwork:                 true,
		GitCacheDir:                  "git-cache",
		RefreshGit:                   true,
		GitCredentials:               source.GitCredentials{Username: "git-user"},
		RefreshRemoteResources:       true,
		RemoteResourceCacheDir:       "remote-cache",
		RemoteResourceForbiddenRoots: []string{"/repo"},
		RemoteResourceCredentials:    remote.Credentials{Username: "remote-user"},
		RemoteResourceGitCredentials: remote.GitCredentials{Username: "remote-git-user"},
		PluginTimeout:                time.Second,
		Parallelism:                  7,
		SkipKinds:                    []string{"Secret"},
		SkipCRDs:                     true,
		SkipSecrets:                  true,
		ApplicationSetProviderFixtures: []string{
			"fixtures.yaml",
		},
		ApplicationSetProviderData: appset.ProviderData{
			Clusters: []appset.ClusterInput{{
				Name:   "prod",
				Labels: map[string]string{"environment": "prod"},
			}},
		},
		RecordCacheEvents: true,
	}

	request := options.Build()
	if request.Path != "repo" || !request.Strict || !request.Offline || !request.RefreshCharts {
		t.Fatalf("basic build fields were not propagated: %#v", request)
	}
	if request.ChartCacheDir != "chart-cache" || request.GitCacheDir != "git-cache" || request.RemoteResourceCacheDir != "remote-cache" {
		t.Fatalf("cache dirs were not propagated: %#v", request)
	}
	if request.ChartCredentials.Username != "helm-user" || request.GitCredentials.Username != "git-user" {
		t.Fatalf("source credentials were not propagated: %#v", request)
	}
	if request.RemoteResourceCredentials.Username != "remote-user" || request.RemoteResourceGitCredentials.Username != "remote-git-user" {
		t.Fatalf("remote credentials were not propagated: %#v", request)
	}
	if !request.AllowNetwork || !request.RefreshGit || !request.RefreshRemoteResources {
		t.Fatalf("refresh/network fields were not propagated: %#v", request)
	}
	if request.PluginTimeout != time.Second || request.Parallelism != 7 || !request.RecordCacheEvents {
		t.Fatalf("runtime fields were not propagated: %#v", request)
	}
	if !request.SkipCRDs || !request.SkipSecrets {
		t.Fatalf("resource filter booleans were not propagated: %#v", request)
	}
	if len(request.RepoMaps) != 1 || request.RepoMaps[0].Path != "/repo" {
		t.Fatalf("repo maps = %#v, want copied map", request.RepoMaps)
	}
	if len(request.RemoteResourceForbiddenRoots) != 1 || request.RemoteResourceForbiddenRoots[0] != "/repo" {
		t.Fatalf("remote forbidden roots = %#v, want copied root", request.RemoteResourceForbiddenRoots)
	}
	if len(request.ApplicationSetProviderData.Clusters) != 1 || request.ApplicationSetProviderData.Clusters[0].Name != "prod" {
		t.Fatalf("provider data = %#v, want prod cluster", request.ApplicationSetProviderData)
	}
	if request.ApplicationSetProviderData.Clusters[0].Labels["environment"] != "prod" {
		t.Fatalf("provider data labels = %#v, want prod environment", request.ApplicationSetProviderData.Clusters[0].Labels)
	}
	if len(request.SkipKinds) != 1 || request.SkipKinds[0] != "Secret" {
		t.Fatalf("skip kinds = %#v, want Secret", request.SkipKinds)
	}
	if len(request.ApplicationSetProviderFixtures) != 1 || request.ApplicationSetProviderFixtures[0] != "fixtures.yaml" {
		t.Fatalf("provider fixtures = %#v, want fixtures.yaml", request.ApplicationSetProviderFixtures)
	}
	options.RepoMaps[0].Path = "/mutated"
	options.RemoteResourceForbiddenRoots[0] = "/mutated"
	options.SkipKinds[0] = "ConfigMap"
	options.ApplicationSetProviderFixtures[0] = "mutated.yaml"
	options.ApplicationSetProviderData.Clusters[0].Name = "mutated"
	options.ApplicationSetProviderData.Clusters[0].Labels["environment"] = "mutated"
	if request.RepoMaps[0].Path != "/repo" || request.RemoteResourceForbiddenRoots[0] != "/repo" || request.SkipKinds[0] != "Secret" || request.ApplicationSetProviderFixtures[0] != "fixtures.yaml" {
		t.Fatalf("request slices share backing storage with options")
	}
	if request.ApplicationSetProviderData.Clusters[0].Name != "prod" || request.ApplicationSetProviderData.Clusters[0].Labels["environment"] != "prod" {
		t.Fatalf("provider data shares backing storage with options")
	}
}

func TestOptionsDiffCopiesSharedFields(t *testing.T) {
	options := Options{
		LeftPath:               "left",
		RightPath:              "right",
		StrictChangedOnly:      true,
		Strict:                 true,
		StripAttrs:             []string{"metadata.annotations.checksum/config"},
		Offline:                true,
		RefreshCharts:          true,
		ChartCacheDir:          "chart-cache",
		ChartCredentials:       chart.ChartCredentials{Username: "helm-user"},
		RepoMaps:               []source.RepoMap{{URL: "https://example.test/repo.git", Path: "/repo"}},
		AllowNetwork:           true,
		GitCacheDir:            "git-cache",
		RefreshGit:             true,
		GitCredentials:         source.GitCredentials{Username: "git-user"},
		RefreshRemoteResources: true,
		RemoteResourceCacheDir: "remote-cache",
		RemoteResourceCredentials: remote.Credentials{
			Username: "remote-user",
		},
		RemoteResourceGitCredentials: remote.GitCredentials{Username: "remote-git-user"},
		PluginTimeout:                time.Second,
		Parallelism:                  7,
		SkipKinds:                    []string{"Secret"},
		SkipCRDs:                     true,
		SkipSecrets:                  true,
		ApplicationSetProviderFixtures: []string{
			"fixtures.yaml",
		},
		ApplicationSetProviderData: appset.ProviderData{
			Clusters: []appset.ClusterInput{{
				Name:   "prod",
				Labels: map[string]string{"environment": "prod"},
			}},
		},
		RecordCacheEvents: true,
	}

	request := options.Diff()
	if request.LeftPath != "left" || request.RightPath != "right" || !request.StrictChangedOnly || !request.Strict {
		t.Fatalf("basic diff fields were not propagated: %#v", request)
	}
	if request.Unified != 0 {
		t.Fatalf("Unified = %d, want 0", request.Unified)
	}
	if !request.ChangedOnly {
		t.Fatalf("ChangedOnly = false, want true")
	}
	if request.ChartCacheDir != "chart-cache" || request.GitCacheDir != "git-cache" || request.RemoteResourceCacheDir != "remote-cache" {
		t.Fatalf("cache dirs were not propagated: %#v", request)
	}
	if request.ChartCredentials.Username != "helm-user" || request.GitCredentials.Username != "git-user" {
		t.Fatalf("source credentials were not propagated: %#v", request)
	}
	if request.RemoteResourceCredentials.Username != "remote-user" || request.RemoteResourceGitCredentials.Username != "remote-git-user" {
		t.Fatalf("remote credentials were not propagated: %#v", request)
	}
	if !request.AllowNetwork || !request.RefreshGit || !request.RefreshCharts || !request.RefreshRemoteResources {
		t.Fatalf("refresh/network fields were not propagated: %#v", request)
	}
	if request.PluginTimeout != time.Second || request.Parallelism != 7 || !request.RecordCacheEvents {
		t.Fatalf("runtime fields were not propagated: %#v", request)
	}
	if !request.SkipCRDs || !request.SkipSecrets {
		t.Fatalf("resource filter booleans were not propagated: %#v", request)
	}
	if len(request.StripAttrs) != 1 || request.StripAttrs[0] != "metadata.annotations.checksum/config" {
		t.Fatalf("strip attrs = %#v, want copied attr", request.StripAttrs)
	}
	if len(request.RepoMaps) != 1 || request.RepoMaps[0].Path != "/repo" {
		t.Fatalf("repo maps = %#v, want copied map", request.RepoMaps)
	}
	if len(request.ApplicationSetProviderData.Clusters) != 1 || request.ApplicationSetProviderData.Clusters[0].Name != "prod" {
		t.Fatalf("provider data = %#v, want prod cluster", request.ApplicationSetProviderData)
	}
	if request.ApplicationSetProviderData.Clusters[0].Labels["environment"] != "prod" {
		t.Fatalf("provider data labels = %#v, want prod environment", request.ApplicationSetProviderData.Clusters[0].Labels)
	}
	if len(request.SkipKinds) != 1 || request.SkipKinds[0] != "Secret" {
		t.Fatalf("skip kinds = %#v, want Secret", request.SkipKinds)
	}
	if len(request.ApplicationSetProviderFixtures) != 1 || request.ApplicationSetProviderFixtures[0] != "fixtures.yaml" {
		t.Fatalf("provider fixtures = %#v, want fixtures.yaml", request.ApplicationSetProviderFixtures)
	}
	options.StripAttrs[0] = "mutated"
	options.RepoMaps[0].Path = "/mutated"
	options.SkipKinds[0] = "ConfigMap"
	options.ApplicationSetProviderFixtures[0] = "mutated.yaml"
	options.ApplicationSetProviderData.Clusters[0].Name = "mutated"
	options.ApplicationSetProviderData.Clusters[0].Labels["environment"] = "mutated"
	if request.StripAttrs[0] != "metadata.annotations.checksum/config" || request.RepoMaps[0].Path != "/repo" || request.SkipKinds[0] != "Secret" || request.ApplicationSetProviderFixtures[0] != "fixtures.yaml" {
		t.Fatalf("request slices share backing storage with options")
	}
	if request.ApplicationSetProviderData.Clusters[0].Name != "prod" || request.ApplicationSetProviderData.Clusters[0].Labels["environment"] != "prod" {
		t.Fatalf("provider data shares backing storage with options")
	}
}

func TestOptionsDiffHonorsExplicitChangedOnlyFalse(t *testing.T) {
	changedOnly := false
	request := Options{ChangedOnly: &changedOnly, Unified: 9}.Diff()
	if request.ChangedOnly {
		t.Fatalf("ChangedOnly = true, want false")
	}
	if request.Unified != 9 {
		t.Fatalf("Unified = %d, want explicit 9", request.Unified)
	}
}
