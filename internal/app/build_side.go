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
	result.discovered = &discovered
	result.pluginOptions = request.PluginOptions
	result.CacheEvents = append(result.CacheEvents, cacheEvents...)
	discoveryDiags = request.normalizeDiagnostics(discoveryDiags, false)
	result.Diagnostics = append(result.Diagnostics, discoveryDiags...)
	return discovered, discoveryDiags, err
}

func loadBuildSideSettings(root string, request BuildRequest, discovered discovery.Result, result *BuildResult) ([]diagnostic.Diagnostic, error) {
	// When the namespace did not settle, the settings keep the namespace
	// that discovery used (discoverWithControllerNamespace).
	request.controllerNamespaceHint = discovered.ControllerNamespaceHint
	settings, landing, settingsDiags, err := loadSettingsWithLanding(root, request.namespaceScope(), discovered)
	if err != nil {
		return nil, err
	}
	result.Settings = settings
	result.landing = landing
	settingsDiags = request.normalizeDiagnostics(settingsDiags, false)
	result.Diagnostics = append(result.Diagnostics, settingsDiags...)
	return settingsDiags, nil
}
