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

func fooFunction(name, kind, apiVersion string) string {
	return "apiVersion: " + apiVersion + "\nkind: " + kind + "\nmetadata:\n  name: " + name + "\n  annotations:\n    config.kubernetes.io/function: |\n      exec:\n        path: ./foo.sh\nspec:\n  value: from-config\n"
}

// A skipped plugin is still there for kustomize: a skipped generator adds
// its own configuration, marked local-config, which kustomize drops only at
// the end of the top-level build, and an entry keeps its kustomization from
// being empty. The outcomes are the ones of the kustomize v5.8.3 binary
// with --enable-alpha-plugins for the same files.
func TestKustomizeSkippedPluginsStayInTheBuild(t *testing.T) {
	cm := func(name string) string {
		return "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: " + name + "\ndata:\n  a: b\n"
	}
	for _, tc := range []struct {
		name      string
		files     map[string]string
		path      string
		options   []string
		wantErr   string
		wantNames string
		wantData  string
		wantLabel bool
	}{
		{name: "only a skipped transformer", path: "one", files: map[string]string{
			"one/kustomization.yaml": "transformers: [fn.yaml]\n", "one/fn.yaml": fooFunction("foo", "Foo", "example.com/v1"),
		}},
		{name: "component with only a skipped transformer", path: "ov", wantNames: "plain", files: map[string]string{
			"ov/kustomization.yaml": "resources: [cm.yaml]\ncomponents: [../comp]\n", "ov/cm.yaml": cm("plain"),
			"comp/kustomization.yaml": "apiVersion: kustomize.config.k8s.io/v1alpha1\nkind: Component\ntransformers: [fn.yaml]\n", "comp/fn.yaml": fooFunction("foo", "Foo", "example.com/v1"),
		}},
		{name: "two bases with the same skipped generator", path: "top", wantErr: "already registered id", files: map[string]string{
			"top/kustomization.yaml": "resources: [../b1, ../b2]\n",
			"b1/kustomization.yaml":  "resources: [cm.yaml]\ngenerators: [gen.yaml]\n", "b1/cm.yaml": cm("b1"), "b1/gen.yaml": fooFunction("secret-generator", "ksops", "viaduct.ai/v1"),
			"b2/kustomization.yaml": "resources: [cm.yaml]\ngenerators: [gen.yaml]\n", "b2/cm.yaml": cm("b2"), "b2/gen.yaml": fooFunction("secret-generator", "ksops", "viaduct.ai/v1"),
		}},
		{name: "skipped generator named like a resource", path: "c", wantErr: "ConfigMap", files: map[string]string{
			"c/kustomization.yaml": "resources: [cm.yaml]\ngenerators: [gen.yaml]\n", "c/cm.yaml": cm("plain"), "c/gen.yaml": fooFunction("plain", "ConfigMap", "v1"),
		}},
		{name: "replacement reads a skipped generator", path: "r", wantNames: "plain", wantData: "from-config", files: map[string]string{
			"r/kustomization.yaml": "resources: [cm.yaml]\ngenerators: [gen.yaml]\nreplacements:\n  - source:\n      kind: Foo\n      name: foo\n      fieldPath: spec.value\n    targets:\n      - select:\n          kind: ConfigMap\n          name: plain\n        fieldPaths: [data.a]\n",
			"r/cm.yaml":            cm("plain"), "r/gen.yaml": fooFunction("foo", "Foo", "example.com/v1"),
		}},
		{name: "same skipped generator twice", path: "d", wantErr: "already registered id", files: map[string]string{
			"d/kustomization.yaml": "resources: [cm.yaml]\ngenerators: [gen.yaml, gen.yaml]\n", "d/cm.yaml": cm("plain"), "d/gen.yaml": fooFunction("foo", "Foo", "example.com/v1"),
		}},
		// drydock does not expand a directory entry that holds a plugin
		// function, where kustomize skips the function (recorded
		// divergence).
		{name: "transformer directory with a function", path: "e", wantErr: "drydock does not expand directory entries that hold plugin functions", files: map[string]string{
			"e/kustomization.yaml": "resources: [cm.yaml]\ntransformers: [fns]\n", "e/cm.yaml": cm("plain"),
			"e/fns/kustomization.yaml": "resources: [fn.yaml]\n", "e/fns/fn.yaml": fooFunction("foo", "Foo", "example.com/v1"),
		}},
		{name: "generator directory", path: "e", wantErr: `kustomize generators entry "fns" is a kustomization directory; drydock does not expand directory entries of generators`, files: map[string]string{
			"e/kustomization.yaml": "resources: [cm.yaml]\ngenerators: [fns]\n", "e/cm.yaml": cm("plain"),
			"e/fns/kustomization.yaml": "resources: [fn.yaml]\n", "e/fns/fn.yaml": fooFunction("foo", "Foo", "example.com/v1"),
		}},
		// krusty builds a transformers directory of builtins as kustomize
		// accumulates it, without dropping local-config configurations.
		{name: "transformer directory of builtins", path: "e", wantNames: "plain", wantLabel: true, files: map[string]string{
			"e/kustomization.yaml": "resources: [cm.yaml]\ntransformers: [fns]\n", "e/cm.yaml": cm("plain"),
			"e/fns/kustomization.yaml": "resources: [label.yaml]\n", "e/fns/label.yaml": strings.Replace(builtinLabelTransformer, "  name: label\n", "  name: label\n  annotations:\n    config.kubernetes.io/local-config: \"true\"\n", 1),
		}},
		// kustomize fails a directory entry at or above the kustomization
		// with "cycle detected" (fileloader.go:286). drydock fails it
		// earlier, with another text: the plugin referent walk reaches the
		// kustomization again, and it sets more than resources.
		{name: "transformer directory of itself", path: "e", wantErr: "sets fields other than resources", files: map[string]string{
			"e/kustomization.yaml": "resources: [cm.yaml]\ntransformers: [.]\n", "e/cm.yaml": cm("plain"),
		}},
		{name: "transformer directory above", path: "top/e", wantErr: "sets fields other than resources", files: map[string]string{
			"top/kustomization.yaml":   "resources: [e]\n",
			"top/e/kustomization.yaml": "resources: [cm.yaml]\ntransformers: [..]\n", "top/e/cm.yaml": cm("plain"),
		}},
		{name: "transformer List", path: "l", wantNames: "plain", files: map[string]string{
			"l/kustomization.yaml": "resources: [cm.yaml]\ntransformers: [list.yaml]\n", "l/cm.yaml": cm("plain"),
			"l/list.yaml": "apiVersion: v1\nkind: List\nitems:\n  - apiVersion: builtin\n    kind: AnnotationsTransformer\n    metadata:\n      name: note\n    annotations:\n      note: listed\n    fieldSpecs:\n      - path: metadata/annotations\n        create: true\n",
		}},
		{name: "transformer outside the kustomization directory", path: "app", wantErr: "accumulation err='accumulating resources from '../shared/fn.yaml': security; file 'shared/fn.yaml' is not in or below 'app'", files: map[string]string{
			"app/kustomization.yaml": "resources: [cm.yaml]\ntransformers: [../shared/fn.yaml]\n", "app/cm.yaml": cm("plain"), "shared/fn.yaml": builtinLabelTransformer,
		}},
		{name: "generator outside the kustomization directory", path: "app", wantErr: "loading generator plugins: accumulation err='accumulating resources from '../shared/fn.yaml': security; file 'shared/fn.yaml' is not in or below 'app'", files: map[string]string{
			"app/kustomization.yaml": "resources: [cm.yaml]\ngenerators: [../shared/fn.yaml]\n", "app/cm.yaml": cm("plain"), "shared/fn.yaml": builtinConfigMapGenerator,
		}},
		{name: "transformer outside the directory without the restriction", path: "app", options: []string{"--load-restrictor", "LoadRestrictionsNone"}, wantNames: "plain", wantLabel: true, files: map[string]string{
			"app/kustomization.yaml": "resources: [cm.yaml]\ntransformers: [../shared/fn.yaml]\n", "app/cm.yaml": cm("plain"), "shared/fn.yaml": builtinLabelTransformer,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for path, content := range tc.files {
				writeFile(t, filepath.Join(root, path), content)
			}
			manifests, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: root, Path: tc.path}, RenderOptions{
				KustomizeCaller: KustomizeCallerArgoCD,
				BuildOptions:    append([]string{"--enable-alpha-plugins"}, tc.options...),
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
				if tc.wantData != "" {
					data, _ := manifest.Object.Object["data"].(map[string]any)
					if data["a"] != tc.wantData {
						t.Fatalf("data = %v, want a=%s", data, tc.wantData)
					}
				}
				if got := manifest.Object.GetLabels()["added"] == "true"; got != tc.wantLabel {
					t.Fatalf("labels = %v, want the builtin label %t", manifest.Object.GetLabels(), tc.wantLabel)
				}
				if tc.name == "transformer List" && manifest.Object.GetAnnotations()["note"] != "listed" {
					t.Fatalf("annotations = %v, want the List transformer applied", manifest.Object.GetAnnotations())
				}
			}
			if got := strings.Join(names, ","); got != tc.wantNames {
				t.Fatalf("names = %s, want %s", got, tc.wantNames)
			}
		})
	}
}

// Errors and origin annotations name the entries, not the files that
// drydock writes for them in its workspace.
func TestKustomizeRewrittenEntriesKeepTheirNames(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), "buildMetadata: [originAnnotations, transformerAnnotations]\nresources: [cm.yaml]\ngenerators: [plugin.yaml]\ntransformers: [tr.yaml]\n")
	writeFile(t, filepath.Join(root, "app", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: plain\n")
	writeFile(t, filepath.Join(root, "app", "plugin.yaml"), builtinConfigMapGenerator+"---\n"+fooExecFunction)
	writeFile(t, filepath.Join(root, "app", "tr.yaml"), builtinLabelTransformer+"---\n"+fooExecFunction)
	opts := RenderOptions{KustomizeCaller: KustomizeCallerArgoCD, BuildOptions: []string{"--enable-alpha-plugins"}}
	manifests, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: root, Path: "app"}, opts)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	for _, manifest := range manifests {
		for key, value := range manifest.Object.GetAnnotations() {
			if strings.Contains(value, ".drydock") {
				t.Fatalf("%s %s = %q, names a workspace file", manifest.Object.GetName(), key, value)
			}
		}
	}
	for _, manifest := range manifests {
		annotations := manifest.Object.GetAnnotations()
		if !strings.Contains(annotations["alpha.config.kubernetes.io/transformations"], "configuredIn: tr.yaml") {
			t.Fatalf("%s transformations = %q, want the entry tr.yaml", manifest.Object.GetName(), annotations["alpha.config.kubernetes.io/transformations"])
		}
		if manifest.Object.GetName() == "generated" && !strings.Contains(annotations["config.kubernetes.io/origin"], "configuredIn: plugin.yaml") {
			t.Fatalf("origin = %q, want the entry plugin.yaml", annotations["config.kubernetes.io/origin"])
		}
	}

	// Two copies of a skipped generator configuration collide. The error
	// names the entry.
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), "resources: [cm.yaml]\ngenerators: [fn.yaml, fn.yaml]\n")
	writeFile(t, filepath.Join(root, "app", "fn.yaml"), fooExecFunction)
	_, _, err = (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: root, Path: "app"}, opts)
	if err == nil || strings.Contains(err.Error(), ".drydock") || strings.Contains(err.Error(), "drydock-kustomize-") || !strings.Contains(err.Error(), "fn.yaml") {
		t.Fatalf("Render() error = %v, want it to name fn.yaml and no workspace path", err)
	}
}

// A function annotation that names neither a container nor an exec
// program gives kustomize nothing to run, also with both flags. The
// warning gives that reason, not a missing --enable-exec.
func TestKustomizeFunctionWithoutProgramWarning(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), "resources: [cm.yaml]\ngenerators: [fn.yaml]\n")
	writeFile(t, filepath.Join(root, "app", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: plain\n")
	writeFile(t, filepath.Join(root, "app", "fn.yaml"), "apiVersion: example.com/v1\nkind: Foo\nmetadata:\n  name: foo\n  annotations:\n    config.kubernetes.io/function: |\n      deferFailure: true\n")
	manifests, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: root, Path: "app"}, RenderOptions{
		KustomizeCaller: KustomizeCallerArgoCD,
		BuildOptions:    []string{"--enable-alpha-plugins", "--enable-exec"},
	})
	if err != nil || len(manifests) != 1 {
		t.Fatalf("Render() = %d manifests, error %v", len(manifests), err)
	}
	for _, diag := range diags {
		if diag.Code != KustomizeExecFunctionSkippedCode {
			continue
		}
		if !strings.Contains(diag.Message, "1 functions whose annotation names neither a container nor an exec program") || strings.Contains(diag.Message, "no --enable-exec") {
			t.Fatalf("warning = %q, want the reason without a missing --enable-exec", diag.Message)
		}
		return
	}
	t.Fatalf("diagnostics = %#v, want the skip warning", diags)
}

// kustomize applies the generator options of a skipped configuration that
// it adds; drydock does not, so it fails closed.
func TestKustomizeSkippedGeneratorWithOptionsFailsClosed(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), "resources: [cm.yaml]\ngenerators: [fn.yaml]\n")
	writeFile(t, filepath.Join(root, "app", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: plain\n")
	writeFile(t, filepath.Join(root, "app", "fn.yaml"), strings.Replace(fooExecFunction, "  annotations:\n", "  annotations:\n    kustomize.config.k8s.io/needs-hash: \"true\"\n", 1))
	_, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: root, Path: "app"}, RenderOptions{
		KustomizeCaller: KustomizeCallerArgoCD,
		BuildOptions:    []string{"--enable-alpha-plugins"},
	})
	if err == nil || !strings.Contains(err.Error(), `sets the generator option annotation "kustomize.config.k8s.io/needs-hash", which drydock does not apply`) {
		t.Fatalf("Render() error = %v, want the generator option failure", err)
	}
}
