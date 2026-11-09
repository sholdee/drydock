package app

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	sourcepkg "github.com/sholdee/drydock/internal/source"
)

// writeKustomizedInstallRepo writes an Application argocd in gitops whose
// kustomization sets namespace gitops over an argocd-cmd-params-cm and the
// AppProject team that name argocd, and the Application demo of team.
func writeKustomizedInstallRepo(t *testing.T, root string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", "argocd.yaml"), kustomizeApplicationYAML("argocd", controllerNamespaceTestNamespace, "install", controllerNamespaceTestNamespace))
	writeTestFile(t, filepath.Join(root, "install", "kustomization.yaml"), "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nnamespace: gitops\nresources: [argocd-cmd-params-cm.yaml, project.yaml]\n")
	writeTestFile(t, filepath.Join(root, "install", "argocd-cmd-params-cm.yaml"), commandParametersConfigMapYAML("argocd", "  application.namespaces: \"\"\n"))
	writeTestFile(t, filepath.Join(root, "install", "project.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: team
  namespace: argocd
spec:
  sourceRepos: ["*"]
  destinations: [{server: "*", namespace: "*"}]
  sourceNamespaces: ["gitops"]
`)
	writeTestFile(t, filepath.Join(root, "apps", "demo.yaml"), strings.Replace(kustomizeApplicationYAML("demo", controllerNamespaceTestNamespace, "demo", "demo"), "project: default", "project: team", 1))
	writeTestFile(t, filepath.Join(root, "demo", "kustomization.yaml"), "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources: [cm.yaml]\n")
	writeTestFile(t, filepath.Join(root, "demo", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\n")
}

// A committed argocd-cmd-params-cm and AppProject land where the
// kustomization of the Application that renders them puts them. They name
// and are filtered by gitops, not by the argocd that they write.
func TestCommittedSettingsLandInTheKustomizationNamespace(t *testing.T) {
	for _, flag := range []string{"", controllerNamespaceTestNamespace} {
		t.Run("flag="+flag, func(t *testing.T) {
			root := t.TempDir()
			writeKustomizedInstallRepo(t, root)
			result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root, Parallelism: 1, Strict: true, ArgoCDNamespace: flag})
			if err != nil {
				t.Fatalf("Build(--strict) error = %v\nDiagnostics: %#v", err, result.Diagnostics)
			}
			if got := result.Settings.ControllerNamespace(""); got != controllerNamespaceTestNamespace || !result.Settings.ControllerNamespaceKnown() {
				t.Fatalf("ControllerNamespace = %q (known %t), want gitops", got, result.Settings.ControllerNamespaceKnown())
			}
			if names := applicationNames(result.Applications); !slices.Contains(names, "demo") {
				t.Fatalf("Applications = %v, want demo", names)
			}
			if got := diagnosticsWithCode(result.Diagnostics, outsideControllerNamespaceCode); len(got) != 0 {
				t.Fatalf("skipped = %#v, want none", got)
			}
			if hasProjectMissing(result.Diagnostics) || len(result.Projects) != 1 {
				t.Fatalf("Projects = %v, Diagnostics = %#v, want team found", result.Projects, result.Diagnostics)
			}
		})
	}
}

// A committed child Application without a namespace lands in the
// kustomization namespace of the Application that renders it, so the
// install that it renders in gitops reconciles it.
func TestNamespaceLessChildTakesTheKustomizationNamespaceOfItsParent(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "root.yaml"), kustomizeApplicationYAML("root", controllerNamespaceTestNamespace, "apps", "default"))
	writeTestFile(t, filepath.Join(root, "apps", "kustomization.yaml"), "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nnamespace: gitops\nresources: [install.yaml, demo.yaml]\n")
	writeTestFile(t, filepath.Join(root, "apps", "install.yaml"), strings.Replace(kustomizeApplicationYAML("argocd", "", "install", controllerNamespaceTestNamespace), "  namespace: \n", "", 1))
	writeDemoApplication(t, root, "apps/demo.yaml", "")
	writeTestFile(t, filepath.Join(root, "install", "kustomization.yaml"), "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nnamespace: gitops\nresources: [cmd.yaml]\n")
	writeTestFile(t, filepath.Join(root, "install", "cmd.yaml"), commandParametersConfigMapYAML(controllerNamespaceTestNamespace, "  application.namespaces: \"\"\n"))

	result, err := Orchestrator{}.ListApplications(context.Background(), BuildRequest{Path: root, Parallelism: 1, Strict: true})
	if err != nil {
		t.Fatalf("ListApplications(--strict) error = %v\nDiagnostics: %#v", err, result.Diagnostics)
	}
	if got := result.Settings.ControllerNamespace(""); got != controllerNamespaceTestNamespace {
		t.Fatalf("ControllerNamespace = %q, want gitops", got)
	}
	if warnings := controllerNamespaceWarnings(result.Diagnostics); len(warnings) != 0 {
		t.Fatalf("namespace warnings = %#v, want none: the install in gitops is not another install", warnings)
	}
}

// An Application of another repository that --repo-map maps does not
// render the committed files of this repository, so it never decides
// whether they are another install.
func TestRepoMappedApplicationDoesNotRenderCommittedSettings(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	writeTestFile(t, filepath.Join(root, "argocd", "cmd.yaml"), commandParametersConfigMapYAML(controllerNamespaceTestNamespace, "  application.namespaces: team-b\n"))
	writeDemoApplication(t, root, "apps/demo.yaml", controllerNamespaceTestNamespace)
	team := strings.Replace(kustomizeApplicationYAML("teamapp", "team-a", "argocd", "team-a"), "https://github.com/example/repo", "https://github.com/team/other", 1)
	writeTestFile(t, filepath.Join(root, "apps", "teamapp.yaml"), team)
	writeTestFile(t, filepath.Join(other, "argocd", "thing.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: team-thing\n")

	request := BuildRequest{Path: root, Parallelism: 1, RepoMaps: []sourcepkg.RepoMap{{URL: "https://github.com/team/other", Path: other}}}
	result, err := Orchestrator{}.ListApplications(context.Background(), request)
	if err != nil {
		t.Fatalf("ListApplications() error = %v\nDiagnostics: %#v", err, result.Diagnostics)
	}
	if got := result.Settings.ControllerNamespace(""); got != controllerNamespaceTestNamespace || !result.Settings.ControllerNamespaceKnown() {
		t.Fatalf("ControllerNamespace = %q (known %t), want gitops", got, result.Settings.ControllerNamespaceKnown())
	}
	if names := applicationNames(result.Applications); !slices.Contains(names, "demo") {
		t.Fatalf("Applications = %v, want demo", names)
	}
}

// A generic .argocd-source.yaml can name a value file with
// $ARGOCD_APP_NAME, so the install that it reads changes with the
// controller namespace. drydock computes the install again, so the value
// file that the final render reads is not a settings source of its own.
func TestGenericSourceOverrideWithTheInstanceNameRecomputesTheInstall(t *testing.T) {
	root := t.TempDir()
	// The template builds the ConfigMap names, so the frontier does not
	// see them and the settings-install pass renders the install.
	writeTestFile(t, filepath.Join(root, "install", "Chart.yaml"), "apiVersion: v2\nname: argo-cd\nversion: 9.0.0\n")
	writeTestFile(t, filepath.Join(root, "install", "values.yaml"), "configs:\n  cm: {}\n  params:\n    application.namespaces: \"\"\n")
	writeTestFile(t, filepath.Join(root, "install", "templates", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ printf "argocd-%s" "cm" }}
  namespace: {{ .Release.Namespace }}
data:
{{- range $k, $v := .Values.configs.cm }}
  {{ $k }}: {{ $v | quote }}
{{- end }}
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ printf "argocd-%s-params-cm" "cmd" }}
  namespace: {{ .Release.Namespace }}
data:
{{- range $k, $v := .Values.configs.params }}
  {{ $k }}: {{ $v | quote }}
{{- end }}
`)
	writeTestFile(t, filepath.Join(root, "install", ".argocd-source.yaml"), "helm:\n  valueFiles:\n    - values-$ARGOCD_APP_NAME.yaml\n")
	writeTestFile(t, filepath.Join(root, "install", "values-argocd.yaml"), "configs:\n  cm:\n    kustomize.buildOptions: --load-restrictor=LoadRestrictionsNone\n")
	writeTestFile(t, filepath.Join(root, "apps", "argocd.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: argocd
  namespace: gitops
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: install
    helm:
      ignoreMissingValueFiles: true
      valuesObject:
        configs:
          cm:
            kustomize.buildOptions: --enable-helm
  destination:
    name: in-cluster
    namespace: gitops
`)
	writeDemoApplication(t, root, "apps/demo.yaml", controllerNamespaceTestNamespace)

	result, err := Orchestrator{}.ListApplications(context.Background(), BuildRequest{Path: root, Parallelism: 1, Strict: true})
	if err != nil {
		t.Fatalf("ListApplications(--strict) error = %v\nDiagnostics: %#v", err, result.Diagnostics)
	}
	for _, diag := range result.Diagnostics {
		if strings.Contains(diag.Message, "conflicting") {
			t.Fatalf("diagnostic %#v, want no conflict with the value file that the install reads", diag)
		}
	}
	if got := result.Settings.ControllerNamespace(""); got != controllerNamespaceTestNamespace {
		t.Fatalf("ControllerNamespace = %q, want gitops", got)
	}
}
