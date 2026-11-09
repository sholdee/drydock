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
	if !settings.CommandParametersFound || settings.ControllerNamespace("") != "gitops" {
		t.Fatalf("found = %t, namespace = %q, want true and gitops", settings.CommandParametersFound, settings.ControllerNamespace(""))
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

// ControllerNamespace decides by the set of namespaces that the
// argocd-cmd-params-cm ConfigMaps name: --argocd-namespace first, then the
// one namespace when they agree, then argocd when they disagree, then the
// fallback, then argocd. The namespace gate applies only when a found
// ConfigMap names the chosen namespace or --argocd-namespace names it.
func TestControllerNamespaceRule(t *testing.T) {
	for _, tc := range []struct {
		name      string
		flag      string
		named     []string
		fallback  string
		want      string
		wantKnown bool
	}{
		{name: "nothing", want: "argocd"},
		{name: "fallback", fallback: "projects", want: "projects"},
		{name: "one namespace", named: []string{"gitops"}, fallback: "projects", want: "gitops", wantKnown: true},
		{name: "disagree with argocd", named: []string{"argocd", "gitops"}, fallback: "projects", want: "argocd", wantKnown: true},
		{name: "disagree without argocd", named: []string{"gitops", "team-a"}, fallback: "projects", want: "argocd"},
		{name: "flag wins", flag: "platform", named: []string{"gitops"}, want: "platform", wantKnown: true},
		{name: "flag alone", flag: "platform", want: "platform", wantKnown: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings := DefaultSettings()
			settings.CommandParametersFound = true
			settings.CommandParametersNamespaces = tc.named
			settings.ArgoCDNamespace = tc.flag
			if got := settings.ControllerNamespace(tc.fallback); got != tc.want {
				t.Fatalf("ControllerNamespace(%q) = %q, want %q", tc.fallback, got, tc.want)
			}
			if got := settings.ControllerNamespaceKnown(); got != tc.wantKnown {
				t.Fatalf("ControllerNamespaceKnown() = %t, want %t", got, tc.wantKnown)
			}
		})
	}
}

// MergeDiscovered keeps every namespace that the candidates name, so the
// order of the candidates cannot decide the controller namespace.
func TestMergeDiscoveredKeepsEveryCommandParametersNamespace(t *testing.T) {
	hub := DefaultSettings()
	hub.CommandParametersFound = true
	hub.CommandParametersNamespace = "gitops"
	spoke := DefaultSettings()
	spoke.CommandParametersFound = true
	spoke.CommandParametersNamespace = "argocd"
	for _, order := range [][]ArgoSettings{{hub, spoke}, {spoke, hub}} {
		merged, _ := MergeDiscovered(order)
		if got := strings.Join(merged.NamedControllerNamespaces(), ","); got != "argocd,gitops" {
			t.Fatalf("NamedControllerNamespaces() = %q, want argocd,gitops", got)
		}
		if got := merged.ControllerNamespace(""); got != "argocd" {
			t.Fatalf("ControllerNamespace() = %q, want argocd", got)
		}
	}
}
