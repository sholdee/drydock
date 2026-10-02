package render

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	goyaml "go.yaml.in/yaml/v3"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const pluginConfigTestConfigMap = `apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
`

func pluginConfigTestLabelPatch(value string) string {
	return `apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
  labels:
    patched-from: ` + value + `
`
}

func renderPluginConfigFixture(t *testing.T, root string) []Manifest {
	t.Helper()
	manifests, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     filepath.Join("apps", "demo"),
	}, RenderOptions{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	return manifests
}

func assertPatchedFrom(t *testing.T, manifests []Manifest, want string) {
	t.Helper()
	demo := findManifest(manifests, "ConfigMap", "demo")
	if demo == nil {
		t.Fatalf("rendered manifests = %#v, want ConfigMap demo", manifests)
	}
	if got, _, _ := unstructured.NestedString(demo.Object, "metadata", "labels", "patched-from"); got != want {
		t.Fatalf("ConfigMap demo label patched-from = %q, want %q", got, want)
	}
}

// TestKustomizeBuiltinPluginReferentsResolveAgainstListingDir pins the
// resolution base the collector relies on: kustomize configures builtin
// plugins with the LISTING kustomization's loader, so a config file at
// cfg/patch.yaml naming path: patch.yaml reads apps/demo/patch.yaml (next to
// the kustomization), not cfg/patch.yaml (the config itself).
func TestKustomizeBuiltinPluginReferentsResolveAgainstListingDir(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cm.yaml
transformers:
  - cfg/patch.yaml
`)
	writeFile(t, filepath.Join(root, "apps", "demo", "cm.yaml"), pluginConfigTestConfigMap)
	writeFile(t, filepath.Join(root, "apps", "demo", "cfg", "patch.yaml"), `apiVersion: builtin
kind: PatchTransformer
metadata:
  name: label-demo
path: patch.yaml
target:
  kind: ConfigMap
  name: demo
`)
	writeFile(t, filepath.Join(root, "apps", "demo", "patch.yaml"), pluginConfigTestLabelPatch("listing-dir"))

	assertPatchedFrom(t, renderPluginConfigFixture(t, root), "listing-dir")
}

// TestKustomizeBuiltinInlineStrategicMergePatchWithLeadingBlankLine pins the
// re-encode the referent walk reads a builtin config through. A
// PatchStrategicMergeTransformer paths: entry holding inline patch content
// that starts with a blank line is a block scalar inside a list; kustomize
// applies it, but at yaml.v3's default indent the re-encoded config carried
// an indentation indicator that disagreed with its content and did not
// decode. The strict digest walk then failed (the source was never cached)
// and changed-only selection dropped the file referent next to it, so an
// edit to patch.yaml selected nothing.
func TestKustomizeBuiltinInlineStrategicMergePatchWithLeadingBlankLine(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cm.yaml
transformers:
  - cfg/smp.yaml
`)
	writeFile(t, filepath.Join(root, "apps", "demo", "cm.yaml"), pluginConfigTestConfigMap)
	writeFile(t, filepath.Join(root, "apps", "demo", "patch.yaml"), pluginConfigTestLabelPatch("listing-dir"))
	writeFile(t, filepath.Join(root, "apps", "demo", "cfg", "smp.yaml"), `apiVersion: builtin
kind: PatchStrategicMergeTransformer
metadata:
  name: label-demo
paths:
  - patch.yaml
  - |

    apiVersion: v1
    kind: ConfigMap
    metadata:
      name: demo
      labels:
        inline: patched
`)

	manifests := renderPluginConfigFixture(t, root)
	assertPatchedFrom(t, manifests, "listing-dir")
	if got, _, _ := unstructured.NestedString(findManifest(manifests, "ConfigMap", "demo").Object, "metadata", "labels", "inline"); got != "patched" {
		t.Fatalf("ConfigMap demo label inline = %q, want the inline patch applied", got)
	}

	assertRequiredDigestPaths(t, pluginDigestPaths(t, root, "apps/demo"), "apps/demo/cfg/smp.yaml", "apps/demo/patch.yaml")
	selection, err := KustomizeSelectionPaths(context.Background(), root, "apps/demo", nil)
	if err != nil {
		t.Fatalf("KustomizeSelectionPaths() error = %v", err)
	}
	if !slices.Contains(selection, "apps/demo/patch.yaml") {
		t.Fatalf("KustomizeSelectionPaths() = %v, missing apps/demo/patch.yaml", selection)
	}
}

// TestKustomizeBuiltinGeneratorReferentsResolveAgainstListingDir pins the
// same base for generators: entries, which always render through the
// prepared workspace.
func TestKustomizeBuiltinGeneratorReferentsResolveAgainstListingDir(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
generators:
  - cfg/generator.yaml
`)
	writeFile(t, filepath.Join(root, "apps", "demo", "cfg", "generator.yaml"), `apiVersion: builtin
kind: ConfigMapGenerator
metadata:
  name: generated
options:
  disableNameSuffixHash: true
files:
  - data.txt
`)
	writeFile(t, filepath.Join(root, "apps", "demo", "data.txt"), "listing-dir")
	writeFile(t, filepath.Join(root, "apps", "demo", "cfg", "data.txt"), "config-dir")

	manifests := renderPluginConfigFixture(t, root)
	if !containsConfigMapData(manifests, "data.txt", "listing-dir") {
		t.Fatalf("rendered manifests = %#v, want data.txt read from the listing kustomization directory", manifests)
	}
}

// TestKustomizeBuiltinDirectoryEntryReferentsResolveAgainstListingDir pins
// that a kustomization-directory transformers: entry still resolves its
// configs' referents against the OUTER listing kustomization, not the entry
// directory.
func TestKustomizeBuiltinDirectoryEntryReferentsResolveAgainstListingDir(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cm.yaml
transformers:
  - ./cfg
`)
	writeFile(t, filepath.Join(root, "apps", "demo", "cm.yaml"), pluginConfigTestConfigMap)
	writeFile(t, filepath.Join(root, "apps", "demo", "cfg", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - transformer.yaml
`)
	writeFile(t, filepath.Join(root, "apps", "demo", "cfg", "transformer.yaml"), `apiVersion: builtin
kind: PatchTransformer
metadata:
  name: label-demo
path: patch.yaml
target:
  kind: ConfigMap
  name: demo
`)
	writeFile(t, filepath.Join(root, "apps", "demo", "cfg", "patch.yaml"), pluginConfigTestLabelPatch("entry-dir"))
	writeFile(t, filepath.Join(root, "apps", "demo", "patch.yaml"), pluginConfigTestLabelPatch("listing-dir"))

	assertPatchedFrom(t, renderPluginConfigFixture(t, root), "listing-dir")
}

func decodePluginConfigTestDocs(t *testing.T, content string) []*goyaml.Node {
	t.Helper()
	docs, err := decodeYAMLDocumentNodes([]byte(content))
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return docs
}

func TestKustomizePluginConfigRefs(t *testing.T) {
	for _, tt := range []struct {
		name    string
		config  string
		want    []kustomizePluginConfigRef
		wantErr string
	}{
		{
			name:   "PatchTransformer path",
			config: "apiVersion: builtin\nkind: PatchTransformer\npath: patch.yaml\n",
			want:   []kustomizePluginConfigRef{{Kind: "PatchTransformer", Field: "path", Path: "patch.yaml"}},
		},
		{
			// The plugins' Config decodes with sigs.k8s.io/yaml (JSON
			// semantics), so krusty honors a differently-cased key too.
			name:   "PatchTransformer key matches case-insensitively",
			config: "apiVersion: builtin\nkind: PatchTransformer\nPATH: patch.yaml\n",
			want:   []kustomizePluginConfigRef{{Kind: "PatchTransformer", Field: "path", Path: "patch.yaml"}},
		},
		{
			name:   "PatchTransformer inline patch only",
			config: "apiVersion: builtin\nkind: PatchTransformer\npatch: |\n  - op: add\n    path: /metadata/labels/a\n    value: b\n",
		},
		{
			// Kustomize reads a path exactly as written: " ops.yaml " names
			// the file " ops.yaml ", so the digest must too.
			name:   "PatchJson6902Transformer path kept as written",
			config: "apiVersion: builtin\nkind: PatchJson6902Transformer\npath: ' ops.yaml '\n",
			want:   []kustomizePluginConfigRef{{Kind: "PatchJson6902Transformer", Field: "path", Path: " ops.yaml "}},
		},
		{
			name:   "PatchStrategicMergeTransformer paths skip inline content",
			config: "apiVersion: builtin\nkind: PatchStrategicMergeTransformer\npaths:\n  - a.yaml\n  - |\n    apiVersion: v1\n    kind: ConfigMap\n    metadata:\n      name: demo\n  - ''\n",
			want:   []kustomizePluginConfigRef{{Kind: "PatchStrategicMergeTransformer", Field: "paths", Path: "a.yaml"}},
		},
		{
			name:   "ReplacementTransformer path and inline replacement",
			config: "apiVersion: builtin\nkind: ReplacementTransformer\nreplacements:\n  - path: replacement.yaml\n  - source:\n      kind: ConfigMap\n      name: demo\n    targets:\n      - select:\n          kind: Deployment\n        fieldPaths: [metadata.name]\n",
			want:   []kustomizePluginConfigRef{{Kind: "ReplacementTransformer", Field: "replacements.path", Path: "replacement.yaml"}},
		},
		{
			name:   "ConfigMapGenerator files envs without env",
			config: "apiVersion: builtin\nkind: ConfigMapGenerator\nmetadata:\n  name: demo\nfiles:\n  - key=data.txt\n  - other.txt\nenvs:\n  - a.env\nliterals:\n  - x=y\n",
			want: []kustomizePluginConfigRef{
				{Kind: "ConfigMapGenerator", Field: "files", Path: "data.txt"},
				{Kind: "ConfigMapGenerator", Field: "files", Path: "other.txt"},
				{Kind: "ConfigMapGenerator", Field: "envs", Path: "a.env"},
			},
		},
		{
			// Only FixKustomization merges env: into envs:, for
			// kustomization-level generators; a builtin config's env: is
			// never read.
			name:   "SecretGenerator env is not a referent",
			config: "apiVersion: builtin\nkind: SecretGenerator\nmetadata:\n  name: demo\nenv: secret.env\n",
		},
		{
			name:   "ValueAddTransformer targetFilePath",
			config: "apiVersion: builtin\nkind: ValueAddTransformer\nvalue: v\ntargetFilePath: targets.yaml\n",
			want:   []kustomizePluginConfigRef{{Kind: "ValueAddTransformer", Field: "targetFilePath", Path: "targets.yaml"}},
		},
		{
			name:   "no-referent kinds",
			config: "apiVersion: builtin\nkind: LabelTransformer\nlabels: {a: b}\n---\napiVersion: builtin\nkind: PrefixSuffixTransformer\nprefix: p-\n---\napiVersion: builtin\nkind: IAMPolicyGenerator\n",
		},
		{
			name:   "KSOPS documents are left to the KSOPS walk",
			config: ksopsTestGeneratorManifest,
		},
		{
			// Kustomize's resource factory drops null, {} and [] documents.
			name:   "empty documents are skipped",
			config: "{}\n---\n[]\n---\n~\n---\napiVersion: builtin\nkind: PatchTransformer\npath: one.yaml\n",
			want:   []kustomizePluginConfigRef{{Kind: "PatchTransformer", Field: "path", Path: "one.yaml"}},
		},
		{
			name:   "multiple documents",
			config: "apiVersion: builtin\nkind: PatchTransformer\npath: one.yaml\n---\n---\napiVersion: builtin\nkind: ValueAddTransformer\ntargetFilePath: two.yaml\n",
			want: []kustomizePluginConfigRef{
				{Kind: "PatchTransformer", Field: "path", Path: "one.yaml"},
				{Kind: "ValueAddTransformer", Field: "targetFilePath", Path: "two.yaml"},
			},
		},
		{
			name:    "HelmChartInflationGenerator",
			config:  "apiVersion: builtin\nkind: HelmChartInflationGenerator\nname: demo\n",
			wantErr: "HelmChartInflationGenerator is unsupported",
		},
		{
			name:    "unknown builtin kind",
			config:  "apiVersion: builtin\nkind: SortOrderTransformer\n",
			wantErr: `builtin plugin kind "SortOrderTransformer" is unknown`,
		},
		{
			name:    "non-builtin plugin",
			config:  "apiVersion: example.com/v1\nkind: Exec\nmetadata:\n  annotations:\n    config.kubernetes.io/function: |\n      exec:\n        path: ./plugin\n",
			wantErr: "plugin example.com/v1/Exec is not a kustomize builtin plugin",
		},
		{
			name:    "config that fails to parse",
			config:  "apiVersion: builtin\nkind: PatchTransformer\npath: [a.yaml]\n",
			wantErr: "decode builtin PatchTransformer config",
		},
		{
			name:    "non-mapping document",
			config:  "- a\n- b\n",
			wantErr: "not a mapping",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := kustomizePluginConfigRefs(decodePluginConfigTestDocs(t, tt.config))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("kustomizePluginConfigRefs() error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("kustomizePluginConfigRefs() error = %v", err)
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("kustomizePluginConfigRefs() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func pluginDigestPaths(t *testing.T, root, sourcePath string) map[string]bool {
	t.Helper()
	paths, err := KustomizeInputDigestPaths(context.Background(), ResolvedSource{RepoRoot: root, Path: sourcePath}, RenderOptions{})
	if err != nil {
		t.Fatalf("KustomizeInputDigestPaths() error = %v", err)
	}
	out := make(map[string]bool, len(paths))
	for _, path := range paths {
		out[path.Path] = path.Optional
	}
	return out
}

func assertRequiredDigestPaths(t *testing.T, paths map[string]bool, want ...string) {
	t.Helper()
	for _, path := range want {
		optional, ok := paths[path]
		if !ok {
			t.Errorf("digest paths %v missing %q — editing it would not rotate the render cache key", paths, path)
			continue
		}
		if optional {
			t.Errorf("digest path %q is optional, want required", path)
		}
	}
}

func assertDigestPathsExclude(t *testing.T, paths map[string]bool, unwanted ...string) {
	t.Helper()
	for _, path := range unwanted {
		if _, ok := paths[path]; ok {
			t.Errorf("digest paths %v contain %q", paths, path)
		}
	}
}

const pluginConfigTestPatchTransformer = `apiVersion: builtin
kind: PatchTransformer
metadata:
  name: label-demo
path: patch.yaml
target:
  kind: ConfigMap
`

// TestKustomizeInputDigestCoversBuiltinPluginConfigReferents pins that every
// entry shape digests its builtin configs' referents, resolved against the
// listing kustomization directory: a same-named file next to the config (or
// inside a directory entry) must not stand in for it.
func TestKustomizeInputDigestCoversBuiltinPluginConfigReferents(t *testing.T) {
	for _, tt := range []struct {
		name     string
		entries  string
		files    map[string]string
		want     []string
		excluded []string
	}{
		{
			name:    "transformers file entry",
			entries: "transformers:\n  - cfg/transformer.yaml\n",
			files: map[string]string{
				"cfg/transformer.yaml": pluginConfigTestPatchTransformer,
				"cfg/patch.yaml":       pluginConfigTestLabelPatch("config-dir"),
			},
			want:     []string{"apps/demo/cfg/transformer.yaml", "apps/demo/patch.yaml"},
			excluded: []string{"apps/demo/cfg/patch.yaml"},
		},
		{
			name:    "transformers inline entry",
			entries: "transformers:\n  - |\n    apiVersion: builtin\n    kind: PatchStrategicMergeTransformer\n    metadata:\n      name: inline\n    paths:\n      - patch.yaml\n",
			want:    []string{"apps/demo/patch.yaml"},
		},
		{
			name:    "validators file entry",
			entries: "validators:\n  - cfg/validator.yaml\n",
			files: map[string]string{
				"cfg/validator.yaml": "apiVersion: builtin\nkind: ReplacementTransformer\nmetadata:\n  name: v\nreplacements:\n  - path: replacement.yaml\n",
				"replacement.yaml":   "source:\n  kind: ConfigMap\n  name: demo\ntargets: []\n",
			},
			want: []string{"apps/demo/cfg/validator.yaml", "apps/demo/replacement.yaml"},
		},
		{
			name:    "generators file entry",
			entries: "generators:\n  - cfg/generator.yaml\n",
			files: map[string]string{
				"cfg/generator.yaml": "apiVersion: builtin\nkind: ConfigMapGenerator\nmetadata:\n  name: generated\nfiles:\n  - key=data.txt\nenvs:\n  - vars.env\n",
				"data.txt":           "listing-dir",
				"vars.env":           "A=B\n",
				"cfg/data.txt":       "config-dir",
			},
			want:     []string{"apps/demo/cfg/generator.yaml", "apps/demo/data.txt", "apps/demo/vars.env"},
			excluded: []string{"apps/demo/cfg/data.txt"},
		},
		{
			name:    "generators inline entry",
			entries: "generators:\n  - |\n    apiVersion: builtin\n    kind: SecretGenerator\n    metadata:\n      name: inline\n    envs: [secret.env]\n",
			files:   map[string]string{"secret.env": "A=B\n"},
			want:    []string{"apps/demo/secret.env"},
		},
		{
			// A builtin generator's env: is never read, so a missing file
			// must not fail the strict digest (the source stays
			// persistence-eligible) nor be owned by selection.
			name:    "generators builtin env is not a referent",
			entries: "generators:\n  - cfg/generator.yaml\n",
			files: map[string]string{
				"cfg/generator.yaml": "apiVersion: builtin\nkind: ConfigMapGenerator\nmetadata:\n  name: generated\nenv: missing.env\nliterals:\n  - a=b\n",
			},
			want:     []string{"apps/demo/cfg/generator.yaml"},
			excluded: []string{"apps/demo/missing.env"},
		},
		{
			name:    "ValueAddTransformer targetFilePath",
			entries: "transformers:\n  - cfg/value-add.yaml\n",
			files: map[string]string{
				"cfg/value-add.yaml": "apiVersion: builtin\nkind: ValueAddTransformer\nmetadata:\n  name: v\nvalue: x\ntargetFilePath: targets.yaml\n",
				"targets.yaml":       "targets: []\n",
			},
			want: []string{"apps/demo/cfg/value-add.yaml", "apps/demo/targets.yaml"},
		},
		{
			// No same-named decoy inside the entry: the entry directory
			// is digested whole, so it could never be excluded.
			// TestKustomizeBuiltinDirectoryEntryReferentsResolveAgainstListingDir
			// pins the resolution base instead.
			name:    "transformers directory entry",
			entries: "transformers:\n  - ./cfg\n",
			files: map[string]string{
				"cfg/kustomization.yaml":        "resources:\n  - nested\n",
				"cfg/nested/kustomization.yaml": "resources:\n  - transformer.yaml\n",
				"cfg/nested/transformer.yaml":   pluginConfigTestPatchTransformer,
			},
			want: []string{"apps/demo/cfg", "apps/demo/cfg/nested/kustomization.yaml", "apps/demo/cfg/nested/transformer.yaml", "apps/demo/patch.yaml"},
		},
		{
			// An empty list builds like an absent field, so the entry
			// stays enumerable.
			name:    "transformers directory entry with empty fields",
			entries: "transformers:\n  - ./cfg\n",
			files: map[string]string{
				"cfg/kustomization.yaml": "resources:\n  - transformer.yaml\npatches: []\ncommonAnnotations: {}\n",
				"cfg/transformer.yaml":   pluginConfigTestPatchTransformer,
			},
			want: []string{"apps/demo/cfg", "apps/demo/cfg/transformer.yaml", "apps/demo/patch.yaml"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), "resources:\n  - cm.yaml\n"+tt.entries)
			writeFile(t, filepath.Join(root, "apps", "demo", "cm.yaml"), pluginConfigTestConfigMap)
			writeFile(t, filepath.Join(root, "apps", "demo", "patch.yaml"), pluginConfigTestLabelPatch("listing-dir"))
			for name, content := range tt.files {
				writeFile(t, filepath.Join(root, "apps", "demo", filepath.FromSlash(name)), content)
			}

			paths := pluginDigestPaths(t, root, "apps/demo")
			assertRequiredDigestPaths(t, paths, tt.want...)
			assertDigestPathsExclude(t, paths, tt.excluded...)

			selection, err := KustomizeSelectionPaths(context.Background(), root, "apps/demo", nil)
			if err != nil {
				t.Fatalf("KustomizeSelectionPaths() error = %v", err)
			}
			for _, want := range tt.want {
				if !slices.Contains(selection, want) {
					t.Errorf("KustomizeSelectionPaths() = %v, missing %q", selection, want)
				}
			}
			for _, unwanted := range tt.excluded {
				if slices.Contains(selection, unwanted) {
					t.Errorf("KustomizeSelectionPaths() = %v, owns %q", selection, unwanted)
				}
			}
		})
	}
}

// TestKustomizeInputDigestFailsClosedOnUnenumerablePluginConfigs pins the
// strict/best-effort split: a config whose referents cannot be enumerated
// fails the digest walk (the source becomes persistence-ineligible) while
// changed-only selection skips it and still owns the entry itself.
func TestKustomizeInputDigestFailsClosedOnUnenumerablePluginConfigs(t *testing.T) {
	const pinned = "0123456789abcdef0123456789abcdef01234567"
	for _, tt := range []struct {
		name    string
		entries string
		files   map[string]string
		wantErr string
		owned   []string
	}{
		{
			name:    "non-builtin plugin",
			entries: "transformers:\n  - cfg/exec.yaml\n",
			files:   map[string]string{"cfg/exec.yaml": "apiVersion: example.com/v1\nkind: Exec\nmetadata:\n  name: exec\n"},
			wantErr: "not a kustomize builtin plugin",
			owned:   []string{"apps/demo/cfg/exec.yaml"},
		},
		{
			name:    "HelmChartInflationGenerator",
			entries: "generators:\n  - cfg/helm.yaml\n",
			files:   map[string]string{"cfg/helm.yaml": "apiVersion: builtin\nkind: HelmChartInflationGenerator\nmetadata:\n  name: helm\n"},
			wantErr: "HelmChartInflationGenerator is unsupported",
			owned:   []string{"apps/demo/cfg/helm.yaml"},
		},
		{
			name:    "pinned remote entry",
			entries: "transformers:\n  - https://github.com/example/tools.git//transformer.yaml?ref=" + pinned + "\n",
			wantErr: "is a remote plugin config",
		},
		{
			name:    "missing referent",
			entries: "transformers:\n  - cfg/transformer.yaml\n",
			files:   map[string]string{"cfg/transformer.yaml": "apiVersion: builtin\nkind: PatchTransformer\nmetadata:\n  name: p\npath: missing.yaml\n"},
			wantErr: "missing.yaml",
			owned:   []string{"apps/demo/cfg/transformer.yaml"},
		},
		{
			name:    "directory referent",
			entries: "transformers:\n  - cfg/transformer.yaml\n",
			files:   map[string]string{"cfg/transformer.yaml": "apiVersion: builtin\nkind: PatchTransformer\nmetadata:\n  name: p\npath: .\n"},
			wantErr: "is a directory",
			owned:   []string{"apps/demo/cfg/transformer.yaml"},
		},
		{
			name:    "directory entry with more than resources",
			entries: "transformers:\n  - ./cfg\n",
			files: map[string]string{
				"cfg/kustomization.yaml": "namePrefix: x-\nresources:\n  - transformer.yaml\n",
				"cfg/transformer.yaml":   pluginConfigTestPatchTransformer,
			},
			wantErr: "sets fields other than resources",
			owned:   []string{"apps/demo/cfg"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), "resources:\n  - cm.yaml\n"+tt.entries)
			writeFile(t, filepath.Join(root, "apps", "demo", "cm.yaml"), pluginConfigTestConfigMap)
			writeFile(t, filepath.Join(root, "apps", "demo", "patch.yaml"), pluginConfigTestLabelPatch("listing-dir"))
			for name, content := range tt.files {
				writeFile(t, filepath.Join(root, "apps", "demo", filepath.FromSlash(name)), content)
			}

			_, err := KustomizeInputDigestPaths(context.Background(), ResolvedSource{RepoRoot: root, Path: "apps/demo"}, RenderOptions{})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("KustomizeInputDigestPaths() error = %v, want %q", err, tt.wantErr)
			}
			selection, err := KustomizeSelectionPaths(context.Background(), root, "apps/demo", nil)
			if err != nil {
				t.Fatalf("KustomizeSelectionPaths() error = %v", err)
			}
			for _, want := range append([]string{"apps/demo/kustomization.yaml", "apps/demo/cm.yaml"}, tt.owned...) {
				if !slices.Contains(selection, want) {
					t.Errorf("KustomizeSelectionPaths() = %v, missing %q", selection, want)
				}
			}
			for _, path := range selection {
				if path == "" || path == "." || path == "apps/demo" {
					t.Errorf("KustomizeSelectionPaths() = %v, owns the listing directory via %q", selection, path)
				}
			}
		})
	}
}

// TestKustomizeSelectionPathsKeepsEnumerableDocumentsOfMixedConfig pins that
// best-effort selection skips one unenumerable document, not the whole entry.
func TestKustomizeSelectionPathsKeepsEnumerableDocumentsOfMixedConfig(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), "resources:\n  - cm.yaml\ntransformers:\n  - cfg/transformers.yaml\n")
	writeFile(t, filepath.Join(root, "apps", "demo", "cm.yaml"), pluginConfigTestConfigMap)
	writeFile(t, filepath.Join(root, "apps", "demo", "patch.yaml"), pluginConfigTestLabelPatch("listing-dir"))
	writeFile(t, filepath.Join(root, "apps", "demo", "cfg", "transformers.yaml"), "apiVersion: example.com/v1\nkind: Exec\nmetadata:\n  name: exec\n---\n"+pluginConfigTestPatchTransformer)

	selection, err := KustomizeSelectionPaths(context.Background(), root, "apps/demo", nil)
	if err != nil {
		t.Fatalf("KustomizeSelectionPaths() error = %v", err)
	}
	for _, want := range []string{"apps/demo/cfg/transformers.yaml", "apps/demo/patch.yaml"} {
		if !slices.Contains(selection, want) {
			t.Errorf("KustomizeSelectionPaths() = %v, missing %q", selection, want)
		}
	}
}

// TestKustomizeSelectionPathsRepoRootPluginConfigsOwnNoRoot pins that at the
// repository root, an empty referent (an inline replacement's path) joined
// with the listing directory would normalize to "" and intersect every
// changed path.
func TestKustomizeSelectionPathsRepoRootPluginConfigsOwnNoRoot(t *testing.T) {
	root := t.TempDir()
	// The inline entry stays under 255 bytes: graph validation still stats
	// inline transformers as paths (recorded out of scope).
	writeFile(t, filepath.Join(root, "kustomization.yaml"), `resources:
  - cm.yaml
transformers:
  - |
    apiVersion: builtin
    kind: ReplacementTransformer
    metadata: {name: r}
    replacements:
    - source: {kind: ConfigMap, name: demo}
      targets:
      - select: {kind: ConfigMap}
        fieldPaths: [data.copy]
        options: {create: true}
generators:
  - generator.yaml
`)
	writeFile(t, filepath.Join(root, "cm.yaml"), pluginConfigTestConfigMap)
	writeFile(t, filepath.Join(root, "generator.yaml"), "apiVersion: builtin\nkind: ConfigMapGenerator\nmetadata:\n  name: generated\nfiles:\n  - data.txt\n")
	writeFile(t, filepath.Join(root, "data.txt"), "data")

	selection, err := KustomizeSelectionPaths(context.Background(), root, ".", nil)
	if err != nil {
		t.Fatalf("KustomizeSelectionPaths() error = %v", err)
	}
	for _, path := range selection {
		if path == "" || path == "." {
			t.Fatalf("KustomizeSelectionPaths() = %q, contains repository root path %q", selection, path)
		}
	}
	if !slices.Contains(selection, "data.txt") || !slices.Contains(selection, "generator.yaml") {
		t.Fatalf("KustomizeSelectionPaths() = %v, want generator.yaml and data.txt", selection)
	}
	paths := pluginDigestPaths(t, root, ".")
	assertDigestPathsExclude(t, paths, "", ".")
	assertRequiredDigestPaths(t, paths, "generator.yaml", "data.txt")
}
