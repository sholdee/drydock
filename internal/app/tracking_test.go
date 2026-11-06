package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/argoproj/argo-cd/v3/common"
	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/render"
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

func TestRenderSettingsSignatureIgnoresProvenance(t *testing.T) {
	base := config.DefaultSettings()
	base.KustomizeBuildOptions = []config.Value[string]{{Value: "--enable-helm", Provenance: config.Provenance{Path: "/tmp/left/argocd-cm.yaml"}}}
	base.HelmValuesFileSchemes = []config.Value[string]{{Value: "https", Provenance: config.Provenance{Path: "/tmp/left/argocd-cm.yaml"}}}
	base.TrackingMethod = config.Value[string]{Value: string(argoappv1.TrackingMethodAnnotation), Provenance: config.Provenance{Path: "/tmp/left/argocd-cm.yaml"}}
	base.InstanceLabelKey = config.Value[string]{Value: "app.kubernetes.io/instance", Provenance: config.Provenance{Path: "/tmp/left/argocd-cm.yaml"}}
	base.InstallationID = config.Value[string]{Value: "cluster-one", Provenance: config.Provenance{Path: "/tmp/left/argocd-cm.yaml"}}
	base.HelmRepositories = map[string]config.RepositorySettings{
		"https://charts.example.test": {
			Name:       "example",
			URL:        "https://charts.example.test",
			Provenance: config.Provenance{Path: "/tmp/left/repositories.yaml"},
		},
	}
	base.ResourceCustomizations = map[string]config.ResourceCustomization{
		"apps/Deployment": {
			HasHealthLua:    true,
			HealthLuaSHA256: "abc123",
			Provenance:      config.Provenance{Path: "/tmp/left/argocd-cm.yaml"},
		},
	}

	next := config.DefaultSettings()
	next.KustomizeBuildOptions = []config.Value[string]{{Value: "--enable-helm", Provenance: config.Provenance{Path: "/tmp/right/argocd-cm.yaml"}}}
	next.HelmValuesFileSchemes = []config.Value[string]{{Value: "https", Provenance: config.Provenance{Path: "/tmp/right/argocd-cm.yaml"}}}
	next.TrackingMethod = config.Value[string]{Value: string(argoappv1.TrackingMethodAnnotation), Provenance: config.Provenance{Path: "/tmp/right/argocd-cm.yaml"}}
	next.InstanceLabelKey = config.Value[string]{Value: "app.kubernetes.io/instance", Provenance: config.Provenance{Path: "/tmp/right/argocd-cm.yaml"}}
	next.InstallationID = config.Value[string]{Value: "cluster-one", Provenance: config.Provenance{Path: "/tmp/right/argocd-cm.yaml"}}
	next.HelmRepositories = map[string]config.RepositorySettings{
		"https://charts.example.test": {
			Name:       "example",
			URL:        "https://charts.example.test",
			Provenance: config.Provenance{Path: "/tmp/right/repositories.yaml"},
		},
	}
	next.ResourceCustomizations = map[string]config.ResourceCustomization{
		"apps/Deployment": {
			HasHealthLua:    true,
			HealthLuaSHA256: "abc123",
			Provenance:      config.Provenance{Path: "/tmp/right/argocd-cm.yaml"},
		},
	}

	baseSig, err := renderSettingsSignature(base)
	if err != nil {
		t.Fatalf("renderSettingsSignature(base) error = %v", err)
	}
	nextSig, err := renderSettingsSignature(next)
	if err != nil {
		t.Fatalf("renderSettingsSignature(next) error = %v", err)
	}
	if nextSig != baseSig {
		t.Fatalf("render settings signature changed for provenance-only metadata")
	}
}

// directoryTrackingProvider renders every source as a directory under root.
type directoryTrackingProvider struct{ root string }

func (p directoryTrackingProvider) RenderSource(ctx context.Context, source render.ResolvedSource, opts render.RenderOptions) ([]render.Manifest, []diagnostic.Diagnostic, error) {
	source.RepoRoot = p.root
	return render.DirectoryRenderer{}.Render(ctx, source, opts)
}

// TestRenderApplicationFailsDirectoryTrackingBeforeMissingKind pins the
// order in which Argo CD v3.6.1 fails a directory source. The repo-server sets
// the tracking metadata on every manifest before the controller checks kinds
// (GenerateManifests, reposerver/repository/repository.go:1842-1847, and
// unmarshalManifests, controller/state.go:562-571). So a list item whose
// annotations are not a map fails the Application under annotation tracking,
// even when an earlier file has an object without a kind. Label tracking
// does not read the annotations, so the missing kind fails it instead.
func TestRenderApplicationFailsDirectoryTrackingBeforeMissingKind(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{
		"a.json": `{}`,
		"b.json": `{"apiVersion":"v1","kind":"List","items":[{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"q","annotations":"x"}}]}`,
	} {
		path := filepath.Join(root, "manifests", name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	application := trackingTestApplication("argocd", "demo")
	for method, want := range map[argoappv1.TrackingMethod]string{
		argoappv1.TrackingMethodAnnotation:         "manifests/b.json document 0 list item /items/0: failed to set app instance tracking info on manifest",
		argoappv1.TrackingMethodAnnotationAndLabel: "manifests/b.json document 0 list item /items/0: failed to set app instance tracking info on manifest",
		argoappv1.TrackingMethodLabel:              "manifests/a.json: JSON object has no kind",
	} {
		t.Run(string(method), func(t *testing.T) {
			_, err := RenderApplicationWithOptions(context.Background(), application, directoryTrackingProvider{root: root}, ApplicationRenderOptions{
				TrackingOptions: TrackingOptions{Method: string(method)},
			})
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("RenderApplicationWithOptions() error = %v, want %q", err, want)
			}
		})
	}
}

func trackingTestApplication(namespace, name string) argoappv1.Application {
	return argoappv1.Application{
		Name: name, Namespace: namespace,
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
