package app

import (
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// TestApplyTrackingMetadataDoesNotEchoFieldValues sets tracking metadata on
// objects whose annotations or labels cannot hold it. The upstream
// SetAppInstance error copies the field value through the apimachinery
// accessor error. drydock keeps the rest of the upstream text and drops the
// value.
func TestApplyTrackingMetadataDoesNotEchoFieldValues(t *testing.T) {
	const secretValue = "hunter2secret"
	application := argoappv1.Application{
		Name: "demo",
		Spec: argoappv1.ApplicationSpec{Destination: argoappv1.ApplicationDestination{Namespace: "default"}},
	}
	for _, tt := range []struct {
		name     string
		method   argoappv1.TrackingMethod
		metadata map[string]any
		want     string
	}{
		{
			name:     "annotations that are a string",
			method:   argoappv1.TrackingMethodAnnotation,
			metadata: map[string]any{"name": "a", "annotations": secretValue},
			want:     "failed to set app instance tracking info on manifest: failed to get annotations for /v1, Kind=ConfigMap /a: .metadata.annotations accessor error: a value is of the type string, expected map[string]interface{}",
		},
		{
			name:     "annotation value that is not a string",
			method:   argoappv1.TrackingMethodAnnotation,
			metadata: map[string]any{"name": "a", "annotations": map[string]any{"note": []any{secretValue}}},
			want:     `.metadata.annotations accessor error: contains non-string value in the map under key "note": a value is of the type []interface {}, expected string`,
		},
		{
			name:     "labels that are a list",
			method:   argoappv1.TrackingMethodLabel,
			metadata: map[string]any{"name": "a", "labels": []any{secretValue + " is of the type x"}},
			want:     ".metadata.labels accessor error: a value is of the type []interface {}, expected map[string]interface{}",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": tt.metadata}}
			err := applyTrackingMetadata(application, obj, TrackingOptions{Method: string(tt.method)})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want it to contain %q", err, tt.want)
			}
			if strings.Contains(err.Error(), secretValue) {
				t.Fatalf("error leaked manifest data: %q", err)
			}
		})
	}
}
