package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// flagSettingsResult lists the Applications of root with --argocd-namespace
// namespace and --strict.
func flagSettingsResult(t *testing.T, root, namespace string) BuildResult {
	t.Helper()
	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root, Parallelism: 1, Strict: true, ArgoCDNamespace: namespace})
	if err != nil {
		t.Fatalf("Build(--strict --argocd-namespace %s) error = %v\nDiagnostics: %#v", namespace, err, result.Diagnostics)
	}
	return result
}

func writeSettingsKustomization(t *testing.T, root, dir, namespace string, resources ...string) {
	t.Helper()
	body := "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\n"
	if namespace != "" {
		body += "namespace: " + namespace + "\n"
	}
	writeTestFile(t, filepath.Join(root, dir, "kustomization.yaml"), body+"resources: ["+strings.Join(resources, ", ")+"]\n")
}

const enableHelmArgoCDConfigMap = "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: argocd-cm\n%sdata:\n  kustomize.buildOptions: --enable-helm\n"

// A base that two overlays render into argocd and argocd-staging lands in
// both. With --argocd-namespace argocd-staging, its ConfigMaps stay.
func TestCommittedSettingsLandInEveryOverlayNamespace(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "prod.yaml"), kustomizeApplicationYAML("install-prod", "argocd", "overlays/prod", "argocd"))
	writeTestFile(t, filepath.Join(root, "apps", "staging.yaml"), kustomizeApplicationYAML("install-staging", "argocd-staging", "overlays/staging", "argocd-staging"))
	writeDemoApplication(t, root, "apps/demo.yaml", "argocd-staging")
	writeSettingsKustomization(t, root, "base", "", "argocd-cm.yaml", "argocd-cmd-params-cm.yaml")
	writeTestFile(t, filepath.Join(root, "base", "argocd-cm.yaml"), strings.Replace(enableHelmArgoCDConfigMap, "%s", "  namespace: argocd\n", 1))
	writeTestFile(t, filepath.Join(root, "base", "argocd-cmd-params-cm.yaml"), commandParametersConfigMapYAML("argocd", "  application.namespaces: \"\"\n"))
	writeSettingsKustomization(t, root, "overlays/prod", "argocd", "../../base")
	writeSettingsKustomization(t, root, "overlays/staging", "argocd-staging", "../../base")

	result := flagSettingsResult(t, root, "argocd-staging")
	if got := diagnosticsWithCode(result.Diagnostics, outsideControllerNamespaceCode); len(got) != 0 {
		t.Fatalf("skipped = %#v, want none: the base lands in argocd-staging too", got)
	}
	if !result.Settings.ArgoCDConfigMapFound || !result.Settings.CommandParametersFound {
		t.Fatalf("settings found = %t/%t, want the base ConfigMaps", result.Settings.ArgoCDConfigMapFound, result.Settings.CommandParametersFound)
	}
}

// A namespace-less ConfigMap that one Application renders into its
// destination argocd, and an overlay into tenant, lands in both. With
// --argocd-namespace argocd, it stays.
func TestNamespaceLessCommittedSettingsLandInTheDestination(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "settings.yaml"), kustomizeApplicationYAML("settings", "argocd", "shared", "argocd"))
	writeTestFile(t, filepath.Join(root, "apps", "tenant.yaml"), kustomizeApplicationYAML("tenant", "argocd", "tenant", "tenant"))
	writeDemoApplication(t, root, "apps/demo.yaml", "argocd")
	writeSettingsKustomization(t, root, "shared", "", "cm.yaml")
	writeTestFile(t, filepath.Join(root, "shared", "cm.yaml"), strings.Replace(enableHelmArgoCDConfigMap, "%s", "", 1))
	writeSettingsKustomization(t, root, "tenant", "tenant", "../shared")

	result := flagSettingsResult(t, root, "argocd")
	if got := diagnosticsWithCode(result.Diagnostics, outsideControllerNamespaceCode); len(got) != 0 {
		t.Fatalf("skipped = %#v, want none: the ConfigMap lands in argocd too", got)
	}
	if !result.Settings.ArgoCDConfigMapFound {
		t.Fatalf("ArgoCDConfigMapFound = false, want the shared argocd-cm")
	}
}

// A skipped argocd-cm in another namespace that turns Kustomize off never
// changes how drydock tells where the real settings land, so the result
// is the one without it, apart from one skip warning.
func TestSkippedSettingsDoNotChangeWhereSettingsLand(t *testing.T) {
	for _, withTenant := range []bool{false, true} {
		root := t.TempDir()
		writeKustomizedInstallRepo(t, root)
		writeSettingsKustomization(t, root, "install", "gitops", "argocd-cm.yaml", "argocd-cmd-params-cm.yaml", "project.yaml")
		writeTestFile(t, filepath.Join(root, "install", "argocd-cm.yaml"), strings.Replace(enableHelmArgoCDConfigMap, "%s", "  namespace: argocd\n", 1))
		if withTenant {
			writeTestFile(t, filepath.Join(root, "tenant", "argocd-cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: argocd-cm\n  namespace: tenant-a\ndata:\n  kustomize.enable: \"false\"\n")
		}
		result := flagSettingsResult(t, root, controllerNamespaceTestNamespace)
		skipped := diagnosticsWithCode(result.Diagnostics, outsideControllerNamespaceCode)
		if want := map[bool]int{false: 0, true: 1}[withTenant]; len(skipped) != want {
			t.Fatalf("tenant %t: skipped = %#v, want %d", withTenant, skipped, want)
		}
		if !result.Settings.ArgoCDConfigMapFound || hasProjectMissing(result.Diagnostics) || len(result.Projects) != 1 {
			t.Fatalf("tenant %t: argocd-cm found %t, Projects = %v, want the install settings and AppProject team", withTenant, result.Settings.ArgoCDConfigMapFound, result.Projects)
		}
	}
}

// A settings file that does not parse, in a namespace that
// --argocd-namespace skips, gives a warning and never fails the run: Argo
// CD would not read it.
func TestSkippedSettingsThatDoNotParseNeverFailTheRun(t *testing.T) {
	root := t.TempDir()
	writeDemoApplication(t, root, "apps/demo.yaml", "argocd")
	writeTestFile(t, filepath.Join(root, "tenant", "argocd-cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: argocd-cm\n  namespace: tenant-a\ndata:\n  resource.customizations:\n    nested: map\n")

	result := flagSettingsResult(t, root, "argocd")
	skipped := diagnosticsWithCode(result.Diagnostics, outsideControllerNamespaceCode)
	if len(skipped) != 1 || !strings.Contains(skipped[0].Message, "does not fail on it") {
		t.Fatalf("skipped = %#v, want one warning that names the parse error", skipped)
	}
	if _, err := (Orchestrator{}).Build(context.Background(), BuildRequest{Path: root, Parallelism: 1}); err == nil {
		t.Fatalf("Build() without the flag error = nil, want the parse error")
	}
}
