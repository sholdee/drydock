package drydock

import (
	"context"
	"fmt"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/diagnostic"
	renderpkg "github.com/sholdee/drydock/internal/render"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// PluginRenderer renders Argo CD config management plugin sources for embedded
// callers. The default CLI and public API paths do not execute plugin commands.
type PluginRenderer interface {
	RenderPlugin(ctx context.Context, request PluginRequest) (PluginResult, error)
}

// PluginRegistry dispatches plugin render requests by explicit plugin name.
// It never discovers plugins or executes plugin commands itself.
type PluginRegistry struct {
	renderers map[string]PluginRenderer
}

// NewPluginRegistry creates a named in-process plugin renderer registry.
//
// Plugin names are trimmed. A source with an empty plugin name only matches an
// explicitly registered empty-name renderer.
func NewPluginRegistry(renderers map[string]PluginRenderer) *PluginRegistry {
	registry := &PluginRegistry{renderers: make(map[string]PluginRenderer, len(renderers))}
	for name, renderer := range renderers {
		registry.renderers[strings.TrimSpace(name)] = renderer
	}
	return registry
}

// RenderPlugin renders a plugin source with the registered renderer for the
// requested plugin name.
func (registry *PluginRegistry) RenderPlugin(ctx context.Context, request PluginRequest) (PluginResult, error) {
	name := strings.TrimSpace(request.Plugin.Name)
	renderer, ok := registry.renderer(name)
	if !ok {
		message := fmt.Sprintf("config management plugin %s is not registered in plugin registry", pluginDisplayName(name))
		return PluginResult{Diagnostics: []Diagnostic{{
			Code:     diagnostic.CodePluginUnsupported,
			Severity: "error",
			Category: "plugin",
			Message:  message,
		}}}, fmt.Errorf("%s", message)
	}
	return renderer.RenderPlugin(ctx, request)
}

func (registry *PluginRegistry) renderer(name string) (PluginRenderer, bool) {
	if registry == nil {
		return nil, false
	}
	renderer, ok := registry.renderers[name]
	return renderer, ok && renderer != nil
}

func pluginDisplayName(name string) string {
	if name == "" {
		return "<unnamed>"
	}
	return name
}

// PluginRequest is passed to an injected PluginRenderer.
type PluginRequest struct {
	Application          Application
	DestinationNamespace string
	Source               PluginSource
	Plugin               PluginConfig
}

// PluginSource describes the resolved source for a plugin render.
type PluginSource struct {
	RepoRoot       string
	Path           string
	RepoURL        string
	TargetRevision string
}

// PluginConfig is the explicit plugin configuration from an Application source.
type PluginConfig struct {
	Name       string
	Env        []PluginEnvEntry
	Parameters []PluginParameter
}

// PluginEnvEntry is one explicit plugin environment entry.
type PluginEnvEntry struct {
	Name  string
	Value string
}

// PluginParameter is one plugin parameter. String, Map, and Array preserve
// Argo CD's distinct optional value semantics.
type PluginParameter struct {
	Name   string
	String *string
	Map    *PluginMapParameter
	Array  *PluginArrayParameter
}

// PluginMapParameter wraps a map parameter so present-empty maps are distinct
// from absent map parameters.
type PluginMapParameter struct {
	Values map[string]string
}

// PluginArrayParameter wraps an array parameter so present-empty arrays are
// distinct from absent array parameters.
type PluginArrayParameter struct {
	Values []string
}

// PluginResult is returned by an injected PluginRenderer.
type PluginResult struct {
	Manifests   []PluginManifest
	Diagnostics []Diagnostic
}

// PluginManifest is one rendered plugin object with optional source path.
type PluginManifest struct {
	Path   string
	Object map[string]any
}

func pluginRequestFromInternal(request renderpkg.PluginRequest) PluginRequest {
	return PluginRequest{
		Application: Application{
			Namespace: request.AppNamespace,
			Name:      request.AppName,
			Project:   request.Project,
		},
		DestinationNamespace: request.Namespace,
		Source: PluginSource{
			RepoRoot:       request.Source.RepoRoot,
			Path:           request.Source.Path,
			RepoURL:        request.Source.RepoURL,
			TargetRevision: request.Source.TargetRevision,
		},
		Plugin: pluginConfigFromInternal(request.Plugin),
	}
}

func pluginConfigFromInternal(config renderpkg.PluginConfig) PluginConfig {
	return PluginConfig{
		Name:       config.Name,
		Env:        pluginEnvFromInternal(config.Env),
		Parameters: pluginParametersFromInternal(config.Parameters),
	}
}

func pluginEnvFromInternal(env argoappv1.Env) []PluginEnvEntry {
	out := make([]PluginEnvEntry, 0, len(env))
	for _, item := range env {
		out = append(out, PluginEnvEntry{Name: item.Name, Value: item.Value})
	}
	return out
}

func pluginParametersFromInternal(params argoappv1.ApplicationSourcePluginParameters) []PluginParameter {
	out := make([]PluginParameter, 0, len(params))
	for _, item := range params {
		param := PluginParameter{Name: item.Name}
		if item.String_ != nil {
			value := *item.String_
			param.String = &value
		}
		if item.OptionalMap != nil {
			param.Map = &PluginMapParameter{Values: cloneStringMapPresent(item.Map)}
		}
		if item.OptionalArray != nil {
			param.Array = &PluginArrayParameter{Values: append([]string{}, item.Array...)}
		}
		out = append(out, param)
	}
	return out
}

func pluginManifestsToInternal(manifests []PluginManifest) []renderpkg.Manifest {
	out := make([]renderpkg.Manifest, 0, len(manifests))
	for _, item := range manifests {
		out = append(out, renderpkg.Manifest{
			Path:   item.Path,
			Object: &unstructured.Unstructured{Object: cloneMap(item.Object)},
		})
	}
	return out
}

func cloneStringMapPresent(input map[string]string) map[string]string {
	out := make(map[string]string, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

type pluginRendererAdapter struct {
	renderer PluginRenderer
}

func (adapter pluginRendererAdapter) RenderPlugin(ctx context.Context, request renderpkg.PluginRequest) ([]renderpkg.Manifest, []diagnostic.Diagnostic, error) {
	result, err := adapter.renderer.RenderPlugin(ctx, pluginRequestFromInternal(request))
	return pluginManifestsToInternal(result.Manifests), diagnosticsToInternal(result.Diagnostics), err
}
