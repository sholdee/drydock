package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/diagnostic"
)

func writeKustomizeBuildOptionsSource(t *testing.T, root string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "kbuild", "kustomization.yaml"), "helmCharts:\n  - name: probe\n    releaseName: probe\n")
	writeTestFile(t, filepath.Join(root, "kbuild", "charts", "probe", "Chart.yaml"), "apiVersion: v2\nname: probe\nversion: 0.1.0\n")
	writeTestFile(t, filepath.Join(root, "kbuild", "charts", "probe", "values.yaml"), "{}\n")
	writeTestFile(t, filepath.Join(root, "kbuild", "charts", "probe", "templates", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: probe\ndata:\n  kube: {{ .Capabilities.KubeVersion.Version | quote }}\n  app: {{ .Capabilities.APIVersions.Has \"app.example.com/v1\" | quote }}\n")
}

// With a found argocd-cm, kustomize.buildOptions decide the flags of the
// build. kustomize renders helmCharts only with Helm on, so an argocd-cm
// without --enable-helm fails them as Argo CD does. Without an argocd-cm,
// drydock assumes --enable-helm, as before.
func TestKustomizeBuildOptionsFromArgoCDConfigMap(t *testing.T) {
	block := "    kustomize:\n      kubeVersion: 1.31.0\n      apiVersions: [app.example.com/v1]\n"
	for _, tc := range []struct {
		name    string
		data    string
		want    map[string]any
		wantErr string
	}{
		{name: "no argocd-cm", want: map[string]any{"kube": "v1.31.0", "app": "true"}},
		{name: "argocd-cm without buildOptions", data: "  application.instanceLabelKey: argocd.argoproj.io/instance\n", wantErr: "must specify --enable-helm"},
		{name: "enable helm", data: "  kustomize.buildOptions: --enable-helm\n", want: map[string]any{"kube": "v1.31.0", "app": "true"}},
		// Argo CD adds the kube and API versions of the Application only
		// when the value names --enable-helm (isHelmEnabled), and kustomize
		// keeps the last --helm-kube-version.
		{name: "alpha plugins only", data: "  kustomize.buildOptions: --enable-alpha-plugins --helm-kube-version 1.29.0\n", want: map[string]any{"kube": "v1.29.0", "app": "false"}},
		{name: "enable helm wins over the options kube version", data: "  kustomize.buildOptions: --enable-helm --helm-kube-version 1.29.0\n", want: map[string]any{"kube": "v1.31.0", "app": "true"}},
		{name: "unknown flag", data: "  kustomize.buildOptions: --enable-helm --no-such-flag\n", wantErr: "kustomize build: unknown flag: --no-such-flag"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeKustomizeBuildOptionsSource(t, root)
			writeReviewApplication(t, root, "kbuild", "kbuild", block, "demo")
			if tc.data != "" {
				writeReviewArgoCDConfigMap(t, root, tc.data)
			}
			data, err := buildConfigMapData(t, root, "kbuild", "probe")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Build() error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Build() error = %v", err)
			}
			for key, want := range tc.want {
				if data[key] != want {
					t.Fatalf("data = %v, want %s=%v", data, key, want)
				}
			}
		})
	}
}

// Argo CD v3.6.1 fails a Kustomize source whose kustomize.version argocd-cm
// does not register, before it reads the kube version of the source
// (GetKustomizeBinaryPath).
func TestKustomizeVersionMustBeRegistered(t *testing.T) {
	for _, tc := range []struct {
		name    string
		data    string
		wantErr string
	}{
		{name: "no argocd-cm"},
		{name: "not registered", data: "  kustomize.buildOptions: --enable-helm\n", wantErr: "error getting kustomize binary path: kustomize version v5.0.0 is not registered"},
		{name: "version key", data: "  kustomize.buildOptions: --enable-helm\n  kustomize.version.v5.0.0: /custom/kustomize\n"},
		{name: "path key", data: "  kustomize.buildOptions: --enable-helm\n  kustomize.path.v5.0.0: /custom/kustomize\n"},
		{name: "duplicate", data: "  kustomize.buildOptions: --enable-helm\n  kustomize.path.v5.0.0: /custom/kustomize\n  kustomize.version.v5.0.0: /custom/kustomize\n", wantErr: `failed to add kustomize version from "kustomize.version.v5.0.0": found duplicate kustomize version: v5.0.0`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeKustomizeBuildOptionsSource(t, root)
			writeReviewApplication(t, root, "kbuild", "kbuild", "    kustomize:\n      version: v5.0.0\n      kubeVersion: not-a-version-but-checked-later\n", "demo")
			if tc.data != "" {
				writeReviewArgoCDConfigMap(t, root, tc.data)
			}
			_, err := buildConfigMapData(t, root, "kbuild", "probe")
			want := tc.wantErr
			if want == "" {
				// A registered version renders, and then the kube version
				// fails.
				want = "not-a-version-but-checked-later"
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("Build() error = %v, want %q", err, want)
			}
		})
	}
}

// An install that renders the argo-cd chart with helmCharts can set
// kustomize.buildOptions where its values files do not show it, here in
// valuesInline. The renders that read the settings of the install assume
// --enable-helm, so drydock still finds the value, and the final render of
// the install, with the found settings, passes as in Argo CD.
//
// This covers the settings pass (renderSettingsInstalls).
// TestSettingsFromRenderedArgoCDInstallReplacesCommittedPatch covers the
// discovery frontier (assumeKustomizeHelmForSettingsInstall): its install
// holds an argocd-cm file, so the frontier renders it.
func TestSettingsInstallRenderAssumesEnableHelm(t *testing.T) {
	root := t.TempDir()
	writeKustomizeArgoCDInstall(t, root, []string{"values.yaml"})
	writeTestFile(t, filepath.Join(root, "argocd", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
namespace: argocd
helmCharts:
  - name: argo-cd
    repo: `+fakeArgoCDChartRepo+`
    version: `+fakeArgoCDChartVersion+`
    releaseName: argocd
    namespace: argocd
    valuesFile: values.yaml
    valuesInline:
      configs:
        cm:
          kustomize.buildOptions: --enable-helm --load-restrictor LoadRestrictionsRootOnly
`)
	// application.resourceTrackingMethod makes the values file a settings
	// file, so drydock finds an argocd-cm without kustomize.buildOptions
	// before it renders the install.
	writeTestFile(t, filepath.Join(root, "argocd", "values.yaml"), "configs:\n  cm:\n    application.resourceTrackingMethod: annotation\n")

	result, err := Orchestrator{ChartAcquirer: fakeArgoCDChartAcquirer(t)}.Build(context.Background(), BuildRequest{Path: root, Parallelism: 1})
	if err != nil {
		t.Fatalf("Build() error = %v, diagnostics = %#v", err, result.Diagnostics)
	}
	if got := settingsBuildOptions(result.Settings); strings.Join(got, " ") != "--enable-helm --load-restrictor LoadRestrictionsRootOnly" {
		t.Fatalf("kustomize.buildOptions = %q, want the value of the install", got)
	}
	for _, diag := range result.Diagnostics {
		if diag.Code == diagnostic.CodeSettingsInstallRenderFailed {
			t.Fatalf("diagnostic = %#v, want the install rendered", diag)
		}
	}
}
