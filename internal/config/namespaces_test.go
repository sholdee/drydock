package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	argoenv "github.com/argoproj/argo-cd/v3/util/env"
)

// TestParseNamespaceListMatchesUpstream compares ParseNamespaceList with the
// Argo CD v3.6.1 env.StringsFromEnv that both controllers use to read
// application.namespaces and applicationsetcontroller.namespaces.
func TestParseNamespaceListMatchesUpstream(t *testing.T) {
	for _, value := range []string{
		"",
		"team-a",
		"team-a,team-b",
		" team-a , team-b ",
		"team-*,/^tenant-[0-9]+$/",
		"team-a,,team-b",
		",",
		" ",
	} {
		t.Setenv("DRYDOCK_TEST_NAMESPACES", value)
		want := argoenv.StringsFromEnv("DRYDOCK_TEST_NAMESPACES", []string{}, ",")
		got := ParseNamespaceList(value)
		if len(got) == 0 && len(want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("ParseNamespaceList(%q) = %q, want %q like env.StringsFromEnv", value, got, want)
		}
	}
}

// TestLoadCommandParametersReadsNamespaceLists pins that drydock reads
// application.namespaces and applicationsetcontroller.namespaces, records
// the ConfigMap and its namespace, and does not report the two keys as
// metadata only.
func TestLoadCommandParametersReadsNamespaceLists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "argocd-cmd-params-cm.yaml")
	if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cmd-params-cm
  namespace: gitops
data:
  application.namespaces: "team-*, /^tenant-[0-9]+$/"
  applicationsetcontroller.namespaces: team-a
`), 0o644); err != nil {
		t.Fatal(err)
	}
	settings, diags, err := LoadCommandParametersConfigMap(path)
	if err != nil {
		t.Fatalf("LoadCommandParametersConfigMap() error = %v", err)
	}
	if !settings.CommandParametersFound || settings.CommandParametersNamespace != "gitops" {
		t.Fatalf("found = %t, namespace = %q, want true and gitops", settings.CommandParametersFound, settings.CommandParametersNamespace)
	}
	if got := settings.ApplicationNamespaces.Value; got != "team-*, /^tenant-[0-9]+$/" {
		t.Fatalf("ApplicationNamespaces = %q", got)
	}
	if got := settings.ApplicationSetNamespaces.Value; got != "team-a" {
		t.Fatalf("ApplicationSetNamespaces = %q", got)
	}
	for _, diag := range diags {
		if strings.Contains(diag.Message, "namespaces") {
			t.Fatalf("diagnostic %q reports a namespace list as metadata only", diag.Message)
		}
	}
	for _, setting := range settings.CommandParameters {
		if setting.Classification != CommandParameterRendered {
			t.Fatalf("%s classification = %q, want rendered", setting.Key, setting.Classification)
		}
	}
}

// TestLoadCommandParametersRecordsEmptyConfigMap pins that a ConfigMap
// without data still counts as found: Argo CD then reconciles only the
// controller namespace.
func TestLoadCommandParametersRecordsEmptyConfigMap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "argocd-cmd-params-cm.yaml")
	if err := os.WriteFile(path, []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: argocd-cmd-params-cm\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	settings, _, err := LoadCommandParametersConfigMap(path)
	if err != nil {
		t.Fatalf("LoadCommandParametersConfigMap() error = %v", err)
	}
	if !settings.CommandParametersFound {
		t.Fatal("CommandParametersFound = false, want true")
	}
	if DefaultSettings().CommandParametersFound {
		t.Fatal("DefaultSettings() must not count as a found ConfigMap")
	}
}

// TestMergeNamespaceListsConflict pins the merge of two namespace lists:
// spellings that split into the same list agree, and different lists
// conflict.
func TestMergeNamespaceListsConflict(t *testing.T) {
	left, _ := commandParametersSettings("left.yaml", "data", map[string]string{applicationNamespacesKey: "team-a,team-b"})
	same, _ := commandParametersSettings("same.yaml", "data", map[string]string{applicationNamespacesKey: " team-a , team-b"})
	other, _ := commandParametersSettings("other.yaml", "data", map[string]string{applicationNamespacesKey: "team-c"})
	if _, diags := MergeDiscovered([]ArgoSettings{left, same}); len(diags) != 0 {
		t.Fatalf("MergeDiscovered() diagnostics = %#v, want none", diags)
	}
	merged, diags := MergeDiscovered([]ArgoSettings{left, other})
	if len(diags) != 1 || !strings.Contains(diags[0].Message, "conflicting application.namespaces") {
		t.Fatalf("MergeDiscovered() diagnostics = %#v, want one conflict", diags)
	}
	if !merged.CommandParametersFound {
		t.Fatal("merged CommandParametersFound = false, want true")
	}
}
