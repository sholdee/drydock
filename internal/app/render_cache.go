package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/render"
	sourcepkg "github.com/sholdee/drydock/internal/source"
)

type applicationRenderCache struct {
	mu      sync.Mutex
	entries map[string]applicationRenderCacheEntry
}

type applicationRenderCacheEntry struct {
	result RenderResult
	err    error
}

func newApplicationRenderCache() *applicationRenderCache {
	return &applicationRenderCache{entries: map[string]applicationRenderCacheEntry{}}
}

func (cache *applicationRenderCache) get(key string) (RenderResult, error, bool) {
	if cache == nil || key == "" {
		return RenderResult{}, nil, false
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	entry, ok := cache.entries[key]
	if !ok {
		return RenderResult{}, nil, false
	}
	return cloneRenderResult(entry.result), entry.err, true
}

func (cache *applicationRenderCache) set(key string, result RenderResult, err error) {
	if cache == nil || key == "" {
		return
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.entries[key] = applicationRenderCacheEntry{
		result: cloneRenderResult(result),
		err:    err,
	}
}

func cloneRenderResult(result RenderResult) RenderResult {
	return RenderResult{
		Manifests:   cloneRenderManifests(result.Manifests),
		Diagnostics: cloneDiagnostics(result.Diagnostics),
	}
}

func cloneRenderManifests(input []render.Manifest) []render.Manifest {
	if input == nil {
		return nil
	}
	out := make([]render.Manifest, len(input))
	for i, item := range input {
		out[i] = item
		if item.Object != nil {
			out[i].Object = item.Object.DeepCopy()
		}
	}
	return out
}

func cloneDiagnostics(input []diagnostic.Diagnostic) []diagnostic.Diagnostic {
	if input == nil {
		return nil
	}
	out := make([]diagnostic.Diagnostic, len(input))
	copy(out, input)
	return out
}

func renderApplicationCached(ctx renderContext, application argoappv1.Application) (RenderResult, error) {
	key, err := applicationRenderCacheKey(ctx, application)
	if err != nil {
		return RenderResult{}, err
	}
	if result, err, ok := ctx.cache.get(key); ok {
		return result, err
	}
	if fallbackKey, ok := namespaceDefaultedApplicationRenderCacheKey(ctx, application); ok {
		if result, err, hit := ctx.cache.get(fallbackKey); hit {
			ctx.cache.set(key, result, err)
			return result, err
		}
	}
	result, err := RenderApplication(ctx.context, application, ctx.provider, ctx.request.PluginOptions)
	if ctx.context.Err() == nil {
		ctx.cache.set(key, result, err)
	}
	return result, err
}

func namespaceDefaultedApplicationRenderCacheKey(ctx renderContext, application argoappv1.Application) (string, bool) {
	if application.Namespace == "" || applicationUsesPluginSource(application) {
		return "", false
	}
	fallback := application
	fallback.Namespace = ""
	key, err := applicationRenderCacheKey(ctx, fallback)
	return key, err == nil
}

func applicationUsesPluginSource(application argoappv1.Application) bool {
	sources := application.Spec.Sources
	if len(sources) == 0 && application.Spec.Source != nil {
		sources = argoappv1.ApplicationSources{*application.Spec.Source}
	}
	for _, source := range sources {
		if source.Plugin != nil {
			return true
		}
	}
	return false
}

type renderContext struct {
	context           context.Context
	provider          localProvider
	cache             *applicationRenderCache
	settingsSignature string
	request           BuildRequest
}

func applicationRenderCacheKey(ctx renderContext, application argoappv1.Application) (string, error) {
	input := struct {
		Root                    string                      `json:"root"`
		Application             applicationRenderCacheInput `json:"application"`
		SettingsSignature       string                      `json:"settingsSignature"`
		RepoMaps                []sourcepkg.RepoMap         `json:"repoMaps,omitempty"`
		Offline                 bool                        `json:"offline"`
		RefreshCharts           bool                        `json:"refreshCharts"`
		ChartCacheDir           string                      `json:"chartCacheDir,omitempty"`
		GitCacheDir             string                      `json:"gitCacheDir,omitempty"`
		RefreshGit              bool                        `json:"refreshGit"`
		RefreshRemoteResources  bool                        `json:"refreshRemoteResources"`
		RemoteResourceCacheDir  string                      `json:"remoteResourceCacheDir,omitempty"`
		PluginTimeout           string                      `json:"pluginTimeout,omitempty"`
		EnableAVPCompat         bool                        `json:"enableAVPCompat,omitempty"`
		PluginPolicyFingerprint string                      `json:"pluginPolicyFingerprint,omitempty"`
		HasInjectedPluginRender bool                        `json:"hasInjectedPluginRender"`
	}{
		Root:                    ctx.provider.repoRoot,
		Application:             newApplicationRenderCacheInput(application),
		SettingsSignature:       ctx.settingsSignature,
		RepoMaps:                append([]sourcepkg.RepoMap(nil), ctx.request.RepoMaps...),
		Offline:                 ctx.request.Offline,
		RefreshCharts:           ctx.request.RefreshCharts,
		ChartCacheDir:           ctx.request.ChartCacheDir,
		GitCacheDir:             ctx.request.GitCacheDir,
		RefreshGit:              ctx.request.RefreshGit,
		RefreshRemoteResources:  ctx.request.RefreshRemoteResources,
		RemoteResourceCacheDir:  ctx.request.RemoteResourceCacheDir,
		PluginTimeout:           ctx.request.PluginTimeout.String(),
		EnableAVPCompat:         ctx.request.EnableAVPCompat,
		PluginPolicyFingerprint: ctx.request.pluginPolicyFingerprint,
		HasInjectedPluginRender: ctx.request.PluginRenderer != nil,
	}
	data, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("fingerprint Application %s: %w", applicationDisplayName(application), err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

type applicationRenderCacheInput struct {
	Name      string                    `json:"name"`
	Namespace string                    `json:"namespace,omitempty"`
	Spec      argoappv1.ApplicationSpec `json:"spec"`
}

func newApplicationRenderCacheInput(application argoappv1.Application) applicationRenderCacheInput {
	return applicationRenderCacheInput{
		Name:      application.Name,
		Namespace: application.Namespace,
		Spec:      application.Spec,
	}
}
