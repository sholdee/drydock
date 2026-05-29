package app

import (
	"context"
	"strings"
	"testing"

	"github.com/argoproj/argo-cd/v3/common"
	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/render"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestRenderApplicationAppliesDefaultAnnotationTracking(t *testing.T) {
	application := trackingTestApplication("argocd", "demo")
	renderers := StaticRenderers{"manifests": []render.Manifest{{Object: cm("tracked", "value")}}}

	result, err := RenderApplication(context.Background(), application, renderers)
	if err != nil {
		t.Fatalf("RenderApplication() error = %v", err)
	}
	object := result.Manifests[0].Object
	assertAnnotation(t, object, common.AnnotationKeyAppInstance, "demo:/ConfigMap:workloads/tracked")
	if _, ok := object.GetLabels()[common.LabelKeyAppInstance]; ok {
		t.Fatalf("unexpected default tracking label: %#v", object.GetLabels())
	}
}

func TestRenderApplicationAppliesAnnotationLabelTrackingAndInstallationID(t *testing.T) {
	application := trackingTestApplication("gitops", "demo")
	renderers := StaticRenderers{"manifests": []render.Manifest{{Object: cm("tracked", "value")}}}

	result, err := RenderApplicationWithOptions(context.Background(), application, renderers, ApplicationRenderOptions{
		TrackingOptions: TrackingOptions{
			Method:           string(argoappv1.TrackingMethodAnnotationAndLabel),
			InstanceLabelKey: "app.kubernetes.io/semantic-instance",
			InstallationID:   "cluster-one",
		},
	})
	if err != nil {
		t.Fatalf("RenderApplicationWithOptions() error = %v", err)
	}
	object := result.Manifests[0].Object
	assertAnnotation(t, object, common.AnnotationKeyAppInstance, "gitops_demo:/ConfigMap:workloads/tracked")
	assertAnnotation(t, object, common.AnnotationInstallationID, "cluster-one")
	assertLabel(t, object, "app.kubernetes.io/semantic-instance", "gitops_demo")
}

func TestRenderApplicationAppliesLabelOnlyTracking(t *testing.T) {
	application := trackingTestApplication("argocd", "demo")
	renderers := StaticRenderers{"manifests": []render.Manifest{{Object: cm("tracked", "value")}}}

	result, err := RenderApplicationWithOptions(context.Background(), application, renderers, ApplicationRenderOptions{
		TrackingOptions: TrackingOptions{Method: string(argoappv1.TrackingMethodLabel)},
	})
	if err != nil {
		t.Fatalf("RenderApplicationWithOptions() error = %v", err)
	}
	object := result.Manifests[0].Object
	assertLabel(t, object, common.LabelKeyAppInstance, "demo")
	if _, ok := object.GetAnnotations()[common.AnnotationKeyAppInstance]; ok {
		t.Fatalf("unexpected label-only tracking annotation: %#v", object.GetAnnotations())
	}
}

func TestRenderApplicationSkipsCRDTracking(t *testing.T) {
	application := trackingTestApplication("argocd", "demo")
	renderers := StaticRenderers{"manifests": []render.Manifest{{Object: crd("widgets.example.com")}}}

	result, err := RenderApplication(context.Background(), application, renderers)
	if err != nil {
		t.Fatalf("RenderApplication() error = %v", err)
	}
	object := result.Manifests[0].Object
	if len(object.GetAnnotations()) != 0 {
		t.Fatalf("annotations = %#v, want none", object.GetAnnotations())
	}
	if len(object.GetLabels()) != 0 {
		t.Fatalf("labels = %#v, want none", object.GetLabels())
	}
}

func TestRenderApplicationTruncatesAnnotationLabelTrackingLabel(t *testing.T) {
	application := trackingTestApplication("argocd", "demo-with-an-extremely-long-application-name-that-exceeds-label-limits")
	renderers := StaticRenderers{"manifests": []render.Manifest{{Object: cm("tracked", "value")}}}

	result, err := RenderApplicationWithOptions(context.Background(), application, renderers, ApplicationRenderOptions{
		TrackingOptions: TrackingOptions{Method: string(argoappv1.TrackingMethodAnnotationAndLabel)},
	})
	if err != nil {
		t.Fatalf("RenderApplicationWithOptions() error = %v", err)
	}
	object := result.Manifests[0].Object
	label := object.GetLabels()[common.LabelKeyAppInstance]
	if len(label) > 63 {
		t.Fatalf("label length = %d, want <= 63: %q", len(label), label)
	}
	if !strings.HasPrefix(object.GetAnnotations()[common.AnnotationKeyAppInstance], application.Name+":/ConfigMap:workloads/tracked") {
		t.Fatalf("tracking annotation = %#v", object.GetAnnotations()[common.AnnotationKeyAppInstance])
	}
}

func TestTrackingOptionsFromSettings(t *testing.T) {
	settings := config.DefaultSettings()
	settings.TrackingMethod = config.Value[string]{Value: string(argoappv1.TrackingMethodAnnotationAndLabel)}
	settings.InstanceLabelKey = config.Value[string]{Value: "custom.io/instance"}
	settings.InstallationID = config.Value[string]{Value: "cluster-one"}

	opts := trackingOptionsFromSettings(settings)
	if opts.Method != string(argoappv1.TrackingMethodAnnotationAndLabel) {
		t.Fatalf("Method = %q", opts.Method)
	}
	if opts.InstanceLabelKey != "custom.io/instance" {
		t.Fatalf("InstanceLabelKey = %q", opts.InstanceLabelKey)
	}
	if opts.InstallationID != "cluster-one" {
		t.Fatalf("InstallationID = %q", opts.InstallationID)
	}
	if opts.ControllerNamespace != defaultArgoCDControllerNamespace {
		t.Fatalf("ControllerNamespace = %q", opts.ControllerNamespace)
	}
}

func TestRenderSettingsSignatureIncludesTrackingInputs(t *testing.T) {
	base := config.DefaultSettings()
	baseSig, err := renderSettingsSignature(base)
	if err != nil {
		t.Fatalf("renderSettingsSignature(base) error = %v", err)
	}

	for name, mutate := range map[string]func(*config.ArgoSettings){
		"tracking method": func(settings *config.ArgoSettings) {
			settings.TrackingMethod = config.Value[string]{Value: string(argoappv1.TrackingMethodLabel)}
		},
		"instance label key": func(settings *config.ArgoSettings) {
			settings.InstanceLabelKey = config.Value[string]{Value: "custom.io/instance"}
		},
		"installation id": func(settings *config.ArgoSettings) {
			settings.InstallationID = config.Value[string]{Value: "cluster-one"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			next := base
			mutate(&next)
			nextSig, err := renderSettingsSignature(next)
			if err != nil {
				t.Fatalf("renderSettingsSignature() error = %v", err)
			}
			if nextSig == baseSig {
				t.Fatalf("signature did not change for %s", name)
			}
		})
	}
}

func TestRenderSettingsSignatureIgnoresCommandParameters(t *testing.T) {
	base := config.DefaultSettings()
	baseSig, err := renderSettingsSignature(base)
	if err != nil {
		t.Fatalf("renderSettingsSignature(base) error = %v", err)
	}

	next := base
	next.CommandParameters = []config.CommandParameterSetting{{
		Key:            "reposerver.include.hidden.directories",
		Value:          "true",
		Classification: config.CommandParameterRuntimeOnly,
		Provenance:     config.Provenance{Path: "argocd-cmd-params-cm.yaml", Pointer: "data.reposerver.include.hidden.directories"},
	}}
	nextSig, err := renderSettingsSignature(next)
	if err != nil {
		t.Fatalf("renderSettingsSignature(next) error = %v", err)
	}
	if nextSig != baseSig {
		t.Fatalf("render settings signature changed for cmd-params metadata")
	}
}

func trackingTestApplication(namespace, name string) argoappv1.Application {
	return argoappv1.Application{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: argoappv1.ApplicationSpec{
			Source:      &argoappv1.ApplicationSource{RepoURL: "https://repo", Path: "manifests"},
			Destination: argoappv1.ApplicationDestination{Namespace: "workloads"},
		},
	}
}

func crd(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apiextensions.k8s.io/v1",
		"kind":       "CustomResourceDefinition",
		"metadata": map[string]any{
			"name": name,
		},
	}}
}

func assertAnnotation(t *testing.T, object *unstructured.Unstructured, key, want string) {
	t.Helper()
	if got := object.GetAnnotations()[key]; got != want {
		t.Fatalf("annotation %s = %q, want %q", key, got, want)
	}
}

func assertLabel(t *testing.T, object *unstructured.Unstructured, key, want string) {
	t.Helper()
	if got := object.GetLabels()[key]; got != want {
		t.Fatalf("label %s = %q, want %q", key, got, want)
	}
}
