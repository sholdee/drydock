package app

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/argoproj/argo-cd/v3/util/glob"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/discovery"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestNamespaceGateMatchesUpstreamControllers compares the gate with the
// checks the Argo CD v3.6.1 controllers run: isAppNamespaceAllowed
// (controller/appcontroller.go:2852-2854) for Applications, and the
// ApplicationSet controller's list with its own namespace added
// (applicationset_controller.go:106 and :676-680), both through
// glob.MatchStringInList with glob.REGEXP.
func TestNamespaceGateMatchesUpstreamControllers(t *testing.T) {
	lists := []string{"", "team-a", "team-*", "/^tenant-[0-9]+$/", "team-a, other", "*"}
	namespaces := []string{"argocd", "gitops", "team-a", "team-b", "tenant-1", "tenant-x", "other"}
	for _, controllerNamespace := range []string{"", "gitops"} {
		for _, list := range lists {
			settings := config.DefaultSettings()
			settings.CommandParametersFound = true
			settings.CommandParametersNamespace = controllerNamespace
			settings.ApplicationNamespaces = config.Value[string]{Value: list}
			settings.ApplicationSetNamespaces = config.Value[string]{Value: list}
			gate := namespaceGateFromSettings(settings)
			upstreamNamespace := controllerNamespace
			if upstreamNamespace == "" {
				upstreamNamespace = "argocd"
			}
			parsed := config.ParseNamespaceList(list)
			for _, namespace := range namespaces {
				wantApp := namespace == upstreamNamespace || glob.MatchStringInList(parsed, namespace, glob.REGEXP)
				if got := gate.applicationAllowed(namespace); got != wantApp {
					t.Fatalf("controller %q list %q: applicationAllowed(%q) = %t, want %t", upstreamNamespace, list, namespace, got, wantApp)
				}
				wantAppSet := glob.MatchStringInList(append(slices.Clone(parsed), upstreamNamespace), namespace, glob.REGEXP)
				if got := gate.applicationSetAllowed(namespace); got != wantAppSet {
					t.Fatalf("controller %q list %q: applicationSetAllowed(%q) = %t, want %t", upstreamNamespace, list, namespace, got, wantAppSet)
				}
			}
		}
	}
	inactive := namespaceGateFromSettings(config.DefaultSettings())
	if !inactive.applicationAllowed("team-b") || !inactive.applicationSetAllowed("team-b") {
		t.Fatal("the gate must allow every namespace when no argocd-cmd-params-cm was read")
	}
}

// TestPruneNotEnabledNamespacesRemovesWhatArgoCDNeverCreates pins the
// final gate: an Application outside the enabled namespaces goes with
// everything it renders, and an ApplicationSet outside them goes with the
// Applications it generated before the settings were known.
func TestPruneNotEnabledNamespacesRemovesWhatArgoCDNeverCreates(t *testing.T) {
	app := func(namespace, name, parentKey, path string) discovery.ApplicationFile {
		return discovery.ApplicationFile{Path: path, Application: argoappv1.Application{Namespace: namespace, Name: name}, ParentKey: parentKey}
	}
	disabledParent := app("team-b", "parent", "", "apps/parent.yaml")
	discovered := discovery.Result{
		Applications: []discovery.ApplicationFile{
			app("argocd", "root", "", "apps/root.yaml"),
			app("team-a", "enabled", "", "apps/enabled.yaml"),
			app("", "no-namespace", "", "apps/no-namespace.yaml"),
			disabledParent,
			app("argocd", "child", applicationKey(disabledParent.Application), "rendered/child.yaml"),
			app("argocd", "grandchild", applicationKey(argoappv1.Application{Namespace: "argocd", Name: "child"}), "rendered/grandchild.yaml"),
			app("team-a", "generated", "", "appsets/team-a.yaml"),
			app("argocd", "kept-child", applicationKey(argoappv1.Application{Namespace: "argocd", Name: "root"}), "rendered/kept.yaml"),
		},
		ApplicationSets: []discovery.ApplicationSetFile{
			{Path: "appsets/team-a.yaml", ApplicationSet: argoappv1.ApplicationSet{ObjectMeta: objectMeta("team-a", "team-a-set")}},
			{Path: "appsets/argocd.yaml", ApplicationSet: argoappv1.ApplicationSet{ObjectMeta: objectMeta("argocd", "argocd-set")}},
			{Path: "rendered/set.yaml", ParentKey: applicationKey(disabledParent.Application), ApplicationSet: argoappv1.ApplicationSet{ObjectMeta: objectMeta("argocd", "rendered-set")}},
		},
	}
	settings := config.DefaultSettings()
	settings.CommandParametersFound = true
	settings.ApplicationNamespaces = config.Value[string]{Value: "team-a"}
	pruned, diags := pruneNotEnabledNamespaces(discovered, namespaceGateFromSettings(settings))
	names := make([]string, 0, len(pruned.Applications))
	for _, appFile := range pruned.Applications {
		names = append(names, appFile.Application.Name)
	}
	if got, want := strings.Join(names, ","), "root,enabled,no-namespace,kept-child"; got != want {
		t.Fatalf("Applications = %s, want %s", got, want)
	}
	appSetNames := make([]string, 0, len(pruned.ApplicationSets))
	for _, appSetFile := range pruned.ApplicationSets {
		appSetNames = append(appSetNames, appSetFile.ApplicationSet.Name)
	}
	if got, want := strings.Join(appSetNames, ","), "argocd-set"; got != want {
		t.Fatalf("ApplicationSets = %s, want %s", got, want)
	}
	messages := make([]string, 0, len(diags))
	for _, diag := range diags {
		if diag.Code != namespaceNotEnabledCode {
			t.Fatalf("diagnostic code = %q, want %q", diag.Code, namespaceNotEnabledCode)
		}
		messages = append(messages, diag.Message)
	}
	if len(messages) != 2 || !strings.Contains(messages[0], "ApplicationSet team-a/team-a-set is skipped") || !strings.Contains(messages[1], "Application team-b/parent is skipped") {
		t.Fatalf("diagnostics = %q, want one for the ApplicationSet and one for the Application", messages)
	}
}

const namespaceGateApplicationTemplate = `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: NAME
  namespace: NAMESPACE
spec:
  project: default
  source:
    repoURL: https://example.test/repo.git
    targetRevision: HEAD
    path: workloads/demo
  destination:
    server: https://kubernetes.default.svc
    namespace: demo
`

const namespaceGateApplicationSet = `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: tenant-set
  namespace: team-a
spec:
  goTemplate: true
  generators:
    - list:
        elements:
          - name: generated
  template:
    metadata:
      name: '{{.name}}'
    spec:
      project: default
      source:
        repoURL: https://example.test/repo.git
        targetRevision: HEAD
        path: workloads/demo
      destination:
        server: https://kubernetes.default.svc
        namespace: demo
`

func writeNamespaceGateRepo(t *testing.T, root, cmdParams string) {
	t.Helper()
	for _, item := range [][2]string{{"argocd", "in-controller"}, {"team-a", "enabled"}, {"team-b", "disabled"}} {
		manifest := strings.NewReplacer("NAMESPACE", item[0], "NAME", item[1]).Replace(namespaceGateApplicationTemplate)
		writeTestFile(t, filepath.Join(root, "apps", item[1]+".yaml"), manifest)
	}
	writeTestFile(t, filepath.Join(root, "appsets", "tenant-set.yaml"), namespaceGateApplicationSet)
	writeTestFile(t, filepath.Join(root, "workloads", "demo", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\n")
	if cmdParams != "" {
		writeTestFile(t, filepath.Join(root, "settings", "argocd-cmd-params-cm.yaml"), cmdParams)
	}
}

func listedApplications(t *testing.T, root string) ([]string, []string) {
	t.Helper()
	result, err := Orchestrator{}.ListApplications(context.Background(), specValidationBuildRequest(root))
	if err != nil {
		t.Fatalf("ListApplications() error = %v", err)
	}
	names := make([]string, 0, len(result.Applications))
	var warnings []string
	for _, application := range result.Applications {
		names = append(names, application.Namespace+"/"+application.Name)
	}
	for _, diag := range result.Diagnostics {
		if diag.Code == namespaceNotEnabledCode {
			warnings = append(warnings, diag.Message)
		}
	}
	slices.Sort(names)
	return names, warnings
}

// TestListApplicationsSkipsNamespacesArgoCDDoesNotReconcile pins discovery
// with the argocd-cmd-params-cm namespace lists. Argo CD reconciles
// team-a Applications through application.namespaces, never reconciles
// team-b, and never runs the team-a ApplicationSet, because
// applicationsetcontroller.namespaces does not list team-a.
func TestListApplicationsSkipsNamespacesArgoCDDoesNotReconcile(t *testing.T) {
	root := t.TempDir()
	writeNamespaceGateRepo(t, root, `apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cmd-params-cm
  namespace: argocd
data:
  application.namespaces: team-a
`)
	names, warnings := listedApplications(t, root)
	if got, want := strings.Join(names, ","), "argocd/in-controller,team-a/enabled"; got != want {
		t.Fatalf("Applications = %s, want %s", got, want)
	}
	if len(warnings) != 2 {
		t.Fatalf("namespace warnings = %q, want one for team-b/disabled and one for team-a/tenant-set", warnings)
	}

	writeTestFile(t, filepath.Join(root, "settings", "argocd-cmd-params-cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cmd-params-cm
  namespace: argocd
data:
  application.namespaces: team-*
  applicationsetcontroller.namespaces: /^team-[a-z]$/
`)
	names, warnings = listedApplications(t, root)
	if got, want := strings.Join(names, ","), "argocd/in-controller,team-a/enabled,team-a/generated,team-b/disabled"; got != want {
		t.Fatalf("Applications = %s, want %s", got, want)
	}
	if len(warnings) != 0 {
		t.Fatalf("namespace warnings = %q, want none", warnings)
	}
}

// TestListApplicationsKeepsEveryNamespaceWithoutCommandParameters pins that
// drydock keeps every Application when the repository holds no
// argocd-cmd-params-cm: it cannot tell how the cluster sets the lists.
func TestListApplicationsKeepsEveryNamespaceWithoutCommandParameters(t *testing.T) {
	root := t.TempDir()
	writeNamespaceGateRepo(t, root, "")
	names, warnings := listedApplications(t, root)
	if got, want := strings.Join(names, ","), "argocd/in-controller,team-a/enabled,team-a/generated,team-b/disabled"; got != want {
		t.Fatalf("Applications = %s, want %s", got, want)
	}
	if len(warnings) != 0 {
		t.Fatalf("namespace warnings = %q, want none", warnings)
	}
}

func objectMeta(namespace, name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Namespace: namespace, Name: name}
}
