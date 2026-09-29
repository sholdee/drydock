package app

import (
	"context"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
)

// argocdCMYAML is an argocd-cm ConfigMap whose data holds settings, the
// indented data entries.
func argocdCMYAML(settings string) string {
	return `apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
  namespace: argocd
data:
` + settings
}

// Settings changes that reach every Application: the tracking method through
// the render, resource exclusions only through the diff (renderSettingsSignature
// leaves them out). Each fixture writer takes one side's argocd-cm data.
var (
	trackingAnnotationSettings = "  application.resourceTrackingMethod: annotation\n"
	trackingLabelSettings      = "  application.resourceTrackingMethod: label\n"
	excludeConfigMapSettings   = trackingAnnotationSettings + `  resource.exclusions: |
    - apiGroups: [""]
      kinds: ["ConfigMap"]
      clusters: ["*"]
`
)

// writeDiscoverKustomizeSettingsApps adds argocd/settings, which holds
// argocd-cm, to the graph of writeDiscoverKustomizeChildApps: the
// discover-kustomize child owns argocd-cm, and other owns nothing it
// reaches. settings is argocd-cm's data. Discover it with DiscoverKustomizePaths [clusters/prod-apps] and DiscoverIgnoreGlobs
// [argocd/**], which hides the static copies so --strict sees no duplicate.
func writeDiscoverKustomizeSettingsApps(t *testing.T, root, settings string) {
	t.Helper()
	writeDiscoverKustomizeChildApps(t, root, "v1")
	writeTestFile(t, filepath.Join(root, "argocd", "base", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - child.yaml
  - ../settings
`)
	writeTestFile(t, filepath.Join(root, "argocd", "settings", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - argocd-cm.yaml
`)
	writeTestFile(t, filepath.Join(root, "argocd", "settings", "argocd-cm.yaml"), argocdCMYAML(settings))
}

// writeSelfManagedSettingsApps writes argocd, a self-managed Application
// whose spec.source.path argocd/settings holds argocd-cm, and other.
// settings is argocd-cm's data.
func writeSelfManagedSettingsApps(t *testing.T, root, settings string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", "argocd.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: argocd
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: argocd/settings
  destination:
    name: in-cluster
    namespace: argocd
`)
	writeTestFile(t, filepath.Join(root, "argocd", "settings", "argocd-cm.yaml"), argocdCMYAML(settings))
	writeDiffApplication(t, root, "other", "other", "same")
}

// An Argo CD settings change reaches every Application, whichever one owns
// the settings file, so changed-only renders all of them. That is the whole
// answer, not an ownership gap: neither strict flag fails it.
func TestOrchestratorDiffAppsChangedOnlyRendersAllOnSettingsChange(t *testing.T) {
	for _, tc := range []struct {
		name             string
		write            func(*testing.T, string, string)
		discovery        DiscoveryOptions
		wantApplications []string
	}{
		{
			name:  "discover-kustomize graph owns argocd-cm",
			write: writeDiscoverKustomizeSettingsApps,
			discovery: DiscoveryOptions{
				DiscoverKustomizePaths: []string{"clusters/prod-apps"},
				DiscoverIgnoreGlobs:    []string{"argocd/**"},
			},
			wantApplications: []string{"child", "other"},
		},
		{
			name:             "self-managed Application path holds argocd-cm",
			write:            writeSelfManagedSettingsApps,
			wantApplications: []string{"argocd", "other"},
		},
	} {
		for _, change := range []struct {
			name, left, right string
		}{
			{name: "tracking method", left: trackingAnnotationSettings, right: trackingLabelSettings},
			{name: "resource exclusions", left: trackingAnnotationSettings, right: excludeConfigMapSettings},
		} {
			for _, mode := range []struct {
				name              string
				strictChangedOnly bool
				strict            bool
			}{
				{name: "warn"},
				{name: "strict-changed-only", strictChangedOnly: true},
				{name: "strict", strict: true},
			} {
				t.Run(tc.name+"/"+change.name+"/"+mode.name, func(t *testing.T) {
					root := t.TempDir()
					left := filepath.Join(root, "left")
					right := filepath.Join(root, "right")
					tc.write(t, left, change.left)
					tc.write(t, right, change.right)
					request := DiffRequest{
						LeftPath:         left,
						RightPath:        right,
						DiscoveryOptions: tc.discovery,
						Strict:           mode.strict,
						Unified:          3,
					}

					full, err := Orchestrator{}.DiffApps(context.Background(), request)
					if err != nil {
						t.Fatalf("full DiffApps() error = %v, diagnostics = %#v", err, full.Diagnostics)
					}
					if got := diffedApplications(full); !slices.Equal(got, tc.wantApplications) {
						t.Fatalf("full diff Applications = %v, want %v", got, tc.wantApplications)
					}

					request.ChangedOnly = true
					request.StrictChangedOnly = mode.strictChangedOnly
					result, err := Orchestrator{}.DiffApps(context.Background(), request)
					if err != nil {
						t.Fatalf("changed-only DiffApps() error = %v, diagnostics = %#v", err, result.Diagnostics)
					}
					if got := diffedApplications(result); !slices.Equal(got, tc.wantApplications) {
						t.Fatalf("changed-only diff Applications = %v, want %v", got, tc.wantApplications)
					}
					var changedOnly []diagnostic.Diagnostic
					for _, diag := range result.Diagnostics {
						if diag.Category == "changed-only" {
							changedOnly = append(changedOnly, diag)
						}
					}
					want := []diagnostic.Diagnostic{changedOnlySettingsDiagnostic()}
					if !slices.Equal(changedOnly, want) {
						t.Fatalf("changed-only diagnostics = %#v, want %#v", changedOnly, want)
					}
					// It stays a warning wherever --strict re-checks diagnostics.
					if err := diagnosticFailure(normalizeDiagnostics(changedOnly, true, false), true); err != nil {
						t.Fatalf("strict diagnosticFailure() = %v, want the settings render-all exempt", err)
					}
				})
			}
		}
	}
}

func diffedApplications(result DiffResult) []string {
	names := map[string]struct{}{}
	for _, diffResult := range result.Results {
		names[diffResult.Parent.Name] = struct{}{}
	}
	return slices.Sorted(maps.Keys(names))
}

func TestChangedOnlySettingsSignature(t *testing.T) {
	// Each load reads its own tree, so provenance names a different root.
	load := func(t *testing.T, data string) config.ArgoSettings {
		t.Helper()
		path := filepath.Join(t.TempDir(), "argocd-cm.yaml")
		writeTestFile(t, path, "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: argocd-cm\ndata:\n"+data)
		settings, _, err := config.LoadFromConfigMapDocument(path, 0)
		if err != nil {
			t.Fatalf("LoadFromConfigMapDocument() error = %v", err)
		}
		return settings
	}
	signature := func(t *testing.T, settings config.ArgoSettings) string {
		t.Helper()
		sig, err := changedOnlySettingsSignature(settings)
		if err != nil {
			t.Fatalf("changedOnlySettingsSignature() error = %v", err)
		}
		return sig
	}
	const base = `  helm.valuesFileSchemes: https
  resource.exclusions: |
    - apiGroups: ["cilium.io"]
      kinds: ["CiliumIdentity"]
      clusters: ["*"]
  resource.compareoptions: |
    ignoreAggregatedRoles: true
  resource.customizations.ignoreDifferences.apps_Deployment: |
    jsonPointers:
      - /spec/replicas
`
	baseSig := signature(t, load(t, base))
	if got := signature(t, load(t, base)); got != baseSig {
		t.Fatalf("identical settings in two trees: signature %s, want %s", got, baseSig)
	}

	// Settings a diff reads beyond the render settings change it.
	for name, data := range map[string]string{
		"tracking method":   base + "  application.resourceTrackingMethod: label\n",
		"resource filters":  strings.Replace(base, "CiliumIdentity", "CiliumEndpoint", 1),
		"compare options":   strings.Replace(base, "ignoreAggregatedRoles: true", "ignoreAggregatedRoles: false", 1),
		"ignoreDifferences": strings.Replace(base, "/spec/replicas", "/spec/template", 1),
	} {
		if signature(t, load(t, data)) == baseSig {
			t.Errorf("%s change left the signature unchanged", name)
		}
	}

	settings := load(t, base)
	plugin := func(fileName string) config.ArgoSettings {
		next := settings
		next.ConfigManagementPlugins = map[string]config.ConfigManagementPlugin{
			"cmp": {Name: "cmp", Discover: config.ConfigManagementPluginDiscovery{FileName: fileName}},
		}
		return next
	}
	if signature(t, plugin("a.yaml")) == signature(t, plugin("b.yaml")) {
		t.Error("plugin discovery rule change left the signature unchanged")
	}
	withParameters := settings
	withParameters.CommandParameters = []config.CommandParameterSetting{{Key: "reposerver.parallelism.limit", Value: "4"}}
	if signature(t, withParameters) != baseSig {
		t.Error("command parameters changed the signature; only diag reads them")
	}
}
