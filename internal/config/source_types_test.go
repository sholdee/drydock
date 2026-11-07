package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	argosettings "github.com/argoproj/argo-cd/v3/util/settings"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// upstreamEnabledSourceTypes reads argocd-cm data through Argo CD's own
// SettingsManager.GetEnabledSourceTypes (util/settings/settings.go at
// v3.6.1), backed by a fake clientset.
func upstreamEnabledSourceTypes(t *testing.T, data map[string]string) map[string]bool {
	t.Helper()
	cm := &corev1.ConfigMap{Data: data}
	cm.Name = "argocd-cm"
	cm.Namespace = "argocd"
	cm.Labels = map[string]string{"app.kubernetes.io/part-of": "argocd"}
	manager := argosettings.NewSettingsManager(t.Context(), fake.NewClientset(cm), "argocd")
	enabled, err := manager.GetEnabledSourceTypes()
	if err != nil {
		t.Fatalf("GetEnabledSourceTypes() error = %v", err)
	}
	return enabled
}

// The argocd-cm source type keys turn a tool on or off as Argo CD reads
// them: only the exact string "true" keeps a tool on, and an empty value is
// the default.
func TestSourceTypeEnabledMatchesArgoCD(t *testing.T) {
	keyType := map[string]argoappv1.ApplicationSourceType{
		HelmEnableKey:      argoappv1.ApplicationSourceTypeHelm,
		KustomizeEnableKey: argoappv1.ApplicationSourceTypeKustomize,
		JsonnetEnableKey:   argoappv1.ApplicationSourceTypeDirectory,
	}
	for _, value := range []string{"", "true", "false", "True", "TRUE", "1", "yes", " true"} {
		for key, sourceType := range keyType {
			data := map[string]string{key: value}
			want := upstreamEnabledSourceTypes(t, data)[string(sourceType)]

			dir := t.TempDir()
			path := filepath.Join(dir, "argocd-cm.yaml")
			var body strings.Builder
			body.WriteString("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: argocd-cm\ndata:\n  " + key + ": ")
			body.WriteString(strconvQuote(value) + "\n")
			if err := os.WriteFile(path, []byte(body.String()), 0o600); err != nil {
				t.Fatal(err)
			}
			settings, diags, err := LoadFromConfigMap(path)
			if err != nil || len(diags) != 0 {
				t.Fatalf("LoadFromConfigMap(%s=%q) = %v, %v", key, value, diags, err)
			}
			if got := settings.SourceTypeEnabled(key); got != want {
				t.Errorf("SourceTypeEnabled(%s=%q) = %v, Argo CD = %v", key, value, got, want)
			}
			if got := slices.Contains(settings.DisabledSourceTypeKeys(), key); got != !want {
				t.Errorf("DisabledSourceTypeKeys(%s=%q) has key = %v, want %v", key, value, got, !want)
			}
		}
	}
	if keys := DefaultSettings().DisabledSourceTypeKeys(); len(keys) != 0 {
		t.Fatalf("DefaultSettings().DisabledSourceTypeKeys() = %v, want none", keys)
	}
}

func strconvQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

// The keys also come from the configs.cm of argo-cd Helm values, and two
// settings sources that disagree on a key conflict, unless Argo CD reads
// both values the same.
func TestSourceTypeEnableFromHelmValuesAndMerge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "values.yaml")
	if err := os.WriteFile(path, []byte("configs:\n  cm:\n    helm.enable: \"false\"\n    jsonnet.enable: \"true\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	settings, diags, err := LoadFromHelmValues(path)
	if err != nil || len(diags) != 0 {
		t.Fatalf("LoadFromHelmValues() = %v, %v", diags, err)
	}
	if got := settings.DisabledSourceTypeKeys(); !slices.Equal(got, []string{HelmEnableKey}) {
		t.Fatalf("DisabledSourceTypeKeys() = %v, want [%s]", got, HelmEnableKey)
	}

	same := ArgoSettings{SourceTypeEnable: map[string]Value[string]{JsonnetEnableKey: {Value: "", Provenance: Provenance{Path: "b.yaml"}}}}
	if _, diags := MergeDiscovered([]ArgoSettings{settings, same}); len(diags) != 0 {
		t.Fatalf("MergeDiscovered() of \"true\" and \"\" diagnostics = %v, want none", diags)
	}
	conflict := ArgoSettings{SourceTypeEnable: map[string]Value[string]{HelmEnableKey: {Value: "true", Provenance: Provenance{Path: "c.yaml"}}}}
	merged, diags := MergeDiscovered([]ArgoSettings{settings, conflict})
	if len(diags) != 1 || !strings.Contains(diags[0].Message, `conflicting helm.enable values "false" and "true"`) {
		t.Fatalf("MergeDiscovered() diagnostics = %v, want one helm.enable conflict", diags)
	}
	if merged.SourceTypeEnabled(HelmEnableKey) {
		t.Fatal("MergeDiscovered() kept the later conflicting helm.enable value, want the first")
	}
}
