package app

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/argoproj/argo-cd/v3/common"
	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	argoutil "github.com/argoproj/argo-cd/v3/util/argo"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/render"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// controllerNamespaceTestNamespace is a controller namespace that is not
// the drydock default, so the tests tell the two apart.
const controllerNamespaceTestNamespace = "gitops"

// writeControllerNamespaceApp writes the Application root in namespace
// (writeControllerNamespaceAppNamed).
func writeControllerNamespaceApp(t *testing.T, root, namespace string) {
	t.Helper()
	writeControllerNamespaceAppNamed(t, root, "root", namespace)
}

// writeControllerNamespaceAppNamed writes an Application name in namespace
// that renders a local chart. The chart prints the ARGOCD_APP_NAME that a
// Helm parameter receives, and the override value that a
// .argocd-source-<app>.yaml file sets. One file uses the bare name and one
// the <namespace>_<name> form, so the output names the file that the
// render read.
func writeControllerNamespaceAppNamed(t *testing.T, root, name, namespace string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", name+".yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: `+name+`
  namespace: `+namespace+`
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: charts/`+name+`
    helm:
      parameters:
        - name: appName
          value: $ARGOCD_APP_NAME
  destination:
    name: in-cluster
    namespace: workloads
`)
	chart := filepath.Join(root, "charts", name)
	writeTestFile(t, filepath.Join(chart, "Chart.yaml"), "apiVersion: v2\nname: "+name+"\nversion: 0.1.0\n")
	writeTestFile(t, filepath.Join(chart, "values.yaml"), "appName: unset\noverride: none\n")
	writeTestFile(t, filepath.Join(chart, "templates", "env.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: `+name+`-env
data:
  appName: {{ .Values.appName | quote }}
  override: {{ .Values.override | quote }}
`)
	writeTestFile(t, filepath.Join(chart, ".argocd-source-"+name+".yaml"), "helm:\n  valuesObject:\n    override: bare\n")
	writeTestFile(t, filepath.Join(chart, ".argocd-source-"+namespace+"_"+name+".yaml"), "helm:\n  valuesObject:\n    override: qualified\n")
}

func commandParametersConfigMapYAML(namespace, data string) string {
	return `apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cmd-params-cm
  namespace: ` + namespace + `
data:
` + data
}

// upstreamTrackingID is the tracking-id annotation that the Argo CD v3.6.1
// controller sets on obj for application, with its own namespace as
// controllerNamespace (controller/state.go:797).
func upstreamTrackingID(t *testing.T, application argoappv1.Application, controllerNamespace string, obj *unstructured.Unstructured) string {
	t.Helper()
	probe := obj.DeepCopy()
	probe.SetAnnotations(nil)
	if err := argoutil.NewResourceTracking().SetAppInstance(probe, common.LabelKeyAppInstance, application.InstanceName(controllerNamespace), application.Spec.Destination.Namespace, argoappv1.TrackingMethodAnnotation, ""); err != nil {
		t.Fatalf("SetAppInstance() error = %v", err)
	}
	return probe.GetAnnotations()[common.AnnotationKeyAppInstance]
}

func controllerNamespaceManifest(t *testing.T, manifests []render.Manifest, name string) *unstructured.Unstructured {
	t.Helper()
	for _, manifest := range manifests {
		if manifest.Object != nil && manifest.Object.GetKind() == "ConfigMap" && manifest.Object.GetName() == name {
			return manifest.Object
		}
	}
	t.Fatalf("no ConfigMap %s in the output", name)
	return nil
}

func controllerNamespaceApplication(t *testing.T, applications []argoappv1.Application, name string) argoappv1.Application {
	t.Helper()
	for _, application := range applications {
		if application.Name == name {
			return application
		}
	}
	t.Fatalf("Applications = %v, want %s", applicationNames(applications), name)
	return argoappv1.Application{}
}

// assertControllerNamespaceRender checks the render of root against the
// Argo CD v3.6.1 functions for the controller namespace want.
func assertControllerNamespaceRender(t *testing.T, result BuildResult, want string) {
	t.Helper()
	assertControllerNamespaceRenderOf(t, result, "root", want)
}

// assertControllerNamespaceRenderOf checks the render of the Application
// name for the controller namespace want: the instance name decides
// ARGOCD_APP_NAME (reposerver/repository/repository.go:1868), the
// .argocd-source-<app>.yaml file (repository.go:1907), and the tracking-id
// (controller/state.go:797).
func assertControllerNamespaceRenderOf(t *testing.T, result BuildResult, name, want string) {
	t.Helper()
	application := controllerNamespaceApplication(t, result.Applications, name)
	instance := application.InstanceName(want)
	env := controllerNamespaceManifest(t, result.Manifests, name+"-env")
	data, _, _ := unstructured.NestedStringMap(env.Object, "data")
	if data["appName"] != instance {
		t.Fatalf("%s: ARGOCD_APP_NAME = %q, want the instance name %q for controller namespace %q", name, data["appName"], instance, want)
	}
	wantOverride := "qualified"
	if !strings.Contains(instance, "_") {
		wantOverride = "bare"
	}
	if data["override"] != wantOverride {
		t.Fatalf("%s: override = %q, want %q from .argocd-source-%s.yaml", name, data["override"], wantOverride, instance)
	}
	if got, wantID := env.GetAnnotations()[common.AnnotationKeyAppInstance], upstreamTrackingID(t, application, want, env); got != wantID {
		t.Fatalf("%s: tracking-id = %q, want %q", name, got, wantID)
	}
}

// An argocd-cmd-params-cm in the repository names the namespace that Argo CD
// runs in. An Application in that namespace has its bare name as instance
// name, as in the Argo CD controller, and an Application in an enabled
// namespace has the <namespace>_<name> form: it reads the qualified
// .argocd-source file and ignores the bare-name one.
func TestBuildUsesCommandParametersNamespaceForInstanceNames(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "settings", "argocd-cmd-params-cm.yaml"), commandParametersConfigMapYAML(controllerNamespaceTestNamespace, "  application.namespaces: team-a\n"))
	writeControllerNamespaceApp(t, root, controllerNamespaceTestNamespace)
	writeControllerNamespaceAppNamed(t, root, "team", "team-a")

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root, Parallelism: 1})
	if err != nil {
		t.Fatalf("Build() error = %v\nDiagnostics: %#v", err, result.Diagnostics)
	}
	assertControllerNamespaceRender(t, result, controllerNamespaceTestNamespace)
	assertControllerNamespaceRenderOf(t, result, "team", controllerNamespaceTestNamespace)
	team := controllerNamespaceApplication(t, result.Applications, "team")
	if got := team.InstanceName(result.Settings.ControllerNamespace("")); got != "team-a_team" {
		t.Fatalf("instance name = %q, want team-a_team", got)
	}
}

// Without anything that names the namespace, drydock keeps the default
// argocd. An Application in another namespace then has the
// <namespace>_<name> instance name.
func TestBuildDefaultsTheControllerNamespaceToArgoCD(t *testing.T) {
	root := t.TempDir()
	writeControllerNamespaceApp(t, root, controllerNamespaceTestNamespace)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root, Parallelism: 1})
	if err != nil {
		t.Fatalf("Build() error = %v\nDiagnostics: %#v", err, result.Diagnostics)
	}
	assertControllerNamespaceRender(t, result, defaultArgoCDControllerNamespace)
}

// --argocd-namespace names the controller namespace without any
// argocd-cmd-params-cm: tracking, ARGOCD_APP_NAME, and the override lookup
// all use it.
func TestArgoCDNamespaceFlagNamesInstanceNames(t *testing.T) {
	root := t.TempDir()
	writeControllerNamespaceApp(t, root, controllerNamespaceTestNamespace)

	request := BuildRequest{Path: root, Parallelism: 1, ArgoCDNamespace: controllerNamespaceTestNamespace}
	result, err := Orchestrator{}.Build(context.Background(), request)
	if err != nil {
		t.Fatalf("Build() error = %v\nDiagnostics: %#v", err, result.Diagnostics)
	}
	assertControllerNamespaceRender(t, result, controllerNamespaceTestNamespace)
	if diags := controllerNamespaceWarnings(result.Diagnostics); len(diags) != 0 {
		t.Fatalf("namespace warnings = %#v, want none without an argocd-cmd-params-cm", diags)
	}
}

// --argocd-namespace must name a valid namespace.
func TestArgoCDNamespaceFlagMustBeANamespaceName(t *testing.T) {
	request := BuildRequest{Path: t.TempDir(), Parallelism: 1, ArgoCDNamespace: "Not_A_Namespace"}
	if _, err := (Orchestrator{}).ListApplications(context.Background(), request); err == nil || !strings.Contains(err.Error(), "argocd-namespace") {
		t.Fatalf("ListApplications() error = %v, want an invalid --argocd-namespace error", err)
	}
}

// Changed-only selection reads the .argocd-source-<app>.yaml file that the
// render reads, so the instance name uses the controller namespace of the
// settings there too.
func TestSelectionOverridesUseTheControllerNamespace(t *testing.T) {
	root := t.TempDir()
	writeControllerNamespaceApp(t, root, controllerNamespaceTestNamespace)
	writeTestFile(t, filepath.Join(root, "charts", "root", ".argocd-source-root.yaml"), "helm:\n  valueFiles:\n    - ../../shared/bare.yaml\n")
	writeTestFile(t, filepath.Join(root, "shared", "bare.yaml"), "override: shared\n")
	application := argoappv1.Application{
		Name:      "root",
		Namespace: controllerNamespaceTestNamespace,
		Spec: argoappv1.ApplicationSpec{
			Source: &argoappv1.ApplicationSource{RepoURL: "https://github.com/example/repo", TargetRevision: "main", Path: "charts/root"},
		},
	}
	inputs := []ApplicationSelectionInput{{Application: application}}

	owns := func(paths []string) bool {
		return slices.Contains(paths, "shared/bare.yaml")
	}
	if got := withSelectionOnlyPathsIn(context.Background(), root, nil, selfRepoRefs{}, sourceTypeGates{}, controllerNamespaceTestNamespace, inputs).inputs; !owns(got[0].Paths) {
		t.Fatalf("paths = %v, want shared/bare.yaml from .argocd-source-root.yaml", got[0].Paths)
	}
	if got := withSelectionOnlyPaths(context.Background(), root, nil, selfRepoRefs{}, sourceTypeGates{}, inputs).inputs; owns(got[0].Paths) {
		t.Fatalf("paths = %v, want no shared/bare.yaml for controller namespace argocd", got[0].Paths)
	}
}

// A changed-only diff selects an Application through the value file that
// its .argocd-source-<app>.yaml names, with the instance name of the
// controller namespace: bare in the controller namespace, and
// <namespace>_<name> in an enabled namespace. Both sides use it, whether an
// argocd-cmd-params-cm or --argocd-namespace names it.
func TestDiffChangedOnlyOwnsOverrideInputsOfTheControllerNamespace(t *testing.T) {
	for _, tc := range []struct {
		name string
		flag string
	}{
		{name: "argocd-cmd-params-cm"},
		{name: "--argocd-namespace", flag: controllerNamespaceTestNamespace},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			left := filepath.Join(root, "left")
			right := filepath.Join(root, "right")
			for side, value := range map[string]string{left: "old", right: "new"} {
				params := commandParametersConfigMapYAML(controllerNamespaceTestNamespace, "  application.namespaces: team-a\n")
				if tc.flag != "" {
					params = strings.Replace(params, "  namespace: "+controllerNamespaceTestNamespace+"\n", "", 1)
				}
				writeTestFile(t, filepath.Join(side, "settings", "argocd-cmd-params-cm.yaml"), params)
				writeControllerNamespaceApp(t, side, controllerNamespaceTestNamespace)
				writeControllerNamespaceAppNamed(t, side, "team", "team-a")
				writeTestFile(t, filepath.Join(side, "charts", "root", ".argocd-source-root.yaml"), "helm:\n  valueFiles:\n    - ../../shared/root.yaml\n")
				writeTestFile(t, filepath.Join(side, "charts", "team", ".argocd-source-team-a_team.yaml"), "helm:\n  valueFiles:\n    - ../../shared/team.yaml\n")
				writeTestFile(t, filepath.Join(side, "shared", "root.yaml"), "override: root-"+value+"\n")
				writeTestFile(t, filepath.Join(side, "shared", "team.yaml"), "override: team-"+value+"\n")
			}

			request := DiffRequest{
				LeftPath:          left,
				RightPath:         right,
				ChangedOnly:       true,
				StrictChangedOnly: true,
				Unified:           3,
				ArgoCDNamespace:   tc.flag,
			}
			result, err := Orchestrator{}.DiffApps(context.Background(), request)
			if err != nil {
				t.Fatalf("DiffApps() error = %v, diagnostics = %#v", err, result.Diagnostics)
			}
			names := make([]string, 0, len(result.Results))
			for _, item := range result.Results {
				names = append(names, item.Parent.Name)
			}
			slices.Sort(names)
			if strings.Join(names, ",") != "root,team" {
				t.Fatalf("Results = %v, want root and team", names)
			}
			for _, item := range result.Results {
				for _, want := range []string{"-  override: " + item.Parent.Name + "-old", "+  override: " + item.Parent.Name + "-new"} {
					if !strings.Contains(item.Diff, want) {
						t.Fatalf("diff of %s missing %q:\n%s", item.Parent.Name, want, item.Diff)
					}
				}
			}
		})
	}
}

// A plugin policy bootstrap entrypoint renders as an Application in the
// controller namespace, so the plugin sees its bare name and the
// controller namespace in ARGOCD_APP_NAME and ARGOCD_APP_NAMESPACE.
func TestPluginPolicyBootstrapRendersInTheControllerNamespace(t *testing.T) {
	for _, tc := range []struct {
		name string
		flag string
	}{
		{name: "argocd-cmd-params-cm"},
		{name: "--argocd-namespace", flag: controllerNamespaceTestNamespace},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeBootstrapExecPluginPolicy(t, root, "bootstrap", "PklProject")
			writeTestFile(t, filepath.Join(root, "bootstrap", "PklProject"), "")
			if tc.flag == "" {
				writeTestFile(t, filepath.Join(root, "settings", "argocd-cmd-params-cm.yaml"), commandParametersConfigMapYAML(controllerNamespaceTestNamespace, "  server.insecure: \"true\"\n"))
			}
			runner := &recordingExecRunner{}

			request := BuildRequest{Path: root, PluginOptions: trustedBootstrapPluginOptions(t, root), ArgoCDNamespace: tc.flag}
			result, err := (Orchestrator{PluginExecRunner: runner}).ListApplications(context.Background(), request)
			if err != nil {
				t.Fatalf("ListApplications() error = %v\nDiagnostics: %#v", err, result.Diagnostics)
			}
			if runner.calls != 1 {
				t.Fatalf("exec runner calls = %d, want one bootstrap render", runner.calls)
			}
			env := strings.Join(runner.lastRequest.ExtraEnv, "\n")
			for _, want := range []string{"ARGOCD_APP_NAME=cluster-root", "ARGOCD_APP_NAMESPACE=" + controllerNamespaceTestNamespace} {
				if !strings.Contains(env, want) {
					t.Fatalf("plugin env = %q, want %q", runner.lastRequest.ExtraEnv, want)
				}
			}
		})
	}
}

// The probe that finds the Argo CD installs reads the
// .argocd-source-<app>.yaml file of the controller namespace, and its memo
// keeps the two namespaces apart.
func TestSettingsInstallProbeReadsOverridesOfTheControllerNamespace(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "platform", "Chart.yaml"), "apiVersion: v2\nname: platform\nversion: 0.1.0\n")
	writeTestFile(t, filepath.Join(root, "platform", ".argocd-source-platform.yaml"), "helm:\n  valueFiles:\n    - settings.yaml\n")
	writeTestFile(t, filepath.Join(root, "platform", "settings.yaml"), "configs:\n  cm:\n    timeout.reconciliation: 300s\n")
	application := decodeTestApplication(t, `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: platform
  namespace: `+controllerNamespaceTestNamespace+`
spec:
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: platform
  destination:
    name: in-cluster
    namespace: `+controllerNamespaceTestNamespace+`
`)
	request := BuildRequest{}
	request.settingsInstallMemo = &sync.Map{}
	if install := applicationSettingsInstall(context.Background(), request, settingsInstallProbe(root, request, controllerNamespaceTestNamespace), application); !install.installsArgoCD() {
		t.Fatalf("install = %#v, want the values file of .argocd-source-platform.yaml", install)
	}
	if install := applicationSettingsInstall(context.Background(), request, settingsInstallProbe(root, request, ""), application); install.installsArgoCD() {
		t.Fatalf("install = %#v, want none: the instance name in argocd is gitops_platform", install)
	}
}

// controllerNamespaceWarnings returns the warnings about the controller
// namespace decision.
func controllerNamespaceWarnings(diags []diagnostic.Diagnostic) []diagnostic.Diagnostic {
	return append(diagnosticsWithCode(diags, controllerNamespaceAmbiguousCode), diagnosticsWithCode(diags, controllerNamespaceUnconfirmedCode)...)
}

// writeHubAndSpokeParams writes a hub argocd-cmd-params-cm in gitops and a
// spoke one in argocd under spokeDir. No Application renders either file.
func writeHubAndSpokeParams(t *testing.T, root, spokeDir string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "hub", "argocd-cmd-params-cm.yaml"), commandParametersConfigMapYAML(controllerNamespaceTestNamespace, "  server.insecure: \"true\"\n"))
	writeTestFile(t, filepath.Join(root, spokeDir, "argocd-cmd-params-cm.yaml"), commandParametersConfigMapYAML("argocd", "  server.insecure: \"true\"\n"))
}

// The argocd-cmd-params-cm ConfigMaps decide the namespace as a set, so the
// folder order does not matter. When they disagree, drydock uses argocd,
// which one of them names, with one warning that --strict accepts.
func TestCommandParametersNamespacesDecideAsASet(t *testing.T) {
	var messages []string
	for _, spoke := range []string{"a-spoke", "z-spoke"} {
		t.Run(spoke, func(t *testing.T) {
			root := t.TempDir()
			writeHubAndSpokeParams(t, root, spoke)

			result, err := Orchestrator{}.ListApplications(context.Background(), BuildRequest{Path: root, Parallelism: 1, Strict: true})
			if err != nil {
				t.Fatalf("ListApplications(--strict) error = %v\nDiagnostics: %#v", err, result.Diagnostics)
			}
			if got := result.Settings.ControllerNamespace(""); got != defaultArgoCDControllerNamespace {
				t.Fatalf("ControllerNamespace = %q, want argocd", got)
			}
			warnings := diagnosticsWithCode(result.Diagnostics, controllerNamespaceAmbiguousCode)
			if len(warnings) != 1 || len(controllerNamespaceWarnings(result.Diagnostics)) != 1 {
				t.Fatalf("namespace warnings = %#v, want one %s", controllerNamespaceWarnings(result.Diagnostics), controllerNamespaceAmbiguousCode)
			}
			for _, want := range []string{"argocd, gitops", "--argocd-namespace"} {
				if !strings.Contains(warnings[0].Message, want) {
					t.Fatalf("warning %q, want %q", warnings[0].Message, want)
				}
			}
			messages = append(messages, warnings[0].Message)
		})
	}
	if len(messages) == 2 && messages[0] != messages[1] {
		t.Fatalf("warnings differ by folder order:\n%s\n%s", messages[0], messages[1])
	}
}

// --argocd-namespace wins over the argocd-cmd-params-cm ConfigMaps. When none
// of them names it, drydock warns, and --strict fails.
func TestArgoCDNamespaceFlagThatNoCommandParametersNameFailsStrict(t *testing.T) {
	root := t.TempDir()
	writeHubAndSpokeParams(t, root, "spoke")

	request := BuildRequest{Path: root, Parallelism: 1, ArgoCDNamespace: "platform"}
	result, err := Orchestrator{}.ListApplications(context.Background(), request)
	if err != nil {
		t.Fatalf("ListApplications() error = %v\nDiagnostics: %#v", err, result.Diagnostics)
	}
	if got := result.Settings.ControllerNamespace(""); got != "platform" {
		t.Fatalf("ControllerNamespace = %q, want platform from --argocd-namespace", got)
	}
	warnings := diagnosticsWithCode(result.Diagnostics, controllerNamespaceUnconfirmedCode)
	if len(warnings) != 1 || !strings.Contains(warnings[0].Message, `"platform"`) || !strings.Contains(warnings[0].Message, "argocd, gitops") {
		t.Fatalf("warnings = %#v, want one %s that names platform, argocd, and gitops", controllerNamespaceWarnings(result.Diagnostics), controllerNamespaceUnconfirmedCode)
	}

	request.Strict = true
	if _, err := (Orchestrator{}).ListApplications(context.Background(), request); err == nil || !strings.Contains(err.Error(), "--argocd-namespace") {
		t.Fatalf("ListApplications(--strict) error = %v, want the unconfirmed namespace warning to fail", err)
	}

	request.ArgoCDNamespace = controllerNamespaceTestNamespace
	if result, err := (Orchestrator{}).ListApplications(context.Background(), request); err != nil {
		t.Fatalf("ListApplications(--strict) error = %v, want a pass when a candidate names the flag namespace\nDiagnostics: %#v", err, result.Diagnostics)
	}
}

// Without --argocd-namespace, candidates that name several namespaces
// without argocd leave the default argocd, which none of them names: that
// fails --strict.
func TestCommandParametersWithoutArgoCDFailStrict(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "hub", "argocd-cmd-params-cm.yaml"), commandParametersConfigMapYAML(controllerNamespaceTestNamespace, "  server.insecure: \"true\"\n"))
	writeTestFile(t, filepath.Join(root, "tenant", "argocd-cmd-params-cm.yaml"), commandParametersConfigMapYAML("team-a", "  server.insecure: \"true\"\n"))

	result, err := Orchestrator{}.ListApplications(context.Background(), BuildRequest{Path: root, Parallelism: 1})
	if err != nil {
		t.Fatalf("ListApplications() error = %v", err)
	}
	if got := result.Settings.ControllerNamespace(""); got != defaultArgoCDControllerNamespace {
		t.Fatalf("ControllerNamespace = %q, want the default argocd", got)
	}
	if result.Settings.ControllerNamespaceKnown() {
		t.Fatalf("ControllerNamespaceKnown() = true, want false: no candidate names argocd")
	}
	warnings := diagnosticsWithCode(result.Diagnostics, controllerNamespaceUnconfirmedCode)
	if len(warnings) != 1 || !strings.Contains(warnings[0].Message, "gitops, team-a") || !strings.Contains(warnings[0].Message, `default "argocd"`) {
		t.Fatalf("warnings = %#v, want one %s", controllerNamespaceWarnings(result.Diagnostics), controllerNamespaceUnconfirmedCode)
	}
	if _, err := (Orchestrator{}).ListApplications(context.Background(), BuildRequest{Path: root, Parallelism: 1, Strict: true}); err == nil {
		t.Fatalf("ListApplications(--strict) error = nil, want the unconfirmed namespace warning to fail")
	}
}

// The namespace gate uses the namespace that --argocd-namespace names, also
// when the only argocd-cmd-params-cm that names a namespace names another.
// That one is skipped, and drydock warns that no candidate names the flag
// namespace. The argocd-cmd-params-cm without a namespace still counts.
func TestArgoCDNamespaceFlagDrivesTheNamespaceGate(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "settings", "argocd-cmd-params-cm.yaml"), commandParametersConfigMapYAML("argocd", "  application.namespaces: team-*\n"))
	writeTestFile(t, filepath.Join(root, "settings", "base-params.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: argocd-cmd-params-cm\ndata:\n  server.insecure: \"true\"\n")
	writeControllerNamespaceAppNamed(t, root, "kept", controllerNamespaceTestNamespace)
	writeControllerNamespaceAppNamed(t, root, "gated", "argocd")

	request := BuildRequest{Path: root, Parallelism: 1, ArgoCDNamespace: controllerNamespaceTestNamespace}
	result, err := Orchestrator{}.ListApplications(context.Background(), request)
	if err != nil {
		t.Fatalf("ListApplications() error = %v\nDiagnostics: %#v", err, result.Diagnostics)
	}
	if got := applicationNames(result.Applications); strings.Join(got, ",") != "kept" {
		t.Fatalf("Applications = %v, want only kept", got)
	}
	gated := diagnosticsWithCode(result.Diagnostics, namespaceNotEnabledCode)
	if len(gated) != 1 || !strings.Contains(gated[0].Message, "Argo CD namespace "+controllerNamespaceTestNamespace) {
		t.Fatalf("%s diagnostics = %#v, want one for gated with Argo CD namespace gitops", namespaceNotEnabledCode, gated)
	}
	if got := diagnosticsWithCode(result.Diagnostics, controllerNamespaceUnconfirmedCode); len(got) != 1 {
		t.Fatalf("namespace warnings = %#v, want one %s", controllerNamespaceWarnings(result.Diagnostics), controllerNamespaceUnconfirmedCode)
	}
	if got := diagnosticsWithCode(result.Diagnostics, outsideControllerNamespaceCode); len(got) != 1 || !strings.Contains(got[0].Message, "argocd/argocd-cmd-params-cm") {
		t.Fatalf("%s diagnostics = %#v, want one for the argocd-cmd-params-cm in argocd", outsideControllerNamespaceCode, got)
	}
}

// Static and fleet discovery decide the controller namespace alike: the one
// namespace when the candidates agree, and argocd with the same warning
// when they do not.
func TestStaticAndFleetDiscoveryDecideTheNamespaceAlike(t *testing.T) {
	for _, tc := range []struct {
		name     string
		write    func(t *testing.T, root string)
		want     string
		warnings int
	}{
		{
			name: "agree",
			write: func(t *testing.T, root string) {
				writeTestFile(t, filepath.Join(root, "settings", "argocd-cmd-params-cm.yaml"), commandParametersConfigMapYAML(controllerNamespaceTestNamespace, "  server.insecure: \"true\"\n"))
			},
			want: controllerNamespaceTestNamespace,
		},
		{
			name:     "disagree",
			write:    func(t *testing.T, root string) { writeHubAndSpokeParams(t, root, "spoke") },
			want:     defaultArgoCDControllerNamespace,
			warnings: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			tc.write(t, root)
			writeControllerNamespaceApp(t, root, tc.want)

			fleet, err := Orchestrator{}.ListApplications(context.Background(), BuildRequest{Path: root, Parallelism: 1})
			if err != nil {
				t.Fatalf("ListApplications() error = %v", err)
			}
			static, err := Orchestrator{}.DiagStatic(context.Background(), BuildRequest{Path: root, Parallelism: 1}, nil)
			if err != nil {
				t.Fatalf("DiagStatic() error = %v", err)
			}
			for name, got := range map[string]struct {
				namespace string
				diags     []diagnostic.Diagnostic
			}{
				"fleet":  {fleet.Settings.ControllerNamespace(""), fleet.Diagnostics},
				"static": {static.Settings.ControllerNamespace(""), static.Diagnostics},
			} {
				if got.namespace != tc.want {
					t.Fatalf("%s: ControllerNamespace = %q, want %q", name, got.namespace, tc.want)
				}
				if warnings := controllerNamespaceWarnings(got.diags); len(warnings) != tc.warnings {
					t.Fatalf("%s: namespace warnings = %#v, want %d", name, warnings, tc.warnings)
				}
			}
		})
	}
}

// moveArgoCDInstallNamespace rewrites every "namespace: argocd" under root
// to namespace, so an install shape installs Argo CD there.
func moveArgoCDInstallNamespace(t *testing.T, root, namespace string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		moved := strings.ReplaceAll(string(data), "namespace: argocd\n", "namespace: "+namespace+"\n")
		if moved == string(data) {
			return nil
		}
		return os.WriteFile(path, []byte(moved), 0o600)
	})
	if err != nil {
		t.Fatalf("move install namespace: %v", err)
	}
}

// An Application on this cluster that installs Argo CD in another namespace
// installs the Argo CD that runs there. Its rendered settings ConfigMaps
// land in that namespace, so they are the settings, and that namespace is
// the controller namespace of the instance names.
func TestRenderedArgoCDInstallNamesTheControllerNamespace(t *testing.T) {
	for _, shape := range argoCDInstallShapes() {
		t.Run(shape.name, func(t *testing.T) {
			root := t.TempDir()
			shape.write(t, root, []string{"values.yaml"})
			writeTestFile(t, filepath.Join(root, "argocd", "values.yaml"), `configs:
  cm:
    kustomize.buildOptions: --enable-helm
  params:
    reposerver.max.combined.directory.manifests.size: 50M
`)
			moveArgoCDInstallNamespace(t, root, controllerNamespaceTestNamespace)
			writeControllerNamespaceApp(t, root, controllerNamespaceTestNamespace)

			request := BuildRequest{Path: root, Parallelism: 1}
			if shape.repoMaps != nil {
				request.RepoMaps = shape.repoMaps(root)
			}
			result, err := Orchestrator{ChartAcquirer: fakeArgoCDChartAcquirer(t)}.Build(context.Background(), request)
			if err != nil {
				t.Fatalf("Build() error = %v\nDiagnostics: %#v", err, result.Diagnostics)
			}
			if got := result.Settings.ControllerNamespace(""); got != controllerNamespaceTestNamespace || !result.Settings.ControllerNamespaceKnown() {
				t.Fatalf("ControllerNamespace = %q, want %q from the rendered install", got, controllerNamespaceTestNamespace)
			}
			if got := result.Settings.MaxCombinedDirectoryManifestsSize.Value; got != "50M" {
				t.Fatalf("MaxCombinedDirectoryManifestsSize = %q, want the configs.params value of the rendered install", got)
			}
			if warnings := controllerNamespaceWarnings(result.Diagnostics); len(warnings) != 0 {
				t.Fatalf("namespace warnings = %#v, want none", warnings)
			}
			assertControllerNamespaceRender(t, result, controllerNamespaceTestNamespace)
		})
	}
}

// An install on another cluster runs another Argo CD. Its rendered
// argocd-cmd-params-cm never names the controller namespace.
func TestInstallOnAnotherClusterNeverNamesTheControllerNamespace(t *testing.T) {
	root := t.TempDir()
	writeLocalChartArgoCDInstall(t, root, nil)
	install := filepath.Join(root, "apps", "argocd.yaml")
	data, err := os.ReadFile(install)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	remote := strings.Replace(string(data), "    name: in-cluster\n    namespace: argocd\n", "    server: https://spoke.example.com\n    namespace: spoke\n", 1)
	remote = strings.Replace(remote, "      valueFiles:\n", "", 1)
	writeTestFile(t, install, remote)

	result := listArgoCDInstallSettings(t, root, argoCDInstallShape{}, fakeArgoCDChartAcquirer(t))
	if result.Settings.ControllerNamespaceKnown() || result.Settings.ControllerNamespace("") != defaultArgoCDControllerNamespace {
		t.Fatalf("controller namespace = %q from an install on another cluster, want the unknown default", result.Settings.ControllerNamespace(""))
	}
	if warnings := controllerNamespaceWarnings(result.Diagnostics); len(warnings) != 0 {
		t.Fatalf("namespace warnings = %#v, want none", warnings)
	}
}

// A tenant Argo CD that an Application in argocd installs in team-a, next to
// the hub argocd-cmd-params-cm in argocd, never silently becomes the
// controller namespace: drydock uses argocd and warns. The settings of the
// tenant install do not mix in, as in earlier releases.
func TestTenantInstallNextToTheHubWarns(t *testing.T) {
	root := t.TempDir()
	writeKustomizeArgoCDInstall(t, root, []string{"values.yaml"})
	writeTestFile(t, filepath.Join(root, "argocd", "values.yaml"), "configs:\n  params:\n    reposerver.max.combined.directory.manifests.size: 50M\n")
	writeTestFile(t, filepath.Join(root, "apps", "tenant.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: tenant-argocd
  namespace: argocd
spec:
  project: default
  source:
    repoURL: `+fakeArgoCDChartRepo+`
    chart: argo-cd
    targetRevision: `+fakeArgoCDChartVersion+`
    helm:
      valuesObject:
        configs:
          params:
            reposerver.max.combined.directory.manifests.size: 10M
  destination:
    name: in-cluster
    namespace: team-a
`)

	result := listArgoCDInstallSettings(t, root, argoCDInstallShape{}, fakeArgoCDChartAcquirer(t))
	if got := result.Settings.ControllerNamespace(""); got != defaultArgoCDControllerNamespace {
		t.Fatalf("ControllerNamespace = %q, want argocd", got)
	}
	warnings := diagnosticsWithCode(result.Diagnostics, controllerNamespaceAmbiguousCode)
	if len(warnings) != 1 || !strings.Contains(warnings[0].Message, "other Argo CD installs in team-a") {
		t.Fatalf("namespace warnings = %#v, want one %s that names team-a as another install", controllerNamespaceWarnings(result.Diagnostics), controllerNamespaceAmbiguousCode)
	}
	if got := applicationNames(result.Applications); !slices.Contains(got, "other") {
		t.Fatalf("Applications = %v, want other kept by the gate in argocd", got)
	}
	if got := result.Settings.MaxCombinedDirectoryManifestsSize.Value; got != "50M" {
		t.Fatalf("MaxCombinedDirectoryManifestsSize = %q, want 50M from the install in argocd", got)
	}
	for _, diag := range result.Diagnostics {
		if strings.Contains(diag.Message, "conflicting") {
			t.Fatalf("diagnostic %#v, want no conflict with the tenant install", diag)
		}
	}
}

// writeNamespacedSettingsRepo writes, in namespace other, an argocd-cm, a
// repository Secret, a cluster Secret, and the AppProject team, and an
// argocd-cm and the AppProject shared without a namespace. Application app
// in controllerNamespaceTestNamespace uses team.
func writeNamespacedSettingsRepo(t *testing.T, root, other string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "settings", "other-cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
  namespace: `+other+`
data:
  application.resourceTrackingMethod: label
`)
	writeTestFile(t, filepath.Join(root, "settings", "plain-cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
data:
  application.instanceLabelKey: example.com/instance
`)
	writeTestFile(t, filepath.Join(root, "settings", "repo-secret.yaml"), `apiVersion: v1
kind: Secret
metadata:
  name: charts
  namespace: `+other+`
  labels:
    argocd.argoproj.io/secret-type: repository
stringData:
  url: https://charts.example.com
  type: helm
`)
	writeTestFile(t, filepath.Join(root, "settings", "cluster-secret.yaml"), `apiVersion: v1
kind: Secret
metadata:
  name: remote
  namespace: `+other+`
  labels:
    argocd.argoproj.io/secret-type: cluster
stringData:
  name: remote
  server: https://remote.example.com
`)
	writeTestFile(t, filepath.Join(root, "projects", "team.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: team
  namespace: `+other+`
spec:
  sourceRepos: ["*"]
  destinations: [{server: "*", namespace: "*"}]
`)
	writeTestFile(t, filepath.Join(root, "projects", "shared.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: shared
spec:
  sourceRepos: ["*"]
  destinations: [{server: "*", namespace: "*"}]
`)
	writeTestFile(t, filepath.Join(root, "apps", "app.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: app
  namespace: `+controllerNamespaceTestNamespace+`
spec:
  project: team
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: workloads/app
  destination:
    name: in-cluster
    namespace: app
`)
	writeTestFile(t, filepath.Join(root, "workloads", "app", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: app\n")
}

func hasProjectMissing(diags []diagnostic.Diagnostic) bool {
	for _, diag := range diags {
		if strings.Contains(diag.Message, `references missing AppProject "team"`) {
			return true
		}
	}
	return false
}

// With --argocd-namespace, Argo CD reads its settings ConfigMaps, repository
// and cluster Secrets, and AppProjects only in that namespace. drydock skips
// the ones in other namespaces with a warning that --strict does not
// promote, and keeps the ones without a namespace. An Application of a
// skipped AppProject gets project.missing.
func TestArgoCDNamespaceFlagSkipsSettingsAndProjectsOutsideIt(t *testing.T) {
	root := t.TempDir()
	writeNamespacedSettingsRepo(t, root, "argocd")

	request := BuildRequest{Path: root, Parallelism: 1, ArgoCDNamespace: controllerNamespaceTestNamespace}
	result, err := Orchestrator{}.ListApplications(context.Background(), request)
	if err != nil {
		t.Fatalf("ListApplications() error = %v\nDiagnostics: %#v", err, result.Diagnostics)
	}
	if got := result.Settings.TrackingMethod.Value; got != "annotation" {
		t.Fatalf("TrackingMethod = %q, want the default: the argocd-cm in argocd does not count", got)
	}
	if got := result.Settings.InstanceLabelKey.Value; got != "example.com/instance" {
		t.Fatalf("InstanceLabelKey = %q, want the argocd-cm without a namespace", got)
	}
	if len(result.Settings.HelmRepositories) != 0 || len(result.Settings.Clusters) != 0 {
		t.Fatalf("repositories = %v, clusters = %v, want none from Secrets in argocd", result.Settings.HelmRepositories, result.Settings.Clusters)
	}
	names := make([]string, 0, len(result.Projects))
	for _, proj := range result.Projects {
		names = append(names, proj.Name)
	}
	if got := strings.Join(names, ","); got != "shared" {
		t.Fatalf("Projects = %s, want only shared", got)
	}
	skipped := diagnosticsWithCode(result.Diagnostics, outsideControllerNamespaceCode)
	if len(skipped) != 4 {
		t.Fatalf("%s diagnostics = %#v, want one each for argocd-cm, the two Secrets, and AppProject team", outsideControllerNamespaceCode, skipped)
	}
	for _, diag := range skipped {
		if !strings.Contains(diag.Message, `namespace "argocd"`) || !strings.Contains(diag.Message, `"`+controllerNamespaceTestNamespace+`"`) {
			t.Fatalf("diagnostic %q, want both namespaces named", diag.Message)
		}
		if !strictExemptDiagnostic(diag) {
			t.Fatalf("diagnostic %q fails --strict, want it exempt", diag.Message)
		}
	}

	build, err := Orchestrator{}.Build(context.Background(), request)
	if err != nil {
		t.Fatalf("Build() error = %v\nDiagnostics: %#v", err, build.Diagnostics)
	}
	if !hasProjectMissing(build.Diagnostics) {
		t.Fatalf("Diagnostics = %#v, want project.missing for team: Argo CD does not find it in argocd", build.Diagnostics)
	}
}

// Without --argocd-namespace, drydock filters nothing by namespace, also
// when an argocd-cmd-params-cm names the namespace, as earlier releases did.
func TestSettingsAndProjectsStayWithoutTheArgoCDNamespaceFlag(t *testing.T) {
	for name, params := range map[string]string{
		"no argocd-cmd-params-cm":                  "",
		"argocd-cmd-params-cm without a namespace": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: argocd-cmd-params-cm\ndata:\n  server.insecure: \"true\"\n",
		"argocd-cmd-params-cm in gitops":           commandParametersConfigMapYAML(controllerNamespaceTestNamespace, "  server.insecure: \"true\"\n"),
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if params != "" {
				writeTestFile(t, filepath.Join(root, "settings", "argocd-cmd-params-cm.yaml"), params)
			}
			writeNamespacedSettingsRepo(t, root, "argocd")

			result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root, Parallelism: 1})
			if err != nil {
				t.Fatalf("Build() error = %v\nDiagnostics: %#v", err, result.Diagnostics)
			}
			if got := result.Settings.TrackingMethod.Value; got != "label" {
				t.Fatalf("TrackingMethod = %q, want label from the argocd-cm in argocd", got)
			}
			if len(result.Settings.HelmRepositories) != 1 || len(result.Settings.Clusters) != 1 {
				t.Fatalf("repositories = %v, clusters = %v, want the Secrets in argocd", result.Settings.HelmRepositories, result.Settings.Clusters)
			}
			if hasProjectMissing(result.Diagnostics) {
				t.Fatalf("Diagnostics = %#v, want AppProject team found", result.Diagnostics)
			}
			if skipped := diagnosticsWithCode(result.Diagnostics, outsideControllerNamespaceCode); len(skipped) != 0 {
				t.Fatalf("diagnostics = %#v, want none", skipped)
			}
		})
	}
}

// When --argocd-namespace skips every AppProject, the repository still
// declares AppProjects, so an Application that names one gets
// project.missing, as Argo CD does not find it, and --strict fails.
func TestApplicationOfASkippedOnlyAppProjectIsMissing(t *testing.T) {
	root := t.TempDir()
	writeNamespacedSettingsRepo(t, root, "argocd")
	if err := os.Remove(filepath.Join(root, "projects", "shared.yaml")); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}

	request := BuildRequest{Path: root, Parallelism: 1, ArgoCDNamespace: controllerNamespaceTestNamespace}
	result, err := Orchestrator{}.Build(context.Background(), request)
	if err != nil {
		t.Fatalf("Build() error = %v\nDiagnostics: %#v", err, result.Diagnostics)
	}
	if len(result.Projects) != 0 {
		t.Fatalf("Projects = %v, want none", result.Projects)
	}
	if !hasProjectMissing(result.Diagnostics) {
		t.Fatalf("Diagnostics = %#v, want project.missing for team", result.Diagnostics)
	}
	request.Strict = true
	if _, err := (Orchestrator{}).Build(context.Background(), request); err == nil || !strings.Contains(err.Error(), `missing AppProject "team"`) {
		t.Fatalf("Build(--strict) error = %v, want project.missing", err)
	}
}

// The home-ops shape: a kustomization with namespace argocd renders the
// argo-cd chart through helmCharts, and its values set configs.params. The
// rendered argocd-cmd-params-cm names argocd, so the namespace gate stays
// on.
func TestKustomizeHelmChartsInstallKeepsTheNamespaceGate(t *testing.T) {
	root := t.TempDir()
	writeKustomizeArgoCDInstall(t, root, []string{"values.yaml"})
	writeTestFile(t, filepath.Join(root, "argocd", "values.yaml"), "configs:\n  params:\n    application.namespaces: team-*\n")
	writeControllerNamespaceAppNamed(t, root, "team", "team-a")
	writeControllerNamespaceAppNamed(t, root, "outside", "elsewhere")

	result := listArgoCDInstallSettings(t, root, argoCDInstallShape{}, fakeArgoCDChartAcquirer(t))
	if !result.Settings.ControllerNamespaceKnown() || result.Settings.ControllerNamespace("") != defaultArgoCDControllerNamespace {
		t.Fatalf("ControllerNamespace = %q (known %t), want argocd from the rendered argocd-cmd-params-cm", result.Settings.ControllerNamespace(""), result.Settings.ControllerNamespaceKnown())
	}
	names := applicationNames(result.Applications)
	if !slices.Contains(names, "team") || slices.Contains(names, "outside") {
		t.Fatalf("Applications = %v, want team kept and outside gated", names)
	}
	if warnings := controllerNamespaceWarnings(result.Diagnostics); len(warnings) != 0 {
		t.Fatalf("namespace warnings = %#v, want none", warnings)
	}
}

// A committed argocd-cmd-params-cm that only an Application on another
// cluster renders configures that cluster's Argo CD, so it never names the
// controller namespace. Without a renderer, it counts.
func TestCommittedCommandParametersOfAnotherClusterNameNoNamespace(t *testing.T) {
	for _, tc := range []struct {
		name     string
		renderer bool
		want     string
	}{
		{name: "rendered for another cluster", renderer: true, want: defaultArgoCDControllerNamespace},
		{name: "no renderer", want: "spoke-system"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, filepath.Join(root, "clusters", "spoke", "argocd-cmd-params-cm.yaml"), commandParametersConfigMapYAML("spoke-system", "  server.insecure: \"true\"\n"))
			if tc.renderer {
				writeTestFile(t, filepath.Join(root, "apps", "spoke.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: spoke
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: clusters/spoke
  destination:
    server: https://spoke.example.com
    namespace: spoke-system
`)
			}
			result, err := Orchestrator{}.ListApplications(context.Background(), BuildRequest{Path: root, Parallelism: 1})
			if err != nil {
				t.Fatalf("ListApplications() error = %v\nDiagnostics: %#v", err, result.Diagnostics)
			}
			if got := result.Settings.ControllerNamespace(""); got != tc.want {
				t.Fatalf("ControllerNamespace = %q, want %q", got, tc.want)
			}
		})
	}
}
