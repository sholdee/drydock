package app

import (
	"context"

	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/discovery"
)

func (o Orchestrator) loadBuildSideDiscovery(ctx context.Context, root string, request BuildRequest, result *BuildResult) (discovery.Result, []diagnostic.Diagnostic, error) {
	discovered, discoveryDiags, cacheEvents, renderCache, renderSettingsSignature, err := o.discoverRepository(ctx, root, request)
	result.renderCache = renderCache
	result.renderSettingsSignature = renderSettingsSignature
	result.CacheEvents = append(result.CacheEvents, cacheEvents...)
	discoveryDiags = normalizeDiagnostics(discoveryDiags, request.Strict, false)
	result.Diagnostics = append(result.Diagnostics, discoveryDiags...)
	return discovered, discoveryDiags, err
}

func loadBuildSideSettings(root string, request BuildRequest, discovered discovery.Result, result *BuildResult) ([]diagnostic.Diagnostic, error) {
	settings, settingsDiags, err := loadSettingsFromDiscovery(root, discovered)
	if err != nil {
		return nil, err
	}
	result.Settings = settings
	settingsDiags = normalizeDiagnostics(settingsDiags, request.Strict, false)
	result.Diagnostics = append(result.Diagnostics, settingsDiags...)
	return settingsDiags, nil
}
