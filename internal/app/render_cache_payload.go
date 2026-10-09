package app

import (
	"encoding/json"
	"fmt"

	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/render"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// renderCachePayload is the persistent-cache serialization of a successful
// RenderResult. render.Manifest carries no JSON tags, so the app layer owns
// explicit DTOs; internal/rendercache stores these bytes opaquely.
type renderCachePayload struct {
	Manifests        []renderCacheManifest   `json:"manifests"`
	Hooks            []renderCacheManifest   `json:"hooks,omitempty"`
	Diagnostics      []diagnostic.Diagnostic `json:"diagnostics,omitempty"`
	PluginExecutions []PluginExecution       `json:"pluginExecutions,omitempty"`
}

type renderCacheManifest struct {
	SourceIndex                  int                        `json:"sourceIndex"`
	SourceName                   string                     `json:"sourceName,omitempty"`
	Path                         string                     `json:"path,omitempty"`
	NamespaceBeforeNormalization string                     `json:"namespaceBeforeNormalization,omitempty"`
	Object                       *unstructured.Unstructured `json:"object,omitempty"`
}

func marshalRenderResultPayload(result RenderResult) ([]byte, error) {
	payload := renderCachePayload{
		Manifests:        renderCacheManifestsFromRender(result.Manifests),
		Hooks:            renderCacheManifestsFromRender(result.Hooks),
		Diagnostics:      result.Diagnostics,
		PluginExecutions: result.PluginExecutions,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode render cache payload: %w", err)
	}
	return data, nil
}

func unmarshalRenderResultPayload(data []byte) (RenderResult, error) {
	var payload renderCachePayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return RenderResult{}, fmt.Errorf("decode render cache payload: %w", err)
	}
	return RenderResult{
		Manifests:        renderManifestsFromCache(payload.Manifests),
		Hooks:            renderManifestsFromCache(payload.Hooks),
		Diagnostics:      payload.Diagnostics,
		PluginExecutions: payload.PluginExecutions,
	}, nil
}

// renderCacheManifestsFromRender preserves nil (absent) versus empty so a
// round trip reproduces the original RenderResult exactly.
func renderCacheManifestsFromRender(items []render.Manifest) []renderCacheManifest {
	if items == nil {
		return nil
	}
	out := make([]renderCacheManifest, 0, len(items))
	for _, item := range items {
		out = append(out, renderCacheManifest{
			SourceIndex:                  item.SourceIndex,
			SourceName:                   item.SourceName,
			Path:                         item.Path,
			NamespaceBeforeNormalization: item.NamespaceBeforeNormalization,
			Object:                       item.Object,
		})
	}
	return out
}

func renderManifestsFromCache(items []renderCacheManifest) []render.Manifest {
	if items == nil {
		return nil
	}
	out := make([]render.Manifest, 0, len(items))
	for _, item := range items {
		out = append(out, render.Manifest{
			SourceIndex:                  item.SourceIndex,
			SourceName:                   item.SourceName,
			Path:                         item.Path,
			NamespaceBeforeNormalization: item.NamespaceBeforeNormalization,
			Object:                       item.Object,
		})
	}
	return out
}
