package app

import (
	"encoding/json"
	"maps"
	"reflect"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	argodiff "github.com/argoproj/argo-cd/v3/util/argo/diff"
	"github.com/argoproj/argo-cd/v3/util/argo/normalizers"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diff"
	"github.com/sholdee/drydock/internal/manifest"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

const parityContainersPointer = "/spec/template/spec/containers/"

// parityWorkloadBody has four containers so index-based removals show rule
// order and count. replicas differs between sides so a diff always remains.
func parityWorkloadBody(apiVersion, kind, annotation, replicas, cpu string) string {
	body := "apiVersion: " + apiVersion + "\nkind: " + kind + "\nmetadata:\n  name: demo\n  namespace: default\n"
	if annotation != "" {
		body += "  annotations:\n    argocd.argoproj.io/ignore-differences: '" + annotation + "'\n"
	}
	return body + `spec:
  replicas: ` + replicas + `
  template:
    spec:
      containers:
        - name: a
          image: example/a:v1
          resources:
            limits:
              cpu: ` + cpu + `
        - name: b
          image: example/b:v1
        - name: c
          image: example/c:v1
        - name: d
          image: example/d:v1
`
}

func parityObject(t *testing.T, body string) *unstructured.Unstructured {
	t.Helper()
	data, err := yaml.YAMLToJSON([]byte(body))
	if err != nil {
		t.Fatalf("YAMLToJSON() error = %v", err)
	}
	obj := &unstructured.Unstructured{}
	if err := obj.UnmarshalJSON(data); err != nil {
		t.Fatalf("UnmarshalJSON() error = %v", err)
	}
	return obj
}

func parityJSONObject(t *testing.T, value any) map[string]any {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	return out
}

func parityYAMLObject(t *testing.T, body string) map[string]any {
	t.Helper()
	data, err := yaml.YAMLToJSON([]byte(body))
	if err != nil {
		t.Fatalf("YAMLToJSON() error = %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	return out
}

// parityOverrides converts drydock resource.customizations into the
// ResourceOverride map Argo CD's normalizers read.
func parityOverrides(customizations map[string]config.ResourceCustomization) map[string]argoappv1.ResourceOverride {
	overrides := map[string]argoappv1.ResourceOverride{}
	for key, customization := range customizations {
		override := argoappv1.ResourceOverride{IgnoreDifferences: argoappv1.OverrideIgnoreDiff{
			JSONPointers:          customization.IgnoreDifferences.JSONPointers,
			JQPathExpressions:     customization.IgnoreDifferences.JQPathExpressions,
			ManagedFieldsManagers: customization.IgnoreDifferences.ManagedFieldsManagers,
		}}
		for _, field := range customization.KnownTypeFields {
			override.KnownTypeFields = append(override.KnownTypeFields, argoappv1.KnownTypeField{Field: field.Field, Type: field.Type})
		}
		overrides[key] = override
	}
	return overrides
}

// TestDiffNormalizationParityWithArgoCD runs the same Application rules,
// resource annotation, and resource.customizations through drydock's
// normalizationFor and diff.Run and through Argo CD v3.6.1's
// util/argo/diff.Normalize, then compares the normalized bodies on both sides.
func TestDiffNormalizationParityWithArgoCD(t *testing.T) {
	ignoreContainer := func(index string) config.ResourceCustomization {
		return config.ResourceCustomization{IgnoreDifferences: config.OverrideIgnoreDifferences{
			JSONPointers: []string{parityContainersPointer + index},
		}}
	}
	podSpecFields := config.ResourceCustomization{KnownTypeFields: []config.KnownTypeField{
		{Field: "spec.template.spec", Type: "core/v1/PodSpec"},
	}}
	tests := []struct {
		name           string
		apiVersion     string
		kind           string
		annotation     string
		ignores        []argoappv1.ResourceIgnoreDifferences
		customizations map[string]config.ResourceCustomization
		leftCPU        string
		rightCPU       string
	}{
		{
			// A1 and A3 through the real rule builder: Application rule,
			// then annotation, then customization, with a repeated pointer.
			name:       "application then annotation then customization",
			apiVersion: "apps/v1",
			kind:       "Deployment",
			annotation: `{"jsonPointers": ["` + parityContainersPointer + `0"]}`,
			ignores: []argoappv1.ResourceIgnoreDifferences{{
				Group:        "apps",
				Kind:         "Deployment",
				JSONPointers: []string{parityContainersPointer + "2", parityContainersPointer + "2"},
			}},
			customizations: map[string]config.ResourceCustomization{"apps/Deployment": ignoreContainer("1")},
		},
		{
			// A5: a key with no slash names a kind in the core group.
			name:           "kind-only key matches the core group",
			apiVersion:     "v1",
			kind:           "Service",
			customizations: map[string]config.ResourceCustomization{"Service": ignoreContainer("1")},
		},
		{
			name:           "kind-only key does not match another group",
			apiVersion:     "example.com/v1",
			kind:           "Service",
			customizations: map[string]config.ResourceCustomization{"Service": ignoreContainer("1")},
		},
		{
			name:           "key with two slashes matches nothing",
			apiVersion:     "example.com/v1",
			kind:           "Service",
			customizations: map[string]config.ResourceCustomization{"example.com/Service/x": ignoreContainer("1")},
		},
		{
			name:           "wildcard key matches every group",
			apiVersion:     "example.com/v1",
			kind:           "Service",
			customizations: map[string]config.ResourceCustomization{"*/*": ignoreContainer("1")},
		},
		{
			// knownTypeFields use exact group and kind lookup.
			name:           "known types exact key applies",
			apiVersion:     "apps/v1",
			kind:           "Deployment",
			customizations: map[string]config.ResourceCustomization{"apps/Deployment": podSpecFields},
			leftCPU:        "\"1\"",
		},
		{
			name:           "known types glob key does not apply",
			apiVersion:     "apps/v1",
			kind:           "Deployment",
			customizations: map[string]config.ResourceCustomization{"apps/*": podSpecFields},
			leftCPU:        "\"1\"",
		},
		{
			name:           "known types kind-only key does not apply to a named group",
			apiVersion:     "apps/v1",
			kind:           "Deployment",
			customizations: map[string]config.ResourceCustomization{"Deployment": podSpecFields},
			leftCPU:        "\"1\"",
		},
		{
			name:       "rollout pod spec is a known type by default",
			apiVersion: "argoproj.io/v1alpha1",
			kind:       "Rollout",
			leftCPU:    "\"1\"",
		},
		{
			// knownTypeFields for another kind leave the Rollout default
			// in place.
			name:           "rollout default stays with other known types",
			apiVersion:     "argoproj.io/v1alpha1",
			kind:           "Rollout",
			customizations: map[string]config.ResourceCustomization{"apps/Deployment": podSpecFields},
			leftCPU:        "\"1\"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			leftCPU, rightCPU := tt.leftCPU, tt.rightCPU
			if leftCPU == "" {
				leftCPU = "1000m"
			}
			if rightCPU == "" {
				rightCPU = "1000m"
			}
			leftBody := parityWorkloadBody(tt.apiVersion, tt.kind, tt.annotation, "1", leftCPU)
			rightBody := parityWorkloadBody(tt.apiVersion, tt.kind, tt.annotation, "2", rightCPU)
			settings := config.DefaultSettings()
			maps.Copy(settings.ResourceCustomizations, tt.customizations)
			application := argoappv1.Application{Spec: argoappv1.ApplicationSpec{IgnoreDifferences: tt.ignores}}

			ignores := tt.ignores
			if ignores == nil {
				ignores = []argoappv1.ResourceIgnoreDifferences{}
			}
			diffConfig, err := argodiff.NewDiffConfigBuilder().
				WithDiffSettings(ignores, parityOverrides(tt.customizations), false, normalizers.IgnoreNormalizerOpts{}).
				WithNoCache().
				Build()
			if err != nil {
				t.Fatalf("Build() error = %v", err)
			}
			upstream, err := argodiff.Normalize(
				[]*unstructured.Unstructured{parityObject(t, leftBody)},
				[]*unstructured.Unstructured{parityObject(t, rightBody)},
				diffConfig,
			)
			if err != nil {
				t.Fatalf("argodiff.Normalize() error = %v", err)
			}

			document := func(body string) diff.Document {
				obj := parityObject(t, body)
				id := manifest.IdentityOf(obj)
				normalization := normalizationFor(application, id, settings)
				normalization.AnnotationJSONPointers = annotationIgnoreJSONPointers(obj)
				return diff.Document{
					Resource:      diff.Resource{Group: id.Group, Kind: id.Kind, Namespace: id.Namespace, Name: id.Name},
					Body:          body,
					Normalization: normalization,
				}
			}
			results, err := diff.Run([]diff.Document{document(leftBody)}, []diff.Document{document(rightBody)}, diff.Options{Unified: 3})
			if err != nil {
				t.Fatalf("diff.Run() error = %v", err)
			}
			if len(results) != 1 {
				t.Fatalf("len(results) = %d, want the replicas diff", len(results))
			}
			if got, want := parityYAMLObject(t, results[0].Before), parityJSONObject(t, upstream.Lives[0].Object); !reflect.DeepEqual(got, want) {
				t.Fatalf("left side mismatch\n got: %#v\nwant: %#v", got, want)
			}
			if got, want := parityYAMLObject(t, results[0].After), parityJSONObject(t, upstream.Targets[0].Object); !reflect.DeepEqual(got, want) {
				t.Fatalf("right side mismatch\n got: %#v\nwant: %#v", got, want)
			}
		})
	}
}
