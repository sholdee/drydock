package app

import (
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestPlanUsesSourcesOverSource(t *testing.T) {
	application := argoappv1.Application{
		Spec: argoappv1.ApplicationSpec{
			Source: &argoappv1.ApplicationSource{RepoURL: "https://ignored", Path: "ignored"},
			Sources: argoappv1.ApplicationSources{
				{RepoURL: "https://one", Path: "one"},
				{RepoURL: "https://two", Path: "two"},
			},
		},
	}

	plan, err := Plan(application)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if len(plan.Sources) != 2 {
		t.Fatalf("len(Sources) = %d, want 2", len(plan.Sources))
	}
	if plan.Sources[0].Source.RepoURL != "https://one" {
		t.Fatalf("first source = %#v", plan.Sources[0])
	}
}

func TestPlanValidatesDuplicateRefs(t *testing.T) {
	application := argoappv1.Application{
		Spec: argoappv1.ApplicationSpec{
			Sources: argoappv1.ApplicationSources{
				{RepoURL: "https://values-a", Ref: "values"},
				{RepoURL: "https://values-b", Ref: "values"},
			},
		},
	}

	_, err := Plan(application)
	if err == nil {
		t.Fatalf("expected duplicate ref error")
	}
}

func TestPlanRejectsInvalidRef(t *testing.T) {
	application := argoappv1.Application{
		Spec: argoappv1.ApplicationSpec{
			Sources: argoappv1.ApplicationSources{
				{RepoURL: "https://values", Ref: "bad/ref"},
			},
		},
	}

	_, err := Plan(application)
	if err == nil {
		t.Fatalf("expected invalid ref error")
	}
}

func TestPlanRejectsRefChart(t *testing.T) {
	application := argoappv1.Application{
		Spec: argoappv1.ApplicationSpec{
			Sources: argoappv1.ApplicationSources{
				{RepoURL: "ghcr.io/example/charts", Chart: "app", Ref: "values"},
			},
		},
	}

	_, err := Plan(application)
	if err == nil {
		t.Fatalf("expected ref chart error")
	}
}

// An oci:// entry with both ref and chart plans: upstream dispatches it as
// an OCI artifact source and renders it (repository.go:358-360 at v3.5.4),
// rejecting the pair only when another source consumes the ref. The entry is
// not ref-only (chart is set, so it renders), and its ref is registered.
func TestPlanAllowsRefChartOnOCISource(t *testing.T) {
	application := argoappv1.Application{
		Spec: argoappv1.ApplicationSpec{
			Sources: argoappv1.ApplicationSources{
				{RepoURL: "oci://ghcr.io/example/charts", Chart: "app", Ref: "values"},
			},
		},
	}

	result, err := Plan(application)
	if err != nil {
		t.Fatalf("Plan() error = %v, want oci:// ref+chart accepted", err)
	}
	if len(result.Sources) != 1 {
		t.Fatalf("Sources = %#v, want one", result.Sources)
	}
	if result.Sources[0].RefOnly {
		t.Fatalf("RefOnly = true, want false: the entry sets chart and renders")
	}
	if got := result.Sources[0].RefKey; got != "$values" {
		t.Fatalf("RefKey = %q, want $values", got)
	}
}

func TestPlanRejectsMultipleExplicitSourceTypes(t *testing.T) {
	application := argoappv1.Application{
		Spec: argoappv1.ApplicationSpec{
			Sources: argoappv1.ApplicationSources{
				{
					RepoURL:   "https://repo",
					Path:      "apps/demo",
					Helm:      &argoappv1.ApplicationSourceHelm{},
					Directory: &argoappv1.ApplicationSourceDirectory{},
				},
			},
		},
	}

	_, err := Plan(application)
	if err == nil {
		t.Fatalf("expected explicit source type conflict")
	}
	if !strings.Contains(err.Error(), "multiple application sources defined") {
		t.Fatalf("Plan() error = %v, want Argo CD explicit type conflict", err)
	}
}

func TestPlanMarksRefOnlySources(t *testing.T) {
	application := argoappv1.Application{
		Spec: argoappv1.ApplicationSpec{
			Sources: argoappv1.ApplicationSources{
				{RepoURL: "https://values", Ref: "values"},
				{RepoURL: "https://manifests", Path: "apps/api", Ref: "manifests"},
			},
		},
	}

	plan, err := Plan(application)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if !plan.Sources[0].RefOnly {
		t.Fatalf("Sources[0].RefOnly = false, want true")
	}
	if plan.Sources[0].RefKey != "$values" {
		t.Fatalf("Sources[0].RefKey = %q, want $values", plan.Sources[0].RefKey)
	}
	if plan.Refs["$values"].Index != 0 {
		t.Fatalf("Refs[$values] = %#v, want source index 0", plan.Refs["$values"])
	}
	if plan.Sources[1].RefOnly {
		t.Fatalf("Sources[1].RefOnly = true, want false")
	}
	if plan.Sources[1].RefKey != "$manifests" {
		t.Fatalf("Sources[1].RefKey = %q, want $manifests", plan.Sources[1].RefKey)
	}
}

func TestApplyDestinationNamespaceSetsMissingNamespace(t *testing.T) {
	application := argoappv1.Application{
		Spec: argoappv1.ApplicationSpec{
			Destination: argoappv1.ApplicationDestination{Namespace: "workloads"},
		},
	}
	tests := []struct {
		name       string
		apiVersion string
		kind       string
	}{
		{name: "config map", apiVersion: "v1", kind: "ConfigMap"},
		{name: "service", apiVersion: "v1", kind: "Service"},
		{name: "deployment", apiVersion: "apps/v1", kind: "Deployment"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": tt.apiVersion,
				"kind":       tt.kind,
			}}

			ApplyDestinationNamespace(application, obj)

			if obj.GetNamespace() != "workloads" {
				t.Fatalf("Namespace = %q, want workloads", obj.GetNamespace())
			}
		})
	}
}

func TestApplyDestinationNamespaceKeepsExistingNamespace(t *testing.T) {
	application := argoappv1.Application{
		Spec: argoappv1.ApplicationSpec{
			Destination: argoappv1.ApplicationDestination{Namespace: "workloads"},
		},
	}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Service",
	}}
	obj.SetNamespace("custom")

	ApplyDestinationNamespace(application, obj)

	if obj.GetNamespace() != "custom" {
		t.Fatalf("Namespace = %q, want custom", obj.GetNamespace())
	}
}

func TestApplyDestinationNamespaceSkipsEmptyDestinationNamespace(t *testing.T) {
	application := argoappv1.Application{}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
	}}

	ApplyDestinationNamespace(application, obj)

	if obj.GetNamespace() != "" {
		t.Fatalf("Namespace = %q, want empty", obj.GetNamespace())
	}
}

func TestApplyDestinationNamespaceSkipsKnownClusterScopedResources(t *testing.T) {
	application := argoappv1.Application{
		Spec: argoappv1.ApplicationSpec{
			Destination: argoappv1.ApplicationDestination{Namespace: "workloads"},
		},
	}
	tests := []struct {
		name       string
		apiVersion string
		kind       string
	}{
		{name: "namespace", apiVersion: "v1", kind: "Namespace"},
		{name: "node", apiVersion: "v1", kind: "Node"},
		{name: "persistent volume", apiVersion: "v1", kind: "PersistentVolume"},
		{name: "mutating webhook configuration", apiVersion: "admissionregistration.k8s.io/v1", kind: "MutatingWebhookConfiguration"},
		{name: "validating webhook configuration", apiVersion: "admissionregistration.k8s.io/v1", kind: "ValidatingWebhookConfiguration"},
		{name: "cluster role", apiVersion: "rbac.authorization.k8s.io/v1", kind: "ClusterRole"},
		{name: "cluster role binding", apiVersion: "rbac.authorization.k8s.io/v1", kind: "ClusterRoleBinding"},
		{name: "storage class", apiVersion: "storage.k8s.io/v1", kind: "StorageClass"},
		{name: "priority class", apiVersion: "scheduling.k8s.io/v1", kind: "PriorityClass"},
		{name: "api service", apiVersion: "apiregistration.k8s.io/v1", kind: "APIService"},
		{name: "custom resource definition", apiVersion: "apiextensions.k8s.io/v1", kind: "CustomResourceDefinition"},
		{name: "ingress class", apiVersion: "networking.k8s.io/v1", kind: "IngressClass"},
		{name: "certificate signing request", apiVersion: "certificates.k8s.io/v1", kind: "CertificateSigningRequest"},
		{name: "runtime class", apiVersion: "node.k8s.io/v1", kind: "RuntimeClass"},
		{name: "csi driver", apiVersion: "storage.k8s.io/v1", kind: "CSIDriver"},
		{name: "csi node", apiVersion: "storage.k8s.io/v1", kind: "CSINode"},
		{name: "volume attachment", apiVersion: "storage.k8s.io/v1", kind: "VolumeAttachment"},
		{name: "flow schema", apiVersion: "flowcontrol.apiserver.k8s.io/v1", kind: "FlowSchema"},
		{name: "priority level configuration", apiVersion: "flowcontrol.apiserver.k8s.io/v1", kind: "PriorityLevelConfiguration"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": tt.apiVersion,
				"kind":       tt.kind,
			}}

			ApplyDestinationNamespace(application, obj)

			if obj.GetNamespace() != "" {
				t.Fatalf("Namespace = %q, want empty", obj.GetNamespace())
			}
		})
	}
}
