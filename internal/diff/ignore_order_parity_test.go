package diff

import (
	"reflect"
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	argodiff "github.com/argoproj/argo-cd/v3/util/argo/diff"
	"github.com/argoproj/argo-cd/v3/util/argo/normalizers"
	"go.yaml.in/yaml/v3"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// parityDeploymentBody has four containers so index-based removals show the
// order in which ignore rules run.
func parityDeploymentBody(kind, apiVersion, annotation, replicas string) string {
	body := "apiVersion: " + apiVersion + "\nkind: " + kind + "\nmetadata:\n  name: demo\n  namespace: default\n"
	if annotation != "" {
		body += "  annotations:\n    argocd.argoproj.io/ignore-differences: |\n      " + annotation + "\n"
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
              cpu: 1000m
        - name: b
          image: example/b:v1
        - name: c
          image: example/c:v1
        - name: d
          image: example/d:v1
`
}

// upstreamParityNormalize runs Argo CD v3.6.1's util/argo/diff.Normalize, the
// function the application controller uses, on the baseline as live and the
// current side as target. A nil body is a missing resource.
func upstreamParityNormalize(t *testing.T, leftBody, rightBody string, ignores []argoappv1.ResourceIgnoreDifferences, overrides map[string]argoappv1.ResourceOverride) (map[string]any, map[string]any) {
	t.Helper()
	toObject := func(body string) *unstructured.Unstructured {
		if body == "" {
			return nil
		}
		var object map[string]any
		if err := yaml.Unmarshal([]byte(body), &object); err != nil {
			t.Fatalf("yaml.Unmarshal() error = %v", err)
		}
		return &unstructured.Unstructured{Object: jsonRoundTrip(t, object)}
	}
	if overrides == nil {
		overrides = map[string]argoappv1.ResourceOverride{}
	}
	config, err := argodiff.NewDiffConfigBuilder().
		WithDiffSettings(ignores, overrides, false, normalizers.IgnoreNormalizerOpts{}).
		WithNoCache().
		Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	result, err := argodiff.Normalize([]*unstructured.Unstructured{toObject(leftBody)}, []*unstructured.Unstructured{toObject(rightBody)}, config)
	if err != nil {
		t.Fatalf("argodiff.Normalize() error = %v", err)
	}
	objectOf := func(obj *unstructured.Unstructured) map[string]any {
		if obj == nil {
			return nil
		}
		return jsonRoundTrip(t, obj.Object)
	}
	return objectOf(result.Lives[0]), objectOf(result.Targets[0])
}

// drydockParityNormalize runs drydock's normalization on the same pair. Both
// sides carry the same settings, so normalization is shared like in Run.
func drydockParityNormalize(t *testing.T, leftBody, rightBody string, resource Resource, normalization Normalization, rightAnnotation, leftAnnotation []string) (map[string]any, map[string]any) {
	t.Helper()
	left := Document{Resource: resource, Body: leftBody, Normalization: cloneNormalization(normalization)}
	left.Normalization.AnnotationJSONPointers = leftAnnotation
	right := Document{Resource: resource, Body: rightBody, Normalization: cloneNormalization(normalization)}
	right.Normalization.AnnotationJSONPointers = rightAnnotation
	hasLeft, hasRight := leftBody != "", rightBody != ""
	left, right = documentsWithSharedNormalization(left, right, hasLeft, hasRight)
	normalize := func(doc Document, present bool) map[string]any {
		if !present {
			return nil
		}
		object, err := normalizeDiffObject(doc.Body, Options{}, doc.Normalization, doc.Resource)
		if err != nil {
			t.Fatalf("normalizeDiffObject() error = %v", err)
		}
		return jsonRoundTrip(t, object)
	}
	return normalize(left, hasLeft), normalize(right, hasRight)
}

const parityContainers = "/spec/template/spec/containers/"

// TestIgnoreRuleOrderParityWithArgoCD proves drydock runs ignore rules in the
// order Argo CD v3.6.1 does, keeps every copy of a repeated pointer, reads the
// annotation only from the current (target) side, and runs the known-types
// normalizer after the ignore rules. Each case uses Argo CD's own Normalize as
// the oracle, mostly with index-based array removals whose result depends on
// order and count.
func TestIgnoreRuleOrderParityWithArgoCD(t *testing.T) {
	deployment := Resource{Group: "apps", Kind: "Deployment", Namespace: "default", Name: "demo"}
	rollout := Resource{Group: "argoproj.io", Kind: "Rollout", Namespace: "default", Name: "demo"}
	appRule := func(pointers []string, expressions []string) argoappv1.ResourceIgnoreDifferences {
		return argoappv1.ResourceIgnoreDifferences{Group: "apps", Kind: "Deployment", JSONPointers: pointers, JQPathExpressions: expressions}
	}
	tests := []struct {
		name          string
		resource      Resource
		leftBody      string
		rightBody     string
		ignores       []argoappv1.ResourceIgnoreDifferences
		overrides     map[string]argoappv1.ResourceOverride
		normalization Normalization
		// Annotation pointers drydock parses from each side. The upstream
		// oracle reads them from the bodies.
		leftAnnotation  []string
		rightAnnotation []string
	}{
		{
			// A1: the annotation rule runs before resource.customizations.
			name:      "annotation pointers before customization pointers",
			resource:  deployment,
			leftBody:  parityDeploymentBody("Deployment", "apps/v1", `{"jsonPointers": ["`+parityContainers+`0"]}`, "1"),
			rightBody: parityDeploymentBody("Deployment", "apps/v1", `{"jsonPointers": ["`+parityContainers+`0"]}`, "2"),
			overrides: map[string]argoappv1.ResourceOverride{"apps/Deployment": {IgnoreDifferences: argoappv1.OverrideIgnoreDiff{
				JSONPointers: []string{parityContainers + "1"},
			}}},
			normalization:   Normalization{CustomizationRules: []IgnoreRule{{JSONPointers: []string{parityContainers + "1"}}}},
			leftAnnotation:  []string{parityContainers + "0"},
			rightAnnotation: []string{parityContainers + "0"},
		},
		{
			// A1: Application rules run before the annotation rule.
			name:            "application pointers before annotation pointers",
			resource:        deployment,
			leftBody:        parityDeploymentBody("Deployment", "apps/v1", `{"jsonPointers": ["`+parityContainers+`0"]}`, "1"),
			rightBody:       parityDeploymentBody("Deployment", "apps/v1", `{"jsonPointers": ["`+parityContainers+`0"]}`, "2"),
			ignores:         []argoappv1.ResourceIgnoreDifferences{appRule([]string{parityContainers + "1"}, nil)},
			normalization:   Normalization{ApplicationRules: []IgnoreRule{{JSONPointers: []string{parityContainers + "1"}}}},
			leftAnnotation:  []string{parityContainers + "0"},
			rightAnnotation: []string{parityContainers + "0"},
		},
		{
			// A2: a rule's jq expressions run before the next rule's pointers.
			name:      "rules apply pointers then jq one rule at a time",
			resource:  deployment,
			leftBody:  parityDeploymentBody("Deployment", "apps/v1", "", "1"),
			rightBody: parityDeploymentBody("Deployment", "apps/v1", "", "2"),
			ignores: []argoappv1.ResourceIgnoreDifferences{
				appRule(nil, []string{".spec.template.spec.containers[0]"}),
				appRule([]string{parityContainers + "1"}, nil),
			},
			normalization: Normalization{ApplicationRules: []IgnoreRule{
				{JQPathExpressions: []string{".spec.template.spec.containers[0]"}},
				{JSONPointers: []string{parityContainers + "1"}},
			}},
		},
		{
			// A2: Application jq runs before customization pointers.
			name:      "application jq before customization pointers",
			resource:  deployment,
			leftBody:  parityDeploymentBody("Deployment", "apps/v1", "", "1"),
			rightBody: parityDeploymentBody("Deployment", "apps/v1", "", "2"),
			ignores:   []argoappv1.ResourceIgnoreDifferences{appRule(nil, []string{".spec.template.spec.containers[0]"})},
			overrides: map[string]argoappv1.ResourceOverride{"apps/Deployment": {IgnoreDifferences: argoappv1.OverrideIgnoreDiff{
				JSONPointers: []string{parityContainers + "1"},
			}}},
			normalization: Normalization{
				ApplicationRules:   []IgnoreRule{{JQPathExpressions: []string{".spec.template.spec.containers[0]"}}},
				CustomizationRules: []IgnoreRule{{JSONPointers: []string{parityContainers + "1"}}},
			},
		},
		{
			// Application and customization pointers use JSON-patch path
			// rules, as annotation pointers do: the first segment is not
			// checked, a negative index counts from the end, and a pointer
			// that fails, such as a non-numeric array index or the root
			// pointer, is skipped while the next one applies.
			name:      "application and customization pointers use JSON-patch path rules",
			resource:  deployment,
			leftBody:  parityDeploymentBody("Deployment", "apps/v1", "", "1"),
			rightBody: parityDeploymentBody("Deployment", "apps/v1", "", "2"),
			ignores: []argoappv1.ResourceIgnoreDifferences{
				appRule([]string{"spec/replicas", "", parityContainers + "a/image", "#" + parityContainers + "-1"}, nil),
			},
			overrides: map[string]argoappv1.ResourceOverride{"apps/Deployment": {IgnoreDifferences: argoappv1.OverrideIgnoreDiff{
				JSONPointers: []string{"/spec/~2", "x/spec/replicas"},
			}}},
			normalization: Normalization{
				ApplicationRules:   []IgnoreRule{{JSONPointers: []string{"spec/replicas", "", parityContainers + "a/image", "#" + parityContainers + "-1"}}},
				CustomizationRules: []IgnoreRule{{JSONPointers: []string{"/spec/~2", "x/spec/replicas"}}},
			},
		},
		{
			// A3: a repeated pointer removes one element per copy.
			name:      "repeated pointers in one rule and across rules",
			resource:  deployment,
			leftBody:  parityDeploymentBody("Deployment", "apps/v1", "", "1"),
			rightBody: parityDeploymentBody("Deployment", "apps/v1", "", "2"),
			ignores: []argoappv1.ResourceIgnoreDifferences{
				appRule([]string{parityContainers + "0", parityContainers + "0"}, nil),
			},
			overrides: map[string]argoappv1.ResourceOverride{"apps/Deployment": {IgnoreDifferences: argoappv1.OverrideIgnoreDiff{
				JSONPointers: []string{parityContainers + "0"},
			}}},
			normalization: Normalization{
				ApplicationRules:   []IgnoreRule{{JSONPointers: []string{parityContainers + "0", parityContainers + "0"}}},
				CustomizationRules: []IgnoreRule{{JSONPointers: []string{parityContainers + "0"}}},
			},
		},
		{
			// A3: the same rule twice keeps both copies.
			name:      "repeated application rules",
			resource:  deployment,
			leftBody:  parityDeploymentBody("Deployment", "apps/v1", "", "1"),
			rightBody: parityDeploymentBody("Deployment", "apps/v1", "", "2"),
			ignores: []argoappv1.ResourceIgnoreDifferences{
				appRule([]string{parityContainers + "1"}, []string{".spec.template.spec.containers[0]"}),
				appRule([]string{parityContainers + "1"}, []string{".spec.template.spec.containers[0]"}),
			},
			normalization: Normalization{ApplicationRules: []IgnoreRule{
				{JSONPointers: []string{parityContainers + "1"}, JQPathExpressions: []string{".spec.template.spec.containers[0]"}},
				{JSONPointers: []string{parityContainers + "1"}, JQPathExpressions: []string{".spec.template.spec.containers[0]"}},
			}},
		},
		{
			// A4: a resource that exists only on the baseline side is not
			// normalized by its own annotation.
			name:           "removed resource ignores its own annotation",
			resource:       deployment,
			leftBody:       parityDeploymentBody("Deployment", "apps/v1", `{"jsonPointers": ["/spec/replicas"]}`, "1"),
			leftAnnotation: []string{"/spec/replicas"},
		},
		{
			// A4: an added resource is normalized by its own annotation.
			name:            "added resource uses its own annotation",
			resource:        deployment,
			rightBody:       parityDeploymentBody("Deployment", "apps/v1", `{"jsonPointers": ["/spec/replicas"]}`, "1"),
			rightAnnotation: []string{"/spec/replicas"},
		},
		{
			// Known types run after the ignore rules: the jq select sees the
			// cpu value before remarshaling turns 1000m into 1.
			name:      "known types run after jq select",
			resource:  deployment,
			leftBody:  parityDeploymentBody("Deployment", "apps/v1", "", "1"),
			rightBody: parityDeploymentBody("Deployment", "apps/v1", "", "2"),
			ignores: []argoappv1.ResourceIgnoreDifferences{appRule(nil, []string{
				`.spec.template.spec.containers[] | select(.resources.limits.cpu == "1000m") | .image`,
			})},
			overrides: map[string]argoappv1.ResourceOverride{"apps/Deployment": {KnownTypeFields: []argoappv1.KnownTypeField{
				{Field: "spec.template.spec", Type: "core/v1/PodSpec"},
			}}},
			normalization: Normalization{
				ApplicationRules: []IgnoreRule{{JQPathExpressions: []string{
					`.spec.template.spec.containers[] | select(.resources.limits.cpu == "1000m") | .image`,
				}}},
				KnownTypeFields: []KnownTypeField{{Field: "spec.template.spec", Type: "core/v1/PodSpec"}},
			},
		},
		{
			// An ignore rule removes a value that would fail to remarshal.
			name:      "ignore rules remove an invalid known-type value first",
			resource:  deployment,
			leftBody:  strings.Replace(parityDeploymentBody("Deployment", "apps/v1", "", "1"), "cpu: 1000m", "cpu: not-a-quantity", 1),
			rightBody: strings.Replace(parityDeploymentBody("Deployment", "apps/v1", "", "2"), "cpu: 1000m", "cpu: not-a-quantity", 1),
			ignores:   []argoappv1.ResourceIgnoreDifferences{appRule([]string{parityContainers + "0/resources"}, nil)},
			overrides: map[string]argoappv1.ResourceOverride{"apps/Deployment": {KnownTypeFields: []argoappv1.KnownTypeField{
				{Field: "spec.template.spec", Type: "core/v1/PodSpec"},
			}}},
			normalization: Normalization{
				ApplicationRules: []IgnoreRule{{JSONPointers: []string{parityContainers + "0/resources"}}},
				KnownTypeFields:  []KnownTypeField{{Field: "spec.template.spec", Type: "core/v1/PodSpec"}},
			},
		},
		{
			// Argo CD always normalizes a Rollout pod spec as a PodSpec.
			name:      "rollout pod spec is a known type by default",
			resource:  rollout,
			leftBody:  strings.Replace(parityDeploymentBody("Rollout", "argoproj.io/v1alpha1", "", "1"), "cpu: 1000m", "cpu: \"1\"", 1),
			rightBody: parityDeploymentBody("Rollout", "argoproj.io/v1alpha1", "", "2"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wantLeft, wantRight := upstreamParityNormalize(t, tt.leftBody, tt.rightBody, tt.ignores, tt.overrides)
			gotLeft, gotRight := drydockParityNormalize(t, tt.leftBody, tt.rightBody, tt.resource, tt.normalization, tt.rightAnnotation, tt.leftAnnotation)
			if !reflect.DeepEqual(gotLeft, wantLeft) {
				t.Fatalf("left side mismatch\n got: %#v\nwant: %#v", gotLeft, wantLeft)
			}
			if !reflect.DeepEqual(gotRight, wantRight) {
				t.Fatalf("right side mismatch\n got: %#v\nwant: %#v", gotRight, wantRight)
			}
		})
	}
}
