package render

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/kustomize/api/types"
)

func TestKustomizeRendererRendersResources(t *testing.T) {
	renderer := KustomizeRenderer{}
	source := ResolvedSource{
		RepoRoot: filepath.Join("..", "..", "testdata", "applications"),
		Path:     "kustomize",
	}

	result, diags, err := renderer.Render(context.Background(), source, RenderOptions{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(result) != 1 {
		t.Fatalf("len(result) = %d, want 1", len(result))
	}
	if result[0].Object.GetKind() != "ConfigMap" || result[0].Object.GetName() != "kustomized" {
		t.Fatalf("unexpected object: %#v", result[0].Object)
	}
	if result[0].Path != filepath.Join("kustomize", "kustomization.yaml") {
		t.Fatalf("Path = %q, want kustomize/kustomization.yaml", result[0].Path)
	}
}

func TestKustomizeRendererAllowsRepoRootLocalComponents(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "components", "namespace", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1alpha1
kind: Component
resources:
  - serviceaccount.yaml
`)
	writeFile(t, filepath.Join(root, "components", "namespace", "serviceaccount.yaml"), `
apiVersion: v1
kind: ServiceAccount
metadata:
  name: demo
`)
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
namespace: demo
components:
  - ../../components/namespace
resources:
  - cm.yaml
`)
	writeFile(t, filepath.Join(root, "apps", "demo", "cm.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
`)

	result, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     filepath.Join("apps", "demo"),
	}, RenderOptions{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(result) != 2 {
		t.Fatalf("len(result) = %d, want 2", len(result))
	}
}

func TestKustomizeRendererHonorsLoadRestrictionsNone(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "shared", "cm.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: shared
`)
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ../../shared/cm.yaml
`)

	_, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     filepath.Join("apps", "demo"),
	}, RenderOptions{})
	if err == nil {
		t.Fatal("Render() error = nil, want default load restriction error")
	}

	result, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     filepath.Join("apps", "demo"),
	}, RenderOptions{BuildOptions: []string{"--load-restrictor=LoadRestrictionsNone"}})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(result) != 1 || result[0].Object.GetName() != "shared" {
		t.Fatalf("result = %#v, want shared ConfigMap", result)
	}
}

func TestKustomizeRendererAppliesSourceKustomizeOptions(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "components", "extra", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1alpha1
kind: Component
resources:
  - cm.yaml
  - ../shared/cm.yaml
`)
	writeFile(t, filepath.Join(root, "components", "extra", "cm.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: component
`)
	writeFile(t, filepath.Join(root, "components", "shared", "cm.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: shared-component
`)
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - deployment.yaml
`)
	writeFile(t, filepath.Join(root, "apps", "demo", "deployment.yaml"), `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
spec:
  replicas: 1
  selector:
    matchLabels:
      app: web
  template:
    metadata:
      labels:
        app: web
    spec:
      containers:
        - name: web
          image: nginx:1.24.0
`)

	manifests, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     filepath.Join("apps", "demo"),
	}, RenderOptions{
		Kustomize: &argoappv1.ApplicationSourceKustomize{
			NamePrefix: "pre-",
			NameSuffix: "-suf",
			Namespace:  "demo-ns",
			Images: argoappv1.KustomizeImages{
				"nginx=registry.example.invalid/nginx:$ARGOCD_APP_NAME",
			},
			Replicas: argoappv1.KustomizeReplicas{{
				Name:  "web",
				Count: intstr.FromInt(2),
			}},
			CommonLabels: map[string]string{
				"fleet": "$ARGOCD_APP_NAME",
			},
			ForceCommonLabels: true,
			CommonAnnotations: map[string]string{
				"owner": "$ARGOCD_APP_NAME",
			},
			CommonAnnotationsEnvsubst: true,
			ForceCommonAnnotations:    true,
			Patches: argoappv1.KustomizePatches{{
				Patch: `apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  labels:
    patched: "true"
`,
			}},
			Components: []string{"../../components/extra"},
		},
		ArgoEnv: argoappv1.Env{
			{Name: "ARGOCD_APP_NAME", Value: "demo"},
		},
		BuildOptions: []string{"--load-restrictor=LoadRestrictionsNone"},
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}

	deployment := findManifest(manifests, "Deployment", "pre-web-suf")
	if deployment == nil {
		t.Fatalf("manifests = %#v, missing transformed Deployment", manifests)
	}
	if deployment.GetNamespace() != "demo-ns" {
		t.Fatalf("Deployment namespace = %q, want demo-ns", deployment.GetNamespace())
	}
	if got, _, _ := unstructured.NestedInt64(deployment.Object, "spec", "replicas"); got != 2 {
		t.Fatalf("Deployment replicas = %d, want 2", got)
	}
	containers, _, _ := unstructured.NestedSlice(deployment.Object, "spec", "template", "spec", "containers")
	if len(containers) != 1 {
		t.Fatalf("containers = %#v", containers)
	}
	container, _ := containers[0].(map[string]any)
	if got := container["image"]; got != "registry.example.invalid/nginx:demo" {
		t.Fatalf("container image = %#v, want env-substituted image", got)
	}
	if deployment.GetLabels()["fleet"] != "demo" {
		t.Fatalf("Deployment labels = %#v, want fleet=demo", deployment.GetLabels())
	}
	if deployment.GetLabels()["patched"] != "true" {
		t.Fatalf("Deployment labels = %#v, want patched=true", deployment.GetLabels())
	}
	if deployment.GetAnnotations()["owner"] != "demo" {
		t.Fatalf("Deployment annotations = %#v, want owner=demo", deployment.GetAnnotations())
	}
	if !containsManifest(manifests, "ConfigMap", "pre-component-suf") {
		t.Fatalf("manifests = %#v, missing component ConfigMap", manifests)
	}
	if !containsManifest(manifests, "ConfigMap", "pre-shared-component-suf") {
		t.Fatalf("manifests = %#v, missing component referenced sibling ConfigMap", manifests)
	}
}

func TestKustomizeRendererDoesNotEnvsubstCommonAnnotationsByDefault(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cm.yaml
`)
	writeFile(t, filepath.Join(root, "apps", "demo", "cm.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
`)

	manifests, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     filepath.Join("apps", "demo"),
	}, RenderOptions{
		Kustomize: &argoappv1.ApplicationSourceKustomize{
			CommonAnnotations: map[string]string{"owner": "$ARGOCD_APP_NAME"},
		},
		ArgoEnv: argoappv1.Env{{Name: "ARGOCD_APP_NAME", Value: "demo"}},
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	configMap := findManifest(manifests, "ConfigMap", "demo")
	if configMap == nil {
		t.Fatalf("manifests = %#v, missing ConfigMap", manifests)
	}
	if got := configMap.GetAnnotations()["owner"]; got != "$ARGOCD_APP_NAME" {
		t.Fatalf("annotation owner = %q, want literal variable", got)
	}
}

func TestKustomizeRendererDoesNotMutateSourceTreeForSourceOptions(t *testing.T) {
	root := t.TempDir()
	kustomizationPath := filepath.Join(root, "apps", "demo", "kustomization.yaml")
	original := `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cm.yaml
`
	writeFile(t, kustomizationPath, original)
	writeFile(t, filepath.Join(root, "apps", "demo", "cm.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
`)

	_, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     filepath.Join("apps", "demo"),
	}, RenderOptions{
		Kustomize: &argoappv1.ApplicationSourceKustomize{NamePrefix: "source-"},
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	data, err := os.ReadFile(kustomizationPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != original {
		t.Fatalf("source kustomization mutated:\n%s", string(data))
	}
	if _, err := os.Stat(filepath.Join(root, ".drydock")); !os.IsNotExist(err) {
		t.Fatalf("repo .drydock Stat() error = %v, want missing", err)
	}
}

func TestKustomizeRendererHandlesSourceKustomizeMissingComponents(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cm.yaml
`)
	writeFile(t, filepath.Join(root, "apps", "demo", "cm.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
`)

	_, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     filepath.Join("apps", "demo"),
	}, RenderOptions{
		Kustomize: &argoappv1.ApplicationSourceKustomize{Components: []string{"../missing"}},
	})
	if err == nil {
		t.Fatal("Render() error = nil, want missing component error")
	}

	manifests, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     filepath.Join("apps", "demo"),
	}, RenderOptions{
		Kustomize: &argoappv1.ApplicationSourceKustomize{
			Components:              []string{"../missing"},
			IgnoreMissingComponents: true,
		},
	})
	if err != nil {
		t.Fatalf("Render() with ignoreMissingComponents error = %v", err)
	}
	if !containsManifest(manifests, "ConfigMap", "demo") {
		t.Fatalf("manifests = %#v, want demo ConfigMap", manifests)
	}
}

func TestKustomizeRendererRejectsSourceKustomizePathEscapes(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cm.yaml
`)
	writeFile(t, filepath.Join(root, "apps", "demo", "cm.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
`)

	_, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     filepath.Join("apps", "demo"),
	}, RenderOptions{
		Kustomize: &argoappv1.ApplicationSourceKustomize{
			Components: []string{"../../../outside"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("Render() error = %v, want escape error", err)
	}
}

func TestKustomizeRendererReportsSourceKustomizeVersionMetadataOnly(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cm.yaml
`)
	writeFile(t, filepath.Join(root, "apps", "demo", "cm.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
`)

	_, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     filepath.Join("apps", "demo"),
	}, RenderOptions{
		Kustomize: &argoappv1.ApplicationSourceKustomize{Version: "v4.5.7"},
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 1 || !strings.Contains(diags[0].Message, "source kustomize version") {
		t.Fatalf("diagnostics = %#v, want source kustomize version warning", diags)
	}
}

func TestHasKustomizeSourceMutationsIgnoresMetadataOnlyOptions(t *testing.T) {
	if hasKustomizeSourceMutations(RenderOptions{Kustomize: &argoappv1.ApplicationSourceKustomize{}}) {
		t.Fatal("empty source kustomize unexpectedly requires mutation")
	}
	if hasKustomizeSourceMutations(RenderOptions{Kustomize: &argoappv1.ApplicationSourceKustomize{
		Version:     "v4.5.7",
		KubeVersion: "1.30.1",
		APIVersions: []string{"example.com/v1/Foo"},
	}}) {
		t.Fatal("metadata-only source kustomize options unexpectedly require mutation")
	}
	if !hasKustomizeSourceMutations(RenderOptions{Kustomize: &argoappv1.ApplicationSourceKustomize{NamePrefix: "demo-"}}) {
		t.Fatal("namePrefix source kustomize option did not require mutation")
	}
}

func TestKustomizeHelmKubeVersionFallsBackToSourceOption(t *testing.T) {
	opts := RenderOptions{KubeVersion: "1.30.1"}
	if got := kustomizeHelmKubeVersion(types.HelmChart{}, opts); got != "1.30.1" {
		t.Fatalf("kustomizeHelmKubeVersion() = %q, want source kube version", got)
	}
	if got := kustomizeHelmKubeVersion(types.HelmChart{KubeVersion: "1.29.0"}, opts); got != "1.29.0" {
		t.Fatalf("kustomizeHelmKubeVersion() = %q, want helm chart kube version", got)
	}
}
