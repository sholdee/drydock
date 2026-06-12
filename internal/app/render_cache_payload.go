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
		Diagnostics:      result.Diagnostics,
		PluginExecutions: result.PluginExecutions,
	}
	if result.Manifests != nil {
		payload.Manifests = make([]renderCacheManifest, 0, len(result.Manifests))
		for _, item := range result.Manifests {
			payload.Manifests = append(payload.Manifests, renderCacheManifest{
				SourceIndex:                  item.SourceIndex,
				SourceName:                   item.SourceName,
				Path:                         item.Path,
				NamespaceBeforeNormalization: item.NamespaceBeforeNormalization,
				Object:                       item.Object,
			})
		}
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
	result := RenderResult{
		Diagnostics:      payload.Diagnostics,
		PluginExecutions: payload.PluginExecutions,
	}
	if payload.Manifests != nil {
		result.Manifests = make([]render.Manifest, 0, len(payload.Manifests))
		for _, item := range payload.Manifests {
			result.Manifests = append(result.Manifests, render.Manifest{
				SourceIndex:                  item.SourceIndex,
				SourceName:                   item.SourceName,
				Path:                         item.Path,
				NamespaceBeforeNormalization: item.NamespaceBeforeNormalization,
				Object:                       item.Object,
			})
		}
	}
	return result, nil
}
