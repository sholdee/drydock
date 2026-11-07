package render

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func writeBuildFlagsHelmChartsFixture(t *testing.T, root string) {
	t.Helper()
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), "helmCharts:\n  - name: probe\n    releaseName: probe\n")
	writeHelmCapabilitiesProbeChart(t, filepath.Join(root, "app"))
}

// helmCharts render only when kustomize turns Helm on: with --enable-helm,
// or with --enable-alpha-plugins (HonorKustomizeFlags). Without a found
// argocd-cm, drydock assumes --enable-helm.
func TestKustomizeHelmChartsNeedEnableHelm(t *testing.T) {
	for _, tc := range []struct {
		name    string
		caller  KustomizeBuildCaller
		options []string
		wantErr string
	}{
		{name: "no argocd-cm", caller: KustomizeCallerArgoCDAssumed},
		{name: "argocd-cm without options", caller: KustomizeCallerArgoCD, wantErr: "trouble configuring builtin HelmChartInflationGenerator: must specify --enable-helm"},
		{name: "argocd-cm with other options", caller: KustomizeCallerArgoCD, options: []string{"--load-restrictor", "LoadRestrictionsNone"}, wantErr: "must specify --enable-helm"},
		{name: "enable helm false", caller: KustomizeCallerArgoCD, options: []string{"--enable-helm=false"}, wantErr: "must specify --enable-helm"},
		{name: "enable helm", caller: KustomizeCallerArgoCD, options: []string{"--enable-helm"}},
		{name: "alpha plugins", caller: KustomizeCallerArgoCD, options: []string{"--enable-alpha-plugins"}},
		{name: "plugin command without enable helm", caller: KustomizeCallerCommand, wantErr: "must specify --enable-helm"},
		{name: "empty helm command", caller: KustomizeCallerArgoCD, options: []string{"--enable-helm", "--helm-command="}, wantErr: "must specify --helm-command"},
		{name: "default helm command", caller: KustomizeCallerArgoCD, options: []string{"--enable-helm", "--helm-command", "helm"}},
		{name: "helm command path", caller: KustomizeCallerArgoCD, options: []string{"--enable-helm", "--helm-command", "/usr/local/bin/helm"}},
		{name: "other helm command", caller: KustomizeCallerArgoCD, options: []string{"--enable-helm", "--helm-command", "/usr/local/bin/helm3"}, wantErr: `--helm-command "/usr/local/bin/helm3" runs a program other than helm`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeBuildFlagsHelmChartsFixture(t, root)
			_, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: root, Path: "app"}, RenderOptions{
				KustomizeCaller: tc.caller,
				BuildOptions:    tc.options,
			})
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Render() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Render() error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

// --helm-command matters only for helmCharts: a kustomization without
// them renders with any value, as kustomize renders it.
func TestKustomizeHelmCommandWithoutHelmCharts(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), "resources: [cm.yaml]\n")
	writeFile(t, filepath.Join(root, "app", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: plain\n")
	manifests, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: root, Path: "app"}, RenderOptions{
		KustomizeCaller: KustomizeCallerArgoCD,
		BuildOptions:    []string{"--helm-command", "/opt/helm"},
	})
	if err != nil || len(manifests) != 1 {
		t.Fatalf("Render() = %d manifests, error %v", len(manifests), err)
	}
}

// --reorder none keeps the order of the resources, as kustomize does.
func TestKustomizeReorderNoneKeepsResourceOrder(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), "resources: [cm.yaml, ns.yaml]\n")
	writeFile(t, filepath.Join(root, "app", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a\n")
	writeFile(t, filepath.Join(root, "app", "ns.yaml"), "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: n\n")
	manifests, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: root, Path: "app"}, RenderOptions{
		KustomizeCaller: KustomizeCallerArgoCD,
		BuildOptions:    []string{"--reorder", "none"},
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if got := kustomizeManifestKinds(manifests); got != "ConfigMap,Namespace" {
		t.Fatalf("kinds = %s, want the resource order", got)
	}
}

// Without --reorder, kustomize sorts in the legacy order: the Namespace
// comes first.
func TestKustomizeDefaultOrderIsLegacy(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), "resources: [cm.yaml, ns.yaml]\n")
	writeFile(t, filepath.Join(root, "app", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a\n")
	writeFile(t, filepath.Join(root, "app", "ns.yaml"), "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: n\n")
	for _, opts := range []RenderOptions{{}, {KustomizeCaller: KustomizeCallerArgoCD, BuildOptions: []string{"--reorder", "legacy"}}} {
		manifests, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: root, Path: "app"}, opts)
		if err != nil {
			t.Fatalf("Render() error = %v", err)
		}
		if got := kustomizeManifestKinds(manifests); got != "Namespace,ConfigMap" {
			t.Fatalf("kinds = %s with %q, want the legacy order", got, opts.BuildOptions)
		}
	}
}

func kustomizeManifestKinds(manifests []Manifest) string {
	kinds := make([]string, 0, len(manifests))
	for _, manifest := range manifests {
		kinds = append(kinds, manifest.Object.GetKind())
	}
	return strings.Join(kinds, ",")
}

// helm template splits each --api-versions value at commas, as CSV.
func TestHelmCapabilitiesSplitAPIVersionsFlagValues(t *testing.T) {
	capabilities, err := helmCapabilities(RenderOptions{APIVersions: []string{"a.example/v1,b.example/v1", `"c.example/v1,x"`}})
	if err != nil {
		t.Fatalf("helmCapabilities() error = %v", err)
	}
	for _, want := range []string{"a.example/v1", "b.example/v1", "c.example/v1,x"} {
		if !capabilities.APIVersions.Has(want) {
			t.Fatalf("APIVersions has no %q", want)
		}
	}
	if capabilities.APIVersions.Has("a.example/v1,b.example/v1") {
		t.Fatal("APIVersions holds the value that helm splits")
	}
	if _, err := helmCapabilities(RenderOptions{APIVersions: []string{`a"b`}}); err == nil || !strings.Contains(err.Error(), `invalid argument "a\"b" for "-a, --api-versions" flag`) {
		t.Fatalf("helmCapabilities() error = %v, want the helm flag error", err)
	}
}

// kustomize writes the build to the --output path. A path that names the
// standard output gives the usual output. Another path leaves it empty, so
// Argo CD reads no manifests and does not fail. A build error still fails.
func TestKustomizeOutputFlag(t *testing.T) {
	for _, tc := range []struct {
		name    string
		options []string
		want    int
		warn    bool
		wantErr string
	}{
		{name: "dev stdout", options: []string{"-o", "/dev/stdout"}, want: 1},
		{name: "fd 1", options: []string{"--output=/dev/fd/1"}, want: 1},
		{name: "proc self fd 1", options: []string{"--output", "/proc/self/fd/1"}, want: 1},
		{name: "file", options: []string{"-o", "out.yaml"}, warn: true},
		{name: "dev stderr", options: []string{"-o", "/dev/stderr"}, warn: true},
		{name: "build error first", options: []string{"-o", "out.yaml", "--reorder", "bad"}, wantErr: "illegal flag value --reorder bad"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), "resources: [cm.yaml]\n")
			writeFile(t, filepath.Join(root, "app", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: plain\n")
			manifests, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: root, Path: "app"}, RenderOptions{
				KustomizeCaller: KustomizeCallerArgoCD,
				BuildOptions:    tc.options,
			})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Render() error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Render() error = %v", err)
			}
			if len(manifests) != tc.want {
				t.Fatalf("manifests = %d, want %d", len(manifests), tc.want)
			}
			warned := false
			for _, diag := range diags {
				warned = warned || diag.Code == KustomizeBuildOutputDiscardedCode
			}
			if warned != tc.warn {
				t.Fatalf("diagnostics = %#v, want the --output warning %t", diags, tc.warn)
			}
		})
	}
}
