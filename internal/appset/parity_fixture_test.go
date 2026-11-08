package appset

import (
	"bytes"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	corev1 "k8s.io/api/core/v1"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
)

// These tests pin the drydock side of the Argo CD render parity smoke
// fixtures under testdata/argocd-parity. The kind smoke compares the same
// fixtures against a real Argo CD v3.6.1; these tests catch fixture or
// generator drift in a normal go test run.

var parityFixtureRoot = filepath.Join("..", "..", "testdata", "argocd-parity")

func generateParityAppSet(t *testing.T, name string, options Options) ([]GeneratedApplication, []string) {
	t.Helper()
	repoRoot := filepath.Join(parityFixtureRoot, "repo")
	manifestPath := filepath.Join("applicationsets", name)
	data, err := os.ReadFile(filepath.Join(repoRoot, manifestPath))
	if err != nil {
		t.Fatalf("read %s: %v", manifestPath, err)
	}
	apps, diags, err := GenerateFromYAMLWithOptions(repoRoot, manifestPath, data, options)
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions(%s) error = %v", name, err)
	}
	codes := make([]string, 0, len(diags))
	for _, diag := range diags {
		codes = append(codes, diag.Code)
	}
	return apps, codes
}

func parityProviderOptions(t *testing.T) Options {
	t.Helper()
	path := filepath.Join(parityFixtureRoot, "appset-provider-fixture.yaml")
	data, diags, err := LoadProviderFixtures([]string{path})
	if err != nil {
		t.Fatalf("LoadProviderFixtures(%s) error = %v (diagnostics %#v)", path, err, diags)
	}
	return Options{Provider: ProviderOptions{FixturePaths: []string{path}, Data: data}}
}

func TestArgoCDParityFixtureEmptyNameDropsOnlyThatApplication(t *testing.T) {
	apps, codes := generateParityAppSet(t, "validation-empty-name.yaml", Options{})
	if got, want := generatedNames(apps), []string{"parity-empty-name-alpha", "parity-empty-name-beta"}; !slices.Equal(got, want) {
		t.Fatalf("generated names = %#v, want %#v", got, want)
	}
	if want := []string{"appset.application-validation-failed"}; !slices.Equal(codes, want) {
		t.Fatalf("diagnostic codes = %#v, want %#v", codes, want)
	}
}

func TestArgoCDParityFixtureDuplicateNameDropsEverySharedApplication(t *testing.T) {
	apps, codes := generateParityAppSet(t, "validation-duplicate-name.yaml", Options{})
	if got, want := generatedNames(apps), []string{"parity-duplicate-name-unique"}; !slices.Equal(got, want) {
		t.Fatalf("generated names = %#v, want %#v", got, want)
	}
	if want := []string{"appset.application-validation-failed"}; !slices.Equal(codes, want) {
		t.Fatalf("diagnostic codes = %#v, want %#v", codes, want)
	}
}

func TestArgoCDParityFixtureClusterGeneratorHidesReservedAnnotations(t *testing.T) {
	options := parityProviderOptions(t)

	goApps, codes := generateParityAppSet(t, "cluster-annotations.yaml", options)
	if len(codes) != 0 {
		t.Fatalf("Go-template diagnostic codes = %#v, want none", codes)
	}
	wantGo := map[string]string{
		"parity-cluster-alpha": `annotations: {"argocd.argoproj.io/manifest-generate-paths":".","parity.drydock.dev/team":"alpha-team"}`,
		// Every beta annotation is reserved, but the key still exists.
		"parity-cluster-beta": `annotations: {}`,
	}
	assertParityHelmValues(t, goApps, wantGo)

	ftApps, codes := generateParityAppSet(t, "cluster-annotations-fasttemplate.yaml", options)
	if len(codes) != 0 {
		t.Fatalf("fasttemplate diagnostic codes = %#v, want none", codes)
	}
	wantFT := map[string]string{
		"parity-cluster-ft-alpha": strings.Join([]string{
			"team: 'alpha-team'",
			"manifestGeneratePaths: '.'",
			"refresh: '{{metadata.annotations.argocd.argoproj.io/refresh}}'",
			"trackingID: '{{metadata.annotations.argocd.argoproj.io/tracking-id}}'",
			"lastApplied: '{{metadata.annotations.kubectl.kubernetes.io/last-applied-configuration}}'",
			"role: 'primary'",
		}, "\n"),
		"parity-cluster-ft-beta": strings.Join([]string{
			"team: '{{metadata.annotations.parity.drydock.dev/team}}'",
			"refresh: '{{metadata.annotations.argocd.argoproj.io/refresh}}'",
			"lastApplied: '{{metadata.annotations.kubectl.kubernetes.io/last-applied-configuration}}'",
		}, "\n"),
	}
	assertParityHelmValues(t, ftApps, wantFT)
}

// TestArgoCDParityFixtureClusterSprigMapFunctionGeneratesNothing pins the
// smoke fixture that calls hasKey on .metadata.annotations. Argo CD passes
// that map as map[string]string, so hasKey fails and no Application exists.
func TestArgoCDParityFixtureClusterSprigMapFunctionGeneratesNothing(t *testing.T) {
	repoRoot := filepath.Join(parityFixtureRoot, "repo")
	manifestPath := filepath.Join("applicationsets", "cluster-sprig-map-function.yaml")
	data, err := os.ReadFile(filepath.Join(repoRoot, manifestPath))
	if err != nil {
		t.Fatalf("read %s: %v", manifestPath, err)
	}
	apps, diags, err := GenerateFromYAMLWithOptions(repoRoot, manifestPath, data, parityProviderOptions(t))
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
	}
	if len(apps) != 0 {
		t.Fatalf("generated names = %#v, want none", generatedNames(apps))
	}
	// Both clusters have the annotations key, so each failure must be the
	// type error, not a missing key.
	if len(diags) != 2 {
		t.Fatalf("diagnostics = %#v, want one per cluster", diags)
	}
	for _, diag := range diags {
		if code := diagnostic.StableCode(diag); code != "appset.template-render-failed" {
			t.Fatalf("diagnostic code = %q, want appset.template-render-failed", code)
		}
		if !strings.Contains(diag.Message, "got map[string]string") {
			t.Fatalf("diagnostic message = %q, want the hasKey map[string]string type error", diag.Message)
		}
	}
}

// TestArgoCDParityFixtureSelectorListParams pins the smoke fixture whose
// generator selectors read the []string path.segments param. Argo CD
// flattens the list to one "[workloads appset git alpha]" value, so only
// alpha matches the first generator and nothing matches path.segments.3.
func TestArgoCDParityFixtureSelectorListParams(t *testing.T) {
	apps, codes := generateParityAppSet(t, "selector-list-params.yaml", Options{})
	if got, want := generatedNames(apps), []string{"parity-segments-alpha"}; !slices.Equal(got, want) {
		t.Fatalf("generated names = %#v, want %#v", got, want)
	}
	if len(codes) != 0 {
		t.Fatalf("diagnostic codes = %#v, want none", codes)
	}
	if got := apps[0].Application.Spec.GetSource().Path; got != "workloads/appset/git/alpha" {
		t.Fatalf("source path = %q, want workloads/appset/git/alpha", got)
	}
}

// TestArgoCDParityFixtureRollingSync pins the RollingSync smoke fixtures,
// with progressive syncs on as the fixture's argocd-cmd-params-cm sets
// them. Every Application generates except those of the empty steps list.
// The first warning must carry the message that the smoke expects in the
// Argo CD condition (ROLLING_SYNC_CONDITIONS in
// scripts/argocd-parity-smoke.sh); later warnings are the lower-priority
// findings that Argo CD does not show.
func TestArgoCDParityFixtureRollingSync(t *testing.T) {
	tests := []struct {
		file         string
		names        []string
		codes        []string
		firstMessage string
	}{
		{file: "rolling-sync-valid.yaml", names: []string{"parity-rolling-sync-valid-dev", "parity-rolling-sync-valid-prod"}},
		{
			file:         "rolling-sync-operator.yaml",
			names:        []string{"parity-rolling-sync-operator-dev"},
			codes:        []string{diagnostic.CodeAppSetInvalidRolloutConfig, diagnostic.CodeAppSetInvalidRolloutConfig},
			firstMessage: "Step 1 has invalid matchExpression operators: Exists. Supported Operators are 'In' and 'NotIn'",
		},
		{
			file:         "rolling-sync-duplicate.yaml",
			names:        []string{"parity-rolling-sync-duplicate-dev"},
			codes:        []string{diagnostic.CodeAppSetInvalidRolloutConfig},
			firstMessage: "Application 'parity-rolling-sync-duplicate-dev' is selected by multiple steps: (1-2)",
		},
		{
			file:         "rolling-sync-empty-step.yaml",
			names:        []string{"parity-rolling-sync-empty-step-dev"},
			codes:        []string{diagnostic.CodeAppSetInvalidRolloutConfig},
			firstMessage: "Steps [2] have no applications matching their criteria",
		},
		{
			file:         "rolling-sync-max-update.yaml",
			names:        []string{"parity-rolling-sync-max-update-dev"},
			codes:        []string{diagnostic.CodeAppSetInvalidRolloutConfig},
			firstMessage: "Step 1 has an invalid maxUpdate value: 1",
		},
		{
			file:         "rolling-sync-no-steps.yaml",
			codes:        []string{"appset.rollout-no-steps"},
			firstMessage: "No steps defined for rollout",
		},
	}
	repoRoot := filepath.Join(parityFixtureRoot, "repo")
	params, _, err := config.LoadCommandParametersConfigMap(filepath.Join(repoRoot, "settings", "argocd-cmd-params-cm.yaml"))
	if err != nil {
		t.Fatalf("LoadCommandParametersConfigMap() error = %v", err)
	}
	options := Options{ProgressiveSyncs: config.ParseEnableProgressiveSyncs(params.EnableProgressiveSyncs.Value)}
	if !options.ProgressiveSyncs {
		t.Fatal("the fixture argocd-cmd-params-cm must turn progressive syncs on, as the smoke does on the live Argo CD")
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			manifestPath := filepath.Join("applicationsets", tt.file)
			data, err := os.ReadFile(filepath.Join(repoRoot, manifestPath))
			if err != nil {
				t.Fatalf("read %s: %v", manifestPath, err)
			}
			apps, diags, err := GenerateFromYAMLWithOptions(repoRoot, manifestPath, data, options)
			if err != nil {
				t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
			}
			if got := generatedNames(apps); !slices.Equal(got, tt.names) {
				t.Fatalf("generated names = %#v, want %#v", got, tt.names)
			}
			codes := make([]string, 0, len(diags))
			for _, diag := range diags {
				codes = append(codes, diag.Code)
			}
			if !slices.Equal(codes, tt.codes) {
				t.Fatalf("diagnostic codes = %#v, want %#v", codes, tt.codes)
			}
			if tt.firstMessage != "" && !strings.Contains(diags[0].Message, ": "+tt.firstMessage+";") {
				t.Fatalf("first diagnostic message = %q, want it to carry %q", diags[0].Message, tt.firstMessage)
			}
		})
	}
}

// assertParityHelmValues checks that each named Application exists and that
// its helm.values string holds every wanted line.
func assertParityHelmValues(t *testing.T, apps []GeneratedApplication, want map[string]string) {
	t.Helper()
	if got, wantNames := generatedNames(apps), slices.Sorted(maps.Keys(want)); !slices.Equal(slices.Sorted(slices.Values(got)), wantNames) {
		t.Fatalf("generated names = %#v, want %#v", got, wantNames)
	}
	for _, app := range apps {
		source := app.Application.Spec.Source
		if source == nil || source.Helm == nil {
			t.Fatalf("%s has no helm source", app.Application.Name)
		}
		lines := strings.Split(source.Helm.Values, "\n")
		for i := range lines {
			lines[i] = strings.TrimSpace(lines[i])
		}
		for line := range strings.SplitSeq(want[app.Application.Name], "\n") {
			if !slices.Contains(lines, line) {
				t.Fatalf("%s helm.values missing line %q:\n%s", app.Application.Name, line, source.Helm.Values)
			}
		}
	}
}

// TestArgoCDParityProviderFixtureMatchesClusterSecrets keeps the drydock
// provider fixture in step with the cluster Secrets the smoke applies to
// Argo CD. The only allowed difference is the
// kubectl.kubernetes.io/last-applied-configuration annotation: kubectl apply
// writes it on the live Secret, so the fixture adds it by hand.
func TestArgoCDParityProviderFixtureMatchesClusterSecrets(t *testing.T) {
	secrets := readParityClusterSecrets(t)
	options := parityProviderOptions(t)
	clusters := options.Provider.Data.Clusters
	if len(clusters) != len(secrets) {
		t.Fatalf("provider fixture has %d clusters, cluster-secrets has %d Secrets", len(clusters), len(secrets))
	}
	for i, secret := range secrets {
		cluster := clusters[i]
		if got, want := cluster.Name, secret.StringData["name"]; got != want {
			t.Fatalf("cluster %d name = %q, Secret %s name = %q", i, got, secret.Name, want)
		}
		if got, want := cluster.Server, secret.StringData["server"]; got != want {
			t.Fatalf("cluster %s server = %q, Secret %s server = %q", cluster.Name, got, secret.Name, want)
		}
		if got, want := cluster.Project, secret.StringData["project"]; got != want {
			t.Fatalf("cluster %s project = %q, Secret %s project = %q", cluster.Name, got, secret.Name, want)
		}
		if !maps.Equal(cluster.Labels, secret.Labels) {
			t.Fatalf("cluster %s labels = %#v, Secret %s labels = %#v", cluster.Name, cluster.Labels, secret.Name, secret.Labels)
		}
		if _, ok := cluster.Annotations[corev1.LastAppliedConfigAnnotation]; !ok {
			t.Fatalf("cluster %s lacks %s, which kubectl apply writes on Secret %s", cluster.Name, corev1.LastAppliedConfigAnnotation, secret.Name)
		}
		if _, ok := secret.Annotations[corev1.LastAppliedConfigAnnotation]; ok {
			t.Fatalf("Secret %s sets %s by hand; let kubectl apply write it", secret.Name, corev1.LastAppliedConfigAnnotation)
		}
		fixtureAnnotations := maps.Clone(cluster.Annotations)
		delete(fixtureAnnotations, corev1.LastAppliedConfigAnnotation)
		if !maps.Equal(fixtureAnnotations, secret.Annotations) {
			t.Fatalf("cluster %s annotations = %#v, Secret %s annotations = %#v", cluster.Name, fixtureAnnotations, secret.Name, secret.Annotations)
		}
	}
}

func readParityClusterSecrets(t *testing.T) []corev1.Secret {
	t.Helper()
	path := filepath.Join(parityFixtureRoot, "cluster-secrets", "clusters.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	decoder := k8syaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	var secrets []corev1.Secret
	for {
		var secret corev1.Secret
		if err := decoder.Decode(&secret); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("decode %s: %v", path, err)
		}
		if secret.Labels["argocd.argoproj.io/secret-type"] != "cluster" {
			t.Fatalf("Secret %s in %s lacks the argocd.argoproj.io/secret-type=cluster label", secret.Name, path)
		}
		secrets = append(secrets, secret)
	}
	return secrets
}

// TestArgoCDParityFixtureGeneratorSemantics pins the smoke fixtures for
// matrix child selectors and for an entry that sets two generators.
func TestArgoCDParityFixtureGeneratorSemantics(t *testing.T) {
	for _, tc := range []struct {
		file string
		want []string
	}{
		{file: "matrix-child-selector.yaml", want: []string{"parity-matrix-selector-inner-dev"}},
		{file: "generator-two-fields.yaml", want: []string{"parity-two-fields-list", "parity-two-fields-matrix"}},
	} {
		t.Run(tc.file, func(t *testing.T) {
			apps, codes := generateParityAppSet(t, tc.file, Options{})
			got := generatedNames(apps)
			slices.Sort(got)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("generated names = %#v, want %#v", got, tc.want)
			}
			if len(codes) != 0 {
				t.Fatalf("diagnostic codes = %#v, want none", codes)
			}
		})
	}
}

// TestArgoCDParityFixtureGeneratorErrorsFailApplicationSet pins the smoke
// fixtures whose generator fails the whole ApplicationSet in Argo CD. The
// texts match the APPLICATIONSET_ERRORS entries of the smoke.
func TestArgoCDParityFixtureGeneratorErrorsFailApplicationSet(t *testing.T) {
	for _, tc := range []struct {
		file string
		want string
	}{
		{file: "generator-error-elements-yaml.yaml", want: "error unmarshling decoded ElementsYaml"},
		{file: "generator-error-empty-child.yaml", want: "failed to get params for second generator in the matrix generator: child generator generated no parameters"},
		{file: "generator-error-nested-too-deep.yaml", want: "failed to get params for second generator in the matrix generator: child generator returned an error on parameter generation: failed to get params for second generator in the matrix generator: child generator generated no parameters"},
		{file: "generator-error-selector.yaml", want: `error parsing label selector: "Sometimes" is not a valid pod selector operator`},
		{file: "generator-error-merge-keys.yaml", want: `error getting param sets by merge key: the parameters from a generator were not unique by the given mergeKeys, Merge requires all param sets to be unique. Duplicate key was {"suffix":"merged"}`},
		{file: "generator-error-matrix-count.yaml", want: "found less than two generators, Matrix support only two"},
		{file: "generator-error-cluster-selector.yaml", want: `error getting cluster secrets: error converting label selector: values[0][parity.drydock.dev/role]: Invalid value: "https://alpha.parity-clusters.invalid"`},
	} {
		t.Run(tc.file, func(t *testing.T) {
			repoRoot := filepath.Join(parityFixtureRoot, "repo")
			manifestPath := filepath.Join("applicationsets", tc.file)
			data, err := os.ReadFile(filepath.Join(repoRoot, manifestPath))
			if err != nil {
				t.Fatalf("read %s: %v", manifestPath, err)
			}
			apps, diags, err := GenerateFromYAMLWithOptions(repoRoot, manifestPath, data, parityProviderOptions(t))
			requireGeneratorFailed(t, apps, diags, err, tc.want)
		})
	}
}

// TestArgoCDParityFixtureGitGenerator pins the Git generator fixtures: the
// Applications that the kind smoke expects in APPLICATIONS, and the
// appset.generator-failed warning of each ApplicationSet that it lists in
// APPLICATIONSET_ERRORS. The missing AppProject case has its own directory
// (testdata/argocd-parity/git-missing-project).
func TestArgoCDParityFixtureGitGenerator(t *testing.T) {
	for _, tc := range []struct {
		file  string
		names []string
		codes []string
	}{
		{"git-files-pathspec.yaml", []string{"parity-git-ps-a-b", "parity-git-ps-a", "parity-git-ps-c", "parity-git-ps-linked", "parity-git-ps-p-two", "parity-git-ps-p-one"}, []string{}},
		{"git-directories-as-written.yaml", []string{"parity-git-written-alpha", "parity-git-written-visible"}, []string{}},
		{"git-bad-param.yaml", []string{}, []string{diagnostic.CodeAppSetGeneratorFailed}},
		{"git-outside-pattern.yaml", []string{}, []string{diagnostic.CodeAppSetGeneratorFailed}},
		{"git-symlinked-directory.yaml", []string{}, []string{diagnostic.CodeAppSetGeneratorFailed}},
	} {
		t.Run(tc.file, func(t *testing.T) {
			apps, codes := generateParityAppSet(t, tc.file, Options{})
			if got := generatedNames(apps); !slices.Equal(got, tc.names) {
				t.Fatalf("generated names = %#v, want %#v", got, tc.names)
			}
			if !slices.Equal(codes, tc.codes) {
				t.Fatalf("diagnostic codes = %#v, want %#v", codes, tc.codes)
			}
		})
	}
}
