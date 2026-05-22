package app

import (
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
	obj := &unstructured.Unstructured{}

	ApplyDestinationNamespace(application, obj)

	if obj.GetNamespace() != "workloads" {
		t.Fatalf("Namespace = %q, want workloads", obj.GetNamespace())
	}
}

func TestApplyDestinationNamespaceKeepsExistingNamespace(t *testing.T) {
	application := argoappv1.Application{
		Spec: argoappv1.ApplicationSpec{
			Destination: argoappv1.ApplicationDestination{Namespace: "workloads"},
		},
	}
	obj := &unstructured.Unstructured{}
	obj.SetNamespace("custom")

	ApplyDestinationNamespace(application, obj)

	if obj.GetNamespace() != "custom" {
		t.Fatalf("Namespace = %q, want custom", obj.GetNamespace())
	}
}

func TestApplyDestinationNamespaceSkipsEmptyDestinationNamespace(t *testing.T) {
	application := argoappv1.Application{}
	obj := &unstructured.Unstructured{}

	ApplyDestinationNamespace(application, obj)

	if obj.GetNamespace() != "" {
		t.Fatalf("Namespace = %q, want empty", obj.GetNamespace())
	}
}
