package app

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/render"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func payloadFixtureResult() RenderResult {
	return RenderResult{
		Manifests: []render.Manifest{
			{
				SourceIndex:                  0,
				SourceName:                   "primary",
				Path:                         "manifests/demo",
				NamespaceBeforeNormalization: "demo-ns",
				Object: &unstructured.Unstructured{Object: map[string]any{
					"apiVersion": "apps/v1",
					"kind":       "Deployment",
					"metadata": map[string]any{
						"name":      "demo",
						"namespace": "demo-ns",
						"labels":    map[string]any{"app": "demo"},
					},
					"spec": map[string]any{
						"replicas": int64(3),
						"template": map[string]any{
							"spec": map[string]any{
								"containers": []any{
									map[string]any{"name": "main", "image": "registry.example.test/demo:1.2.3"},
								},
							},
						},
					},
				}},
			},
			{
				SourceIndex: 1,
				Path:        "manifests/extra",
				Object: &unstructured.Unstructured{Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
					"metadata":   map[string]any{"name": "extra"},
					"data":       map[string]any{"key": "value"},
				}},
			},
		},
		Diagnostics: []diagnostic.Diagnostic{
			{
				Code:       "plugin.avp-compat-substituted",
				Severity:   diagnostic.SeverityWarning,
				Category:   "plugin",
				Message:    "placeholders replaced",
				Provenance: diagnostic.Provenance{Path: "manifests/demo/values.yaml", Pointer: "/spec"},
			},
		},
		PluginExecutions: []PluginExecution{
			{
				AppNamespace: "argocd",
				AppName:      "demo",
				SourceIndex:  0,
				PluginName:   "example",
				Engine:       "native",
				Phase:        "render",
				Command:      "internal",
				Duration:     "10ms",
			},
		},
	}
}

func TestRenderResultPayloadRoundTrip(t *testing.T) {
	original := payloadFixtureResult()

	payload, err := marshalRenderResultPayload(original)
	if err != nil {
		t.Fatalf("marshalRenderResultPayload() error = %v", err)
	}
	restored, err := unmarshalRenderResultPayload(payload)
	if err != nil {
		t.Fatalf("unmarshalRenderResultPayload() error = %v", err)
	}
	if !reflect.DeepEqual(original, restored) {
		t.Fatalf("round trip mismatch:\noriginal: %#v\nrestored: %#v", original, restored)
	}
}

func TestRenderResultPayloadRoundTripEmptyResult(t *testing.T) {
	payload, err := marshalRenderResultPayload(RenderResult{})
	if err != nil {
		t.Fatalf("marshalRenderResultPayload() error = %v", err)
	}
	restored, err := unmarshalRenderResultPayload(payload)
	if err != nil {
		t.Fatalf("unmarshalRenderResultPayload() error = %v", err)
	}
	if !reflect.DeepEqual(RenderResult{}, restored) {
		t.Fatalf("empty round trip mismatch: %#v", restored)
	}
}

func TestRenderResultPayloadDeterministicBytes(t *testing.T) {
	// Two marshals of the same logical result must be byte-identical: the
	// foundation of the no-absolute-paths invariant test.
	first, err := marshalRenderResultPayload(payloadFixtureResult())
	if err != nil {
		t.Fatalf("marshalRenderResultPayload() error = %v", err)
	}
	second, err := marshalRenderResultPayload(payloadFixtureResult())
	if err != nil {
		t.Fatalf("marshalRenderResultPayload() error = %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("payload bytes are not deterministic")
	}
}

func TestUnmarshalRenderResultPayloadRejectsGarbage(t *testing.T) {
	if _, err := unmarshalRenderResultPayload([]byte("not-json")); err == nil {
		t.Fatalf("unmarshalRenderResultPayload() error = nil, want decode error")
	}
}
