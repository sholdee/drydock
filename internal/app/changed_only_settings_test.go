package app

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/render"
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

// Repository and cluster settings fixtures. argocd is a self-managed
// Application in the default project whose spec.source.path argocd/settings
// holds AppProject team-a and the settings Secrets, so argocd owns every
// changed settings file. A repository or cluster entry reaches the other
// Applications only through project validation.
const (
	chartsXURL       = "https://charts.example/x"
	chartsYURL       = "https://charts.example/y"
	inClusterServer  = "https://kubernetes.default.svc"
	prodServer       = "https://prod.example"
	prodBServer      = "https://prod-b.example"
	inClusterDestYML = "    server: " + inClusterServer + "\n    namespace: workloads\n"
)

// writeSettingsOwnerApp writes argocd with files (name to content, an empty
// content skipped) under argocd/settings.
func writeSettingsOwnerApp(t *testing.T, root string, files map[string]string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", "argocd.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: argocd
  namespace: argocd
spec:
  source:
    repoURL: `+otherRepoURL+`
    targetRevision: main
    path: argocd/settings
  destination:
    server: `+inClusterServer+`
    namespace: argocd
`)
	for name, content := range files {
		if content != "" {
			writeTestFile(t, filepath.Join(root, "argocd", "settings", name+".yaml"), content)
		}
	}
}

// repositorySecretYAML is a repository Secret for url scoped to project.
func repositorySecretYAML(url, project string) string {
	return `apiVersion: v1
kind: Secret
metadata:
  name: repository
  namespace: argocd
  labels:
    argocd.argoproj.io/secret-type: repository
stringData:
  url: "` + url + `"
  project: ` + project + `
`
}

// clusterSecretYAML is a cluster Secret for server named name, scoped to
// project unless it is empty.
func clusterSecretYAML(name, server, project string) string {
	secret := `apiVersion: v1
kind: Secret
metadata:
  name: cluster
  namespace: argocd
  labels:
    argocd.argoproj.io/secret-type: cluster
stringData:
  name: ` + name + `
  server: ` + server + `
`
	if project != "" {
		secret += "  project: " + project + "\n"
	}
	return secret
}

// writeRepositorySecretApps writes argocd with AppProject team-a, which
// permits only otherRepoURL, and, unless secretURL is empty, a team-a
// repository Secret for secretURL; each of uses (an Application name to its
// source repoURL) in team-a; and other in team-a sourced from otherRepoURL.
// A team-a repository entry joins team-a's sourceRepos
// (project.effectiveProject), so it changes the validation of only the
// Application whose source uses its URL.
func writeRepositorySecretApps(t *testing.T, root, secretURL string, uses map[string]string) {
	t.Helper()
	writeRepositorySecretProjectApps(t, root, otherRepoURL, secretURL, uses)
}

// writeRepositorySecretProjectApps is writeRepositorySecretApps with
// AppProject team-a permitting only sourceRepo.
func writeRepositorySecretProjectApps(t *testing.T, root, sourceRepo, secretURL string, uses map[string]string) {
	t.Helper()
	secret := ""
	if secretURL != "" {
		secret = repositorySecretYAML(secretURL, "team-a")
	}
	writeSettingsOwnerApp(t, root, map[string]string{"team-a": appProjectYAML("team-a", sourceRepo), "repository": secret})
	for name, repoURL := range uses {
		writeBuildApplicationWithProject(t, root, name, name, "team-a", repoURL, "workloads")
	}
	writeBuildApplicationWithProject(t, root, "other", "other", "team-a", otherRepoURL, "workloads")
}

// writeDefaultRepositorySecretApps writes argocd with no AppProject and,
// unless secretURL is empty, a default-project repository Secret for
// secretURL; uses-x in project sourced from chartsXURL; and other in project
// sourced from otherRepoURL. With no AppProject, every Application
// validates against the implicit default project, whatever project it names.
func writeDefaultRepositorySecretApps(t *testing.T, root, secretURL, project string) {
	t.Helper()
	secret := ""
	if secretURL != "" {
		secret = repositorySecretYAML(secretURL, argoappv1.DefaultAppProjectName)
	}
	owners := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: settings-owners\ndata:\n  owner: platform\n"
	writeSettingsOwnerApp(t, root, map[string]string{"owners": owners, "repository": secret})
	writeBuildApplicationWithProject(t, root, "uses-x", "uses-x", project, chartsXURL, "workloads")
	writeBuildApplicationWithProject(t, root, "other", "other", project, otherRepoURL, "workloads")
}

// clusterProjectYAML is AppProject team-a permitting every source and the
// destinations (YAML list items), enforcing project-scoped clusters when
// permitOnlyProjectScoped.
func clusterProjectYAML(permitOnlyProjectScoped bool, destinations string) string {
	project := `apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: team-a
  namespace: argocd
spec:
  sourceRepos:
    - "*"
  destinations:
` + destinations
	if permitOnlyProjectScoped {
		project += "  permitOnlyProjectScopedClusters: true\n"
	}
	return project
}

// writeDestinationApp writes name in project with destination (YAML lines
// under spec.destination), rendering a ConfigMap from manifests/<name>.
func writeDestinationApp(t *testing.T, root, name, project, destination string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", name+".yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: `+name+`
  namespace: argocd
spec:
  project: `+project+`
  source:
    repoURL: `+otherRepoURL+`
    path: manifests/`+name+`
  destination:
`+destination)
	writeTestFile(t, filepath.Join(root, "manifests", name, "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: `+name+`
data:
  value: demo
`)
}

// writeClusterSecretApps writes argocd with project (AppProject team-a) and,
// unless it is empty, cluster (a cluster Secret); to-prod in team-a with
// destination; bystander in team-a deployed in-cluster; and other in the
// default project deployed in-cluster.
func writeClusterSecretApps(t *testing.T, root, project, cluster, destination string) {
	t.Helper()
	writeSettingsOwnerApp(t, root, map[string]string{"team-a": project, "cluster": cluster})
	writeDestinationApp(t, root, "to-prod", "team-a", destination)
	writeDestinationApp(t, root, "bystander", "team-a", inClusterDestYML)
	writeDestinationApp(t, root, "other", argoappv1.DefaultAppProjectName, inClusterDestYML)
}

// noEffectSettings changes only settings no build, diff, or test output
// reads: ignoreResourceUpdatesEnabled, an ignoreResourceUpdates
// customization, and action Lua.
var noEffectSettings = trackingAnnotationSettings + `  resource.ignoreResourceUpdatesEnabled: "false"
  resource.customizations.ignoreResourceUpdates.apps_Deployment: |
    jsonPointers:
      - /spec/replicas
  resource.customizations.actions.apps_Deployment: |
    definitions:
      - name: restart
        action.lua: |
          return obj
`

// ignoreDifferencesSettings adds a diff normalization rule: it keeps
// rendering every Application.
var ignoreDifferencesSettings = trackingAnnotationSettings + `  resource.customizations.ignoreDifferences.apps_Deployment: |
    jsonPointers:
      - /spec/replicas
`

func destinationDenied(app, project string) projectDiagnosticKey {
	return projectDiagnosticKey{
		Code:       diagnostic.CodeProjectDestinationDenied,
		Message:    fmt.Sprintf("Application argocd/%s destination is not permitted by AppProject %q", app, project),
		Provenance: diagnostic.Provenance{Path: "argocd/" + app},
	}
}

func destinationNameUnresolved(app, name string) projectDiagnosticKey {
	return projectDiagnosticKey{
		Code:       diagnostic.CodeProjectUnspecified,
		Message:    fmt.Sprintf("Application argocd/%s destination name %q cannot be resolved against AppProject server policy offline", app, name),
		Provenance: diagnostic.Provenance{Path: "argocd/" + app},
	}
}

// A repository or cluster settings change selects only the Applications it
// reaches through project validation, besides the owner of the changed
// settings file, and reports how many with a diagnostic --strict and
// --strict-changed-only accept. Settings no output reads select nothing.
func TestOrchestratorDiffAppsChangedOnlySelectsSettingsReach(t *testing.T) {
	repositoryApps := func(uses map[string]string) func(*testing.T, string, string) {
		return func(t *testing.T, root, url string) {
			t.Helper()
			writeRepositorySecretApps(t, root, url, uses)
		}
	}
	for _, tc := range []struct {
		name        string
		write       func(*testing.T, string, string)
		left, right string
		// wantRendered are the source paths changed-only renders.
		wantRendered []string
		// wantReached is the count the scoped diagnostic reports, 0 for none.
		wantReached int
	}{
		{
			name:         "repository Secret added",
			write:        repositoryApps(map[string]string{"uses-x": chartsXURL, "uses-y": chartsYURL}),
			right:        chartsXURL,
			wantRendered: []string{"argocd/settings", "manifests/uses-x"},
			wantReached:  1,
		},
		{
			name:         "repository Secret URL changed",
			write:        repositoryApps(map[string]string{"uses-x": chartsXURL, "uses-y": chartsYURL}),
			left:         chartsXURL,
			right:        chartsYURL,
			wantRendered: []string{"argocd/settings", "manifests/uses-x", "manifests/uses-y"},
			wantReached:  2,
		},
		{
			name: "cluster Secret renamed",
			write: func(t *testing.T, root, name string) {
				t.Helper()
				project := clusterProjectYAML(false, "    - server: \"*\"\n      namespace: \"*\"\n")
				writeClusterSecretApps(t, root, project, clusterSecretYAML(name, prodServer, ""), "    server: "+prodServer+"\n    namespace: workloads\n")
			},
			left:         "prod",
			right:        "staging",
			wantRendered: []string{"argocd/settings", "manifests/to-prod"},
			wantReached:  1,
		},
		{
			name: "project-scoped cluster Secret moved",
			write: func(t *testing.T, root, project string) {
				t.Helper()
				writeClusterSecretApps(t, root, clusterProjectYAML(false, "    - server: \"*\"\n      namespace: \"*\"\n"), clusterSecretYAML("prod", prodServer, project), "    name: prod\n    namespace: workloads\n")
			},
			left:         "team-a",
			right:        "team-b",
			wantRendered: []string{"argocd/settings", "manifests/bystander", "manifests/to-prod"},
			wantReached:  2,
		},
		{
			name:         "no-effect settings",
			write:        writeSelfManagedSettingsApps,
			left:         trackingAnnotationSettings,
			right:        noEffectSettings,
			wantRendered: []string{"argocd/settings"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			left := filepath.Join(root, "left")
			right := filepath.Join(root, "right")
			tc.write(t, left, tc.left)
			tc.write(t, right, tc.right)

			var mu sync.Mutex
			rendered := map[string]struct{}{}
			o := Orchestrator{}
			o.renderObserver = func(source render.ResolvedSource) {
				mu.Lock()
				defer mu.Unlock()
				rendered[source.Path] = struct{}{}
			}
			result, err := o.DiffApps(context.Background(), DiffRequest{LeftPath: left, RightPath: right, ChangedOnly: true, StrictChangedOnly: true, Unified: 3})
			if err != nil {
				t.Fatalf("DiffApps() error = %v, diagnostics = %#v", err, result.Diagnostics)
			}
			if got := slices.Sorted(maps.Keys(rendered)); !slices.Equal(got, tc.wantRendered) {
				t.Fatalf("changed-only rendered %v, want %v", got, tc.wantRendered)
			}
			var changedOnly []diagnostic.Diagnostic
			for _, diag := range result.Diagnostics {
				if diag.Category == "changed-only" {
					changedOnly = append(changedOnly, diag)
				}
			}
			var want []diagnostic.Diagnostic
			if tc.wantReached > 0 {
				want = []diagnostic.Diagnostic{changedOnlySettingsScopedDiagnostic(tc.wantReached)}
			}
			if !slices.Equal(changedOnly, want) {
				t.Fatalf("changed-only diagnostics = %#v, want %#v", changedOnly, want)
			}
			if err := diagnosticFailure(normalizeDiagnostics(changedOnly, true, false), true); err != nil {
				t.Fatalf("strict diagnosticFailure() = %v, want the scoped selection exempt", err)
			}
		})
	}
}

// A scoped or no-effect settings change does not excuse an unowned changed
// path: --strict-changed-only still fails on it.
func TestOrchestratorDiffAppsChangedOnlyUnownedPathFailsStrictWithSettingsChange(t *testing.T) {
	for _, tc := range []struct {
		name  string
		write func(*testing.T, string, string)
	}{
		{
			name: "repository Secret",
			write: func(t *testing.T, root, value string) {
				t.Helper()
				url := ""
				if value == "new" {
					url = chartsXURL
				}
				writeRepositorySecretApps(t, root, url, map[string]string{"uses-x": chartsXURL})
			},
		},
		{
			name: "no-effect setting",
			write: func(t *testing.T, root, value string) {
				t.Helper()
				settings := trackingAnnotationSettings
				if value == "new" {
					settings = noEffectSettings
				}
				writeSelfManagedSettingsApps(t, root, settings)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			left := filepath.Join(root, "left")
			right := filepath.Join(root, "right")
			for side, value := range map[string]string{left: "old", right: "new"} {
				tc.write(t, side, value)
				writeTestFile(t, filepath.Join(side, "README.md"), value)
			}
			result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{LeftPath: left, RightPath: right, ChangedOnly: true, StrictChangedOnly: true, Unified: 3})
			if err == nil || !strings.Contains(err.Error(), "changed-only input ownership incomplete") {
				t.Fatalf("strict changed-only DiffApps() error = %v, want the unowned README to fail it: %#v", err, result.Diagnostics)
			}
		})
	}
}
