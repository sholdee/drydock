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
