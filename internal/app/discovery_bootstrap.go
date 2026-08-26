package app

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/appset"
	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/discovery"
	"github.com/sholdee/drydock/internal/pluginpolicy"
	"github.com/sholdee/drydock/internal/render"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	policyBootstrapApplicationNamespace = "argocd"
	policyBootstrapApplicationProject   = "default"
	policyBootstrapDestinationName      = "in-cluster"
	policyBootstrapDestinationNamespace = "argocd"
)

func (o Orchestrator) applyPolicyBootstrapDiscovery(ctx context.Context, root string, request BuildRequest, discovered discovery.Result, appsetOptions appset.Options, renderCache *applicationRenderCache, mode string) (discovery.Result, []diagnostic.Diagnostic, []cacheevent.Event, error) {
	if !policyBootstrapDiscoveryEnabled(request, mode) {
		return discovered, nil, nil, nil
	}

	settings, _, err := loadSettingsFromDiscovery(root, discovered)
	if err != nil {
		return discovered, nil, nil, err
	}
	renderSig, err := renderSettingsSignature(settings)
	if err != nil {
		return discovered, nil, nil, err
	}

	recorder := cacheevent.NewRecorder(request.RecordCacheEvents)
	provider, cleanup, err := o.discoveryProvider(ctx, root, settings, request, recorder)
	if err != nil {
		return discovered, nil, recorder.Events(), err
	}
	defer cleanup()

	var rendered discovery.Result
	var allDiags []diagnostic.Diagnostic
	for _, entrypoint := range request.pluginPolicy.Bootstrap.Entrypoints {
		next, diags, err := renderPolicyBootstrapEntrypoint(ctx, root, request, provider, renderSig, renderCache, entrypoint)
		allDiags = append(allDiags, diags...)
		if err != nil {
			return discovered, allDiags, recorder.Events(), err
		}
		var mergeDiags []diagnostic.Diagnostic
		rendered, mergeDiags = mergeDiscoveryResultsWithDiagnostics(rendered, next)
		allDiags = append(allDiags, mergeDiags...)
	}

	next, mergeDiags := mergeDiscoveryResultsWithDiagnostics(discovered, rendered)
	allDiags = append(allDiags, mergeDiags...)
	var expansionDiags []diagnostic.Diagnostic
	next, expansionDiags, err = expandApplicationSetDiscovery(root, request, next, appsetOptions)
	allDiags = append(allDiags, expansionDiags...)
	if err != nil {
		return next, allDiags, recorder.Events(), err
	}
	return next, allDiags, recorder.Events(), nil
}

func policyBootstrapDiscoveryEnabled(request BuildRequest, mode string) bool {
	return !request.DisablePluginPolicy && len(request.pluginPolicy.Bootstrap.Entrypoints) > 0 && mode == DiscoveryModeFleet
}

func renderPolicyBootstrapEntrypoint(ctx context.Context, root string, request BuildRequest, provider localProvider, settingsSig string, renderCache *applicationRenderCache, entrypoint pluginpolicy.BootstrapEntrypoint) (discovery.Result, []diagnostic.Diagnostic, error) {
	sourcePath, err := validatePolicyBootstrapEntrypoint(root, request.pluginPolicy, entrypoint)
	if err != nil {
		return discovery.Result{}, nil, fmt.Errorf("plugin policy bootstrap entrypoint %q: %w", entrypoint.Name, err)
	}
	application := policyBootstrapEntrypointApplication(entrypoint, sourcePath)
	rendered, err := renderApplicationCached(renderContext{
		context:           ctx,
		provider:          provider,
		cache:             renderCache,
		settingsSignature: settingsSig,
		request:           request,
	}, application)
	if err != nil {
		return discovery.Result{}, rendered.Diagnostics, fmt.Errorf("plugin policy bootstrap entrypoint %q: %w", entrypoint.Name, err)
	}
	return scanPolicyBootstrapEntrypointObjects(request, entrypoint, sourcePath, rendered.Manifests)
}

func validatePolicyBootstrapEntrypoint(root string, policy pluginpolicy.Policy, entrypoint pluginpolicy.BootstrapEntrypoint) (string, error) {
	plugin, ok := policy.Plugin(entrypoint.Plugin)
	if !ok {
		return "", fmt.Errorf("plugin %q is not defined", entrypoint.Plugin)
	}
	discover, discoverBy, ok := policyPluginStaticDiscoverRule(plugin)
	if !ok {
		return "", fmt.Errorf("plugin %q must define match.discover or configManagementPlugin.discover", entrypoint.Plugin)
	}
	sourcePath, err := cleanLocalSourcePath(entrypoint.SourcePath)
	if err != nil {
		return "", err
	}
	if err := rejectLocalSymlinkComponents(root, sourcePath); err != nil {
		return "", err
	}
	sourceDir := filepath.Join(root, sourcePath)
	info, err := os.Lstat(sourceDir)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("source path %q is a symlink", sourcePath)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("source path %q must be a directory", sourcePath)
	}
	matched, err := policyPluginDiscoverMatch(sourceDir, discover)
	if err != nil {
		return "", fmt.Errorf("match %s for plugin %q: %w", discoverBy, entrypoint.Plugin, err)
	}
	if !matched {
		return "", fmt.Errorf("source path %q (sourcePath) did not match plugin %q %s rule", sourcePath, entrypoint.Plugin, discoverBy)
	}
	return filepath.ToSlash(sourcePath), nil
}

func policyBootstrapEntrypointApplication(entrypoint pluginpolicy.BootstrapEntrypoint, sourcePath string) argoappv1.Application {
	return argoappv1.Application{
		Name:      entrypoint.Name,
		Namespace: policyBootstrapApplicationNamespace,
		Spec: argoappv1.ApplicationSpec{
			Project: policyBootstrapApplicationProject,
			Source: &argoappv1.ApplicationSource{
				Path: sourcePath,
				Plugin: &argoappv1.ApplicationSourcePlugin{
					Name:       entrypoint.Plugin,
					Parameters: policyBootstrapPluginParameters(entrypoint.Parameters),
				},
			},
			Destination: argoappv1.ApplicationDestination{
				Name:      policyBootstrapDestinationName,
				Namespace: policyBootstrapDestinationNamespace,
			},
		},
	}
}

func policyBootstrapPluginParameters(parameters []pluginpolicy.BootstrapParameter) argoappv1.ApplicationSourcePluginParameters {
	out := make(argoappv1.ApplicationSourcePluginParameters, 0, len(parameters))
	for _, parameter := range parameters {
		next := argoappv1.ApplicationSourcePluginParameter{Name: parameter.Name}
		switch {
		case parameter.String != nil:
			value := *parameter.String
			next.String_ = &value
		case parameter.Array != nil:
			next.OptionalArray = &argoappv1.OptionalArray{Array: append([]string(nil), parameter.Array.Values...)}
		case parameter.Map != nil:
			values := make(map[string]string, len(parameter.Map.Values))
			maps.Copy(values, parameter.Map.Values)
			next.OptionalMap = &argoappv1.OptionalMap{Map: values}
		}
		out = append(out, next)
	}
	return out
}

func scanPolicyBootstrapEntrypointObjects(request BuildRequest, entrypoint pluginpolicy.BootstrapEntrypoint, sourcePath string, manifests []render.Manifest) (discovery.Result, []diagnostic.Diagnostic, error) {
	var out discovery.Result
	var allDiags []diagnostic.Diagnostic
	for _, renderedManifest := range manifests {
		if renderedManifest.Object == nil {
			continue
		}
		displayPath := policyBootstrapObjectDiscoveryPath(entrypoint, renderedManifest)
		next, err := discovery.ScanObjects(displayPath, []*unstructured.Unstructured{renderedManifest.Object.DeepCopy()})
		if err != nil {
			return out, allDiags, fmt.Errorf("discover plugin policy bootstrap entrypoint %q output %q: %w", entrypoint.Name, displayPath, err)
		}
		markDiscoveryTier(&next, discovery.SourceTierPolicyBootstrap, policyBootstrapInputPaths(request, sourcePath, renderedManifest))
		var mergeDiags []diagnostic.Diagnostic
		out, mergeDiags = mergeDiscoveryResultsWithDiagnostics(out, next)
		allDiags = append(allDiags, mergeDiags...)
	}
	return out, allDiags, nil
}

func policyBootstrapObjectDiscoveryPath(entrypoint pluginpolicy.BootstrapEntrypoint, renderedManifest render.Manifest) string {
	base := filepath.ToSlash(filepath.Join("plugin-policy-bootstrap", entrypoint.Name))
	if renderedManifest.Path == "" {
		return base
	}
	return filepath.ToSlash(filepath.Join(base, renderedManifest.Path))
}

func policyBootstrapInputPaths(request BuildRequest, sourcePath string, renderedManifest render.Manifest) []string {
	inputs := []string{policyBootstrapPolicyInputPath(request), filepath.ToSlash(sourcePath)}
	if renderedManifest.Path != "" {
		inputs = append(inputs, filepath.ToSlash(renderedManifest.Path))
	}
	return uniqueStrings(inputs)
}

func policyBootstrapPolicyInputPath(request BuildRequest) string {
	if strings.TrimSpace(request.PluginPolicyPath) == "" {
		return defaultPluginPolicyPath
	}
	clean, err := cleanPluginPolicyPath(request.PluginPolicyPath)
	if err != nil {
		return filepath.ToSlash(request.PluginPolicyPath)
	}
	return filepath.ToSlash(clean)
}
