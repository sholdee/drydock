package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/diagnostic"
	sourcepkg "github.com/sholdee/drydock/internal/source"
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

// The render cache key must tell no argocd-cm from an argocd-cm with
// "--enable-helm" and no kustomize versions: their build flags match, but
// only the second fails a kustomize.version that it does not register.
func TestRenderCacheKeySeparatesFoundArgoCDConfigMap(t *testing.T) {
	// The render cache keeps renders of the Git work tree under drydock.
	root, fixture := initSelfRepoDiffGitRepo(t, reviewRepoURL)
	writeTestFile(t, filepath.Join(root, "kbuild", "kustomization.yaml"), "resources: [cm.yaml]\n")
	writeTestFile(t, filepath.Join(root, "kbuild", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: probe\n")
	writeReviewApplication(t, root, "kbuild", "kbuild", "    kustomize:\n      version: v9.9.9\n", "demo")
	commitDiffGitRepo(t, fixture.repo, fixture.wt, "baseline")
	request := BuildRequest{
		Path:               root,
		RenderCacheEnabled: true,
		RenderCacheDir:     t.TempDir(),
		EngineFingerprint:  testEngineFingerprint(),
		RecordCacheEvents:  true,
	}
	first, err := (Orchestrator{}).Build(context.Background(), request)
	if err != nil {
		t.Fatalf("Build() without argocd-cm error = %v", err)
	}
	if !hasRenderCacheAction(first.CacheEvents, "store") {
		t.Fatalf("cache events = %v, want the render stored", first.CacheEvents)
	}
	writeReviewArgoCDConfigMap(t, root, "  kustomize.buildOptions: --enable-helm\n")
	commitDiffGitRepo(t, fixture.repo, fixture.wt, "argocd-cm")
	_, err = (Orchestrator{}).Build(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "kustomize version v9.9.9 is not registered") {
		t.Fatalf("Build() with argocd-cm and a warm render cache error = %v, want the version not registered", err)
	}
}

func hasRenderCacheAction(events []cacheevent.Event, action string) bool {
	for _, event := range events {
		if string(event.Source) == "render" && string(event.Action) == action {
			return true
		}
	}
	return false
}

// The managed-by label names the kustomize binary of Argo CD, v5.8.3, for
// --enable-managedby-label and for buildMetadata: [managedByLabel]. krusty
// alone names the version that provenance finds in the build information
// of the program. This test lives in internal/app because its test binary
// does not link sigs.k8s.io/kustomize/kustomize/v5, as the drydock binary
// does not, so krusty alone would write another version here.
func TestKustomizeManagedByLabelNamesArgoCDKustomize(t *testing.T) {
	for _, tc := range []struct {
		name          string
		kustomization string
		data          string
		want          string
	}{
		{name: "no label", kustomization: "resources: [cm.yaml]\n"},
		{name: "flag", kustomization: "resources: [cm.yaml]\n", data: "  kustomize.buildOptions: --enable-managedby-label\n", want: "kustomize-v5.8.3"},
		{name: "build metadata", kustomization: "resources: [cm.yaml]\nbuildMetadata: [managedByLabel]\n", want: "kustomize-v5.8.3"},
		{name: "flag with helmCharts", kustomization: "resources: [cm.yaml]\nhelmCharts:\n  - name: probe\n    releaseName: probe\n", data: "  kustomize.buildOptions: --enable-helm --enable-managedby-label\n", want: "kustomize-v5.8.3"},
		// Without the option, a label of the source stays.
		{name: "label of the source", kustomization: "resources: [labeled.yaml]\n", data: "  kustomize.buildOptions: --enable-helm\n", want: "someone-else"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeKustomizeBuildOptionsSource(t, root)
			writeTestFile(t, filepath.Join(root, "kbuild", "kustomization.yaml"), tc.kustomization)
			writeTestFile(t, filepath.Join(root, "kbuild", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: plain\n")
			writeTestFile(t, filepath.Join(root, "kbuild", "labeled.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: labeled\n  labels:\n    app.kubernetes.io/managed-by: someone-else\n")
			writeReviewApplication(t, root, "kbuild", "kbuild", "", "demo")
			if tc.data != "" {
				writeReviewArgoCDConfigMap(t, root, tc.data)
			}
			result, err := Orchestrator{}.Build(context.Background(), BuildRequest{
				Path:     root,
				RepoMaps: []sourcepkg.RepoMap{{URL: reviewRepoURL, Path: root}},
				Offline:  true,
			})
			if err != nil {
				t.Fatalf("Build() error = %v", err)
			}
			if len(result.ApplicationManifests) == 0 {
				t.Fatal("Build() rendered nothing")
			}
			for _, item := range result.ApplicationManifests {
				if got := item.Manifest.Object.GetLabels()["app.kubernetes.io/managed-by"]; got != tc.want {
					t.Fatalf("%s managed-by = %q, want %q", item.Manifest.Object.GetName(), got, tc.want)
				}
			}
		})
	}
}
