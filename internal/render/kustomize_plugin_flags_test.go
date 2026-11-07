package render

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

const (
	fooExecFunction = `apiVersion: example.com/v1
kind: Foo
metadata:
  name: foo
  annotations:
    config.kubernetes.io/function: |
      exec:
        path: ./foo.sh
`
	fooContainerFunction = `apiVersion: example.com/v1
kind: Foo
metadata:
  name: foo
  annotations:
    config.kubernetes.io/function: |
      container:
        image: example.com/foo:v1
      exec:
        path: ./foo.sh
`
	fooBadFunction = `apiVersion: example.com/v1
kind: Foo
metadata:
  name: foo
  annotations:
    config.kubernetes.io/function: "bogus: 1"
`
	fooLegacyPlugin = `apiVersion: example.com/v1
kind: Foo
metadata:
  name: foo
`
	builtinLabelTransformer = `apiVersion: builtin
kind: LabelTransformer
metadata:
  name: label
labels:
  added: "true"
fieldSpecs:
  - path: metadata/labels
    create: true
`
	builtinConfigMapGenerator = `apiVersion: builtin
kind: ConfigMapGenerator
metadata:
  name: generated
literals:
  - a=b
options:
  disableNameSuffixHash: true
`
)

// Generators and transformers that are not builtin follow the plugin flags
// of the kustomize v5.8.3 binary. The texts and outcomes are the ones of
// the binary for the same kustomization. With --enable-alpha-plugins and
// no --enable-exec, kustomize skips every exec function: a generator gives
// nothing, and a transformer changes nothing.
func TestKustomizePluginFunctionsFollowBuildFlags(t *testing.T) {
	alpha := []string{"--enable-alpha-plugins"}
	both := []string{"--enable-alpha-plugins", "--enable-exec"}
	for _, tc := range []struct {
		name      string
		field     string
		plugin    string
		caller    KustomizeBuildCaller
		options   []string
		wantErr   string
		wantNames string
		wantLabel bool
		wantSkip  bool
	}{
		{name: "generator without argocd-cm", field: "generators", plugin: fooExecFunction, caller: KustomizeCallerArgoCDAssumed, wantErr: "generator example.com/v1/Foo is unsupported"},
		{name: "generator without alpha plugins", field: "generators", plugin: fooExecFunction, caller: KustomizeCallerArgoCD, wantErr: "loading generator plugins: failed to load generator: external plugins disabled; unable to load external plugin 'Foo'"},
		{name: "generator exec skipped", field: "generators", plugin: fooExecFunction, caller: KustomizeCallerArgoCD, options: alpha, wantNames: "plain", wantSkip: true},
		{name: "generator exec with both flags", field: "generators", plugin: fooExecFunction, caller: KustomizeCallerArgoCD, options: both, wantErr: "example.com/v1/Foo is an exec function, which drydock does not run"},
		{name: "generator container", field: "generators", plugin: fooContainerFunction, caller: KustomizeCallerArgoCD, options: alpha, wantErr: "is a container function"},
		{name: "generator bad annotation", field: "generators", plugin: fooBadFunction, caller: KustomizeCallerArgoCD, options: alpha, wantErr: `loading generator plugins: failed to load generator: loader: failed to get FunctionSpec: config.kubernetes.io/function unmarshal error`},
		{name: "generator legacy plugin", field: "generators", plugin: fooLegacyPlugin, caller: KustomizeCallerArgoCD, options: alpha, wantErr: "is a kustomize plugin program, which drydock does not run"},
		{name: "generator builtin and skipped exec", field: "generators", plugin: builtinConfigMapGenerator + "---\n" + fooExecFunction, caller: KustomizeCallerArgoCD, options: alpha, wantNames: "generated,plain", wantSkip: true},
		{name: "transformer without alpha plugins", field: "transformers", plugin: fooExecFunction, caller: KustomizeCallerArgoCD, wantErr: "external plugins disabled; unable to load external plugin 'Foo'"},
		{name: "transformer exec skipped", field: "transformers", plugin: fooExecFunction, caller: KustomizeCallerArgoCD, options: alpha, wantNames: "plain", wantSkip: true},
		{name: "transformer builtin and skipped exec", field: "transformers", plugin: builtinLabelTransformer + "---\n" + fooExecFunction, caller: KustomizeCallerArgoCD, options: alpha, wantNames: "plain", wantLabel: true, wantSkip: true},
		{name: "transformer exec with both flags", field: "transformers", plugin: fooExecFunction, caller: KustomizeCallerArgoCD, options: both, wantErr: "example.com/v1/Foo is an exec function, which drydock does not run"},
		{name: "transformer bad annotation", field: "transformers", plugin: fooBadFunction, caller: KustomizeCallerArgoCD, options: alpha, wantErr: "loader: failed to get FunctionSpec: config.kubernetes.io/function unmarshal error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), "resources: [cm.yaml]\n"+tc.field+": [plugin.yaml]\n")
			writeFile(t, filepath.Join(root, "app", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: plain\n")
			writeFile(t, filepath.Join(root, "app", "plugin.yaml"), tc.plugin)
			manifests, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: root, Path: "app"}, RenderOptions{
				KustomizeCaller: tc.caller,
				BuildOptions:    tc.options,
			})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Render() error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Render() error = %v", err)
			}
			names := make([]string, 0, len(manifests))
			for _, manifest := range manifests {
				names = append(names, manifest.Object.GetName())
				if got := manifest.Object.GetLabels()["added"] == "true"; got != tc.wantLabel {
					t.Fatalf("%s labels = %v, want the builtin label %t", manifest.Object.GetName(), manifest.Object.GetLabels(), tc.wantLabel)
				}
			}
			if got := strings.Join(names, ","); got != tc.wantNames {
				t.Fatalf("names = %s, want %s", got, tc.wantNames)
			}
			skipped := false
			for _, diag := range diags {
				skipped = skipped || diag.Code == KustomizeExecFunctionSkippedCode
			}
			if skipped != tc.wantSkip {
				t.Fatalf("diagnostics = %#v, want the exec skip warning %t", diags, tc.wantSkip)
			}
		})
	}
}
