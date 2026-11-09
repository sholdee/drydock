package app

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	sourcepkg "github.com/sholdee/drydock/internal/source"
)

const tenantParamsValues = "configs:\n  params:\n    application.namespaces: \"\"\n"

// writeDemoApplication writes the Application demo in namespace, or
// without a namespace when namespace is empty.
func writeDemoApplication(t *testing.T, root, file, namespace string) {
	t.Helper()
	app := kustomizeApplicationYAML("demo", namespace, "demo", "demo")
	if namespace == "" {
		app = strings.Replace(app, "  namespace: \n", "", 1)
	}
	writeTestFile(t, filepath.Join(root, file), app)
	writeTestFile(t, filepath.Join(root, "demo", "kustomization.yaml"), "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources: [cm.yaml]\n")
	writeTestFile(t, filepath.Join(root, "demo", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\n")
}

// assertTenantIgnored checks that drydock keeps argocd, keeps every
// Application, names team-a as another install, and reads no tenant
// settings, under --strict.
func assertTenantIgnored(t *testing.T, result BuildResult, wantApps ...string) {
	t.Helper()
	if result.Settings.ControllerNamespace("") != defaultArgoCDControllerNamespace {
		t.Fatalf("ControllerNamespace = %q, want argocd", result.Settings.ControllerNamespace(""))
	}
	names := applicationNames(result.Applications)
	for _, want := range wantApps {
		if !slices.Contains(names, want) {
			t.Fatalf("Applications = %v, want %s", names, want)
		}
	}
	if got := result.Settings.TrackingMethod.Value; got == "label" {
		t.Fatalf("TrackingMethod = label from %q, want no tenant settings", result.Settings.TrackingMethod.Provenance.Path)
	}
	if gated := diagnosticsWithCode(result.Diagnostics, namespaceNotEnabledCode); len(gated) != 0 {
		t.Fatalf("gated = %#v, want none", gated)
	}
	warnings := diagnosticsWithCode(result.Diagnostics, controllerNamespaceAmbiguousCode)
	if len(warnings) != 1 || !strings.Contains(warnings[0].Message, "other Argo CD installs in team-a") {
		t.Fatalf("namespace warnings = %#v, want one that names team-a as another install", controllerNamespaceWarnings(result.Diagnostics))
	}
}

// A namespace-less Application that root, in argocd, renders into argocd
// lives in argocd (controller/state.go:588-590). The tenant Argo CD that it
// installs in team-a does not reconcile it, so it is another install.
func TestNamespaceLessChildInstallLivesWhereItsParentPutsIt(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "root.yaml"), kustomizeApplicationYAML("root", "argocd", "children", "argocd"))
	writeTestFile(t, filepath.Join(root, "children", "tenant.yaml"), strings.Replace(kustomizeApplicationYAML("tenant", "", "tenant", "team-a"), "  namespace: \n", "", 1))
	writeDemoApplication(t, root, "children/demo.yaml", "")
	kustomizeArgoCDInstallIn(t, root, "tenant", "team-a", "configs:\n  cm:\n    application.resourceTrackingMethod: label\n  params:\n    application.namespaces: \"\"\n")

	result, err := Orchestrator{ChartAcquirer: fakeArgoCDChartAcquirer(t)}.ListApplications(context.Background(), BuildRequest{Path: root, Parallelism: 1, Strict: true})
	if err != nil {
		t.Fatalf("ListApplications(--strict) error = %v\nDiagnostics: %#v", err, result.Diagnostics)
	}
	assertTenantIgnored(t, result, "root", "tenant", "demo")
}

// writePlainKustomizeTenant writes, at dir, a plain kustomization in
// team-a with an argocd-cm and an argocd-cmd-params-cm. withNamespace
// writes the namespace into the files too; otherwise only the
// kustomization sets it.
func writePlainKustomizeTenant(t *testing.T, root, dir string, withNamespace bool) {
	t.Helper()
	namespace := ""
	if withNamespace {
		namespace = "  namespace: team-a\n"
	}
	writeTestFile(t, filepath.Join(root, dir, "kustomization.yaml"), "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nnamespace: team-a\nresources: [argocd-cm.yaml, argocd-cmd-params-cm.yaml]\n")
	writeTestFile(t, filepath.Join(root, dir, "argocd-cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: argocd-cm\n"+namespace+"data:\n  application.resourceTrackingMethod: label\n")
	writeTestFile(t, filepath.Join(root, dir, "argocd-cmd-params-cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: argocd-cmd-params-cm\n"+namespace+"data:\n  application.namespaces: \"\"\n")
}

// A plain Kustomize tenant install in team-a, whose application.namespaces
// does not enable argocd, is another install whether its files name
// team-a, only the kustomization does, or another repository holds them.
// --argocd-namespace argocd then passes --strict too.
func TestPlainKustomizeTenantInstallIsAnotherInstall(t *testing.T) {
	for _, tc := range []struct {
		name          string
		withNamespace bool
		mapped        bool
	}{
		{name: "files name team-a", withNamespace: true},
		{name: "kustomization names team-a"},
		{name: "another repository", mapped: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			request := BuildRequest{Path: root, Parallelism: 1, Strict: true}
			tenant := kustomizeApplicationYAML("tenant", "argocd", "tenant", "team-a")
			if tc.mapped {
				platform := t.TempDir()
				writePlainKustomizeTenant(t, platform, "tenant", tc.withNamespace)
				tenant = strings.Replace(tenant, "https://github.com/example/repo", "https://github.com/example/platform", 1)
				request.RepoMaps = []sourcepkg.RepoMap{{URL: "https://github.com/example/platform", Path: platform}}
			} else {
				writePlainKustomizeTenant(t, root, "tenant", tc.withNamespace)
			}
			writeTestFile(t, filepath.Join(root, "apps", "tenant.yaml"), tenant)
			writeDemoApplication(t, root, "apps/demo.yaml", "argocd")

			result, err := Orchestrator{}.ListApplications(context.Background(), request)
			if err != nil {
				t.Fatalf("ListApplications(--strict) error = %v\nDiagnostics: %#v", err, result.Diagnostics)
			}
			assertTenantIgnored(t, result, "tenant", "demo")

			request.ArgoCDNamespace = defaultArgoCDControllerNamespace
			if result, err := (Orchestrator{}).ListApplications(context.Background(), request); err != nil {
				t.Fatalf("ListApplications(--strict --argocd-namespace argocd) error = %v\nDiagnostics: %#v", err, result.Diagnostics)
			}
		})
	}
}

// writeUnsettledRepo writes root in gitops. With the instance name
// gitops_root, which the default namespace gives, its chart renders an
// Application that installs Argo CD in gitops. With the instance name
// root, which gitops gives, it renders child-ca instead, and nothing names
// gitops any more.
func writeUnsettledRepo(t *testing.T, root string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", "root.yaml"), kustomizeApplicationYAML("root", controllerNamespaceTestNamespace, "root", controllerNamespaceTestNamespace))
	writeTestFile(t, filepath.Join(root, "root", "Chart.yaml"), "apiVersion: v2\nname: root\nversion: 0.1.0\n")
	writeTestFile(t, filepath.Join(root, "root", "values.yaml"), "child: cb\n")
	writeTestFile(t, filepath.Join(root, "root", ".argocd-source-root.yaml"), "helm:\n  parameters:\n    - name: child\n      value: ca\n")
	writeTestFile(t, filepath.Join(root, "root", ".argocd-source-"+controllerNamespaceTestNamespace+"_root.yaml"), "helm:\n  parameters:\n    - name: child\n      value: install\n")
	writeTestFile(t, filepath.Join(root, "root", "templates", "apps.yaml"), kustomizeApplicationYAML("child-{{ .Values.child }}", controllerNamespaceTestNamespace, "{{ .Values.child }}", controllerNamespaceTestNamespace))
	kustomizeArgoCDInstallIn(t, root, "install", controllerNamespaceTestNamespace, tenantParamsValues)
	for _, child := range []string{"ca", "cb"} {
		writeTestFile(t, filepath.Join(root, child, "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: "+child+"\n")
	}
}

// When the namespace does not settle, the final render uses the namespace
// that the second discovery run used, so the output agrees with the
// warning.
func TestUnsettledControllerNamespaceKeepsTheNamespaceOfTheSecondRun(t *testing.T) {
	root := t.TempDir()
	writeUnsettledRepo(t, root)

	result, err := Orchestrator{ChartAcquirer: fakeArgoCDChartAcquirer(t)}.Build(context.Background(), BuildRequest{Path: root, Parallelism: 1})
	if err != nil {
		t.Fatalf("Build() error = %v\nDiagnostics: %#v", err, result.Diagnostics)
	}
	if got := diagnosticsWithCode(result.Diagnostics, controllerNamespaceUnsettledCode); len(got) != 1 {
		t.Fatalf("unsettled = %#v, want one", got)
	}
	if got := result.Settings.ControllerNamespace(""); got != controllerNamespaceTestNamespace {
		t.Fatalf("ControllerNamespace = %q, want gitops from the second run", got)
	}
	names := applicationNames(result.Applications)
	if !slices.Contains(names, "child-ca") || slices.Contains(names, "child-install") {
		t.Fatalf("Applications = %v, want child-ca from the second run", names)
	}
	application := controllerNamespaceApplication(t, result.Applications, "child-ca")
	cm := controllerNamespaceManifest(t, result.Manifests, "ca")
	if got, want := cm.GetAnnotations()["argocd.argoproj.io/tracking-id"], upstreamTrackingID(t, application, controllerNamespaceTestNamespace, cm); got != want {
		t.Fatalf("tracking-id = %q, want %q in gitops", got, want)
	}
}

// A top-level Helm install in gitops renders its installs again in later
// settle rounds with gitops, so discovery runs once.
func TestTopLevelInstallOutsideArgoCDRunsDiscoveryOnce(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "argocd.yaml"), kustomizeApplicationYAML("argocd", controllerNamespaceTestNamespace, "install", controllerNamespaceTestNamespace))
	kustomizeArgoCDInstallIn(t, root, "install", controllerNamespaceTestNamespace, tenantParamsValues)
	writeDemoApplication(t, root, "apps/demo.yaml", controllerNamespaceTestNamespace)
	runs := 0
	result, err := Orchestrator{ChartAcquirer: fakeArgoCDChartAcquirer(t), discoveryRunObserver: func() { runs++ }}.ListApplications(context.Background(), BuildRequest{Path: root, Parallelism: 1, Strict: true})
	if err != nil {
		t.Fatalf("ListApplications() error = %v\nDiagnostics: %#v", err, result.Diagnostics)
	}
	if got := result.Settings.ControllerNamespace(""); got != controllerNamespaceTestNamespace {
		t.Fatalf("ControllerNamespace = %q, want gitops", got)
	}
	if runs != 1 {
		t.Fatalf("discovery runs = %d, want 1", runs)
	}
}

// The second run for a late namespace starts with the ApplicationSet
// generation values of the first, so it does not restart for them again.
func TestLateNamespaceRunKeepsTheApplicationSetGeneration(t *testing.T) {
	root := t.TempDir()
	writeBootstrapExecPluginPolicy(t, root, "bootstrap", "PklProject")
	writeTestFile(t, filepath.Join(root, "bootstrap", "PklProject"), "")
	writeTestFile(t, filepath.Join(root, "apps", "argocd.yaml"), kustomizeApplicationYAML("argocd", controllerNamespaceTestNamespace, "install", controllerNamespaceTestNamespace))
	kustomizeArgoCDInstallIn(t, root, "install", controllerNamespaceTestNamespace, "configs:\n  params:\n    application.namespaces: \"\"\n    applicationsetcontroller.enable.new.git.file.globbing: \"true\"\n")
	writeTestFile(t, filepath.Join(root, "apps", "gen.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: gen
  namespace: `+controllerNamespaceTestNamespace+`
spec:
  goTemplate: true
  generators:
    - git:
        repoURL: https://github.com/example/repo
        revision: main
        files:
          - path: "cfg/**/config.json"
  template:
    metadata:
      name: 'gen-{{.name}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        targetRevision: main
        path: demo
      destination:
        name: in-cluster
        namespace: '{{.name}}'
`)
	writeTestFile(t, filepath.Join(root, "cfg", "a", "b", "config.json"), `{"name": "deep"}`)
	writeDemoApplication(t, root, "apps/demo.yaml", controllerNamespaceTestNamespace)
	gitCommitAll(t, root, "initial")
	runs := 0
	orchestrator := Orchestrator{PluginExecRunner: &recordingExecRunner{}, ChartAcquirer: fakeArgoCDChartAcquirer(t), discoveryRunObserver: func() { runs++ }}
	result, err := orchestrator.ListApplications(context.Background(), BuildRequest{Path: root, Parallelism: 1, PluginOptions: trustedBootstrapPluginOptions(t, root)})
	if err != nil {
		t.Fatalf("ListApplications() error = %v\nDiagnostics: %#v", err, result.Diagnostics)
	}
	if !slices.Contains(applicationNames(result.Applications), "gen-deep") {
		t.Fatalf("Applications = %v, want gen-deep from the new Git file globbing", applicationNames(result.Applications))
	}
	// One run, one restart for the ApplicationSet generation values, and
	// one run for the namespace that the bootstrap entrypoint used.
	if runs != 3 {
		t.Fatalf("discovery runs = %d, want 3", runs)
	}
}
