package app

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/gitref"
	sourcepkg "github.com/sholdee/drydock/internal/source"
)

// renderOutcomeSignature reduces a render to a comparable string: an error
// (any error) is one outcome class; otherwise the JSON of the manifest objects
// and diagnostics in order. Diagnostics are included so that diagnostic-only
// changes (e.g. a new warning emitted by a mutated file) count as outcome
// changes and trigger the coverage check.
func renderOutcomeSignature(t *testing.T, result RenderResult, err error) string {
	t.Helper()
	if err != nil {
		return "error"
	}
	objects := make([]map[string]any, 0, len(result.Manifests))
	for _, manifest := range result.Manifests {
		if manifest.Object != nil {
			objects = append(objects, manifest.Object.Object)
		}
	}
	type outcomePayload struct {
		Objects     []map[string]any `json:"objects"`
		Diagnostics any              `json:"diagnostics"`
	}
	data, marshalErr := json.Marshal(outcomePayload{
		Objects:     objects,
		Diagnostics: result.Diagnostics,
	})
	if marshalErr != nil {
		t.Fatalf("marshal render outcome: %v", marshalErr)
	}
	return string(data)
}

func digestPathsCover(paths []gitref.PathDigestPath, rel string) bool {
	for _, item := range paths {
		if item.Path == "." || item.Path == rel || strings.HasPrefix(rel, item.Path+"/") {
			return true
		}
	}
	return false
}

func renderCoverageFixtureProvider(repoRoot string) localProvider {
	return localProvider{
		repoRoot:       repoRoot,
		sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{}),
		rootIdentity:   SourceIdentity{Kind: sourceIdentityKindRoot},
		rootInputMode:  rootInputModeDirty,
		cacheEvents:    cacheevent.NewRecorder(false),
		acquisitions:   cacheevent.NewAcquisitionCollector(),
	}
}

// assertRenderInputCoverage mutates every file under repoRoot (except .git)
// to garbage, one at a time, and asserts that any file whose mutation changes
// the render outcome is covered by the digest path set computed from the
// pristine tree. Files whose mutation does not change the outcome did not
// change the outcome under this probe and are legitimately uncovered.
//
// Fixture constraint: files under the source path must contribute to the
// baseline output for the probe to be potent. The garbage payload has no
// apiVersion/kind, and the directory renderer silently skips non-manifest
// decode failures — a file that only produces decode errors when corrupted
// will not register as an outcome change.
func assertRenderInputCoverage(t *testing.T, repoRoot string, application argoappv1.Application, pluginOptions ...PluginOptions) {
	t.Helper()
	provider := renderCoverageFixtureProvider(repoRoot)
	plan := mustPlan(t, application)

	// Use the prepared plan to mirror production: PrepareSource merges
	// .argocd-source.yaml into the source spec before rendering, so digestPaths
	// must be computed from the same prepared state.
	preparedPlan, _, prepErr := preparePlanSourcesForRender(context.Background(), application, provider, plan)
	if prepErr != nil {
		t.Fatalf("preparePlanSourcesForRender() error = %v", prepErr)
	}

	digestPaths, _, err := localInputDigestPathsForSource(context.Background(), preparedPlan, preparedPlan.Sources[0], provider)
	if err != nil {
		t.Fatalf("localInputDigestPathsForSource() error = %v", err)
	}

	baselineResult, baselineErr := RenderApplication(context.Background(), application, provider, pluginOptions...)
	if baselineErr != nil {
		t.Fatalf("baseline render must succeed, got %v", baselineErr)
	}
	baseline := renderOutcomeSignature(t, baselineResult, baselineErr)

	var files []string
	walkErr := filepath.WalkDir(repoRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		files = append(files, path)
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk fixture: %v", walkErr)
	}

	for _, file := range files {
		rel, err := filepath.Rel(repoRoot, file)
		if err != nil {
			t.Fatalf("rel %q: %v", file, err)
		}
		rel = filepath.ToSlash(rel)

		original, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %q: %v", file, err)
		}
		if err := os.WriteFile(file, []byte("}{ tripwire-garbage: ["), 0o600); err != nil {
			t.Fatalf("mutate %q: %v", file, err)
		}

		result, renderErr := RenderApplication(context.Background(), application, renderCoverageFixtureProvider(repoRoot), pluginOptions...)
		mutated := renderOutcomeSignature(t, result, renderErr)

		if err := os.WriteFile(file, original, 0o600); err != nil {
			t.Fatalf("restore %q: %v", file, err)
		}

		if mutated != baseline && !digestPathsCover(digestPaths, rel) {
			t.Errorf("file %q changes the render outcome when corrupted but is NOT covered by the digest path set %#v — a cache entry would stay valid while the render changed", rel, digestPaths)
		}
	}
}

func TestRenderInputCoverageDirectorySource(t *testing.T) {
	repoRoot := t.TempDir()
	writeTestFile(t, repoRoot+"/manifests/demo/cm.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\ndata:\n  value: one\n")
	writeTestFile(t, repoRoot+"/manifests/demo/extra.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: extra\n")
	writeTestFile(t, repoRoot+"/unrelated/README.md", "not a render input\n")
	application := argoappv1.Application{
		Name: "demo", Namespace: "argocd",
		Spec: argoappv1.ApplicationSpec{
			Source: &argoappv1.ApplicationSource{
				RepoURL: "https://git.example.test/org/repo.git", Path: "manifests/demo", TargetRevision: "main",
				Directory: &argoappv1.ApplicationSourceDirectory{},
			},
			Destination: argoappv1.ApplicationDestination{Namespace: "default"},
		},
	}
	assertRenderInputCoverage(t, repoRoot, application)
}

func TestRenderInputCoverageKustomizeSourceWithBaseAndOverride(t *testing.T) {
	repoRoot := t.TempDir()
	writeTestFile(t, repoRoot+"/base/kustomization.yaml", "resources:\n  - cm.yaml\n")
	writeTestFile(t, repoRoot+"/base/cm.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: base\ndata:\n  value: one\n")
	writeTestFile(t, repoRoot+"/manifests/app/kustomization.yaml", "resources:\n  - ../../base\n")
	writeTestFile(t, repoRoot+"/manifests/app/.argocd-source.yaml", "kustomize:\n  namePrefix: prod-\n")
	writeTestFile(t, repoRoot+"/unrelated/README.md", "not a render input\n")
	application := argoappv1.Application{
		Name: "app", Namespace: "argocd",
		Spec: argoappv1.ApplicationSpec{
			Source: &argoappv1.ApplicationSource{
				RepoURL: "https://git.example.test/org/repo.git", Path: "manifests/app", TargetRevision: "main",
			},
			Destination: argoappv1.ApplicationDestination{Namespace: "default"},
		},
	}
	assertRenderInputCoverage(t, repoRoot, application)
}

func TestRenderInputCoverageHelmSource(t *testing.T) {
	repoRoot := t.TempDir()
	writeTestFile(t, repoRoot+"/charts/demo/Chart.yaml", "apiVersion: v2\nname: demo\nversion: 0.1.0\n")
	writeTestFile(t, repoRoot+"/charts/demo/values.yaml", "value: one\n")
	writeTestFile(t, repoRoot+"/charts/demo/templates/cm.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: {{ .Chart.Name }}\ndata:\n  value: {{ .Values.value }}\n")
	writeTestFile(t, repoRoot+"/unrelated/README.md", "not a render input\n")
	application := argoappv1.Application{
		Name: "demo", Namespace: "argocd",
		Spec: argoappv1.ApplicationSpec{
			Source: &argoappv1.ApplicationSource{
				RepoURL: "https://git.example.test/org/repo.git", Path: "charts/demo", TargetRevision: "main",
				Helm: &argoappv1.ApplicationSourceHelm{ValueFiles: []string{"values.yaml"}},
			},
			Destination: argoappv1.ApplicationDestination{Namespace: "default"},
		},
	}
	assertRenderInputCoverage(t, repoRoot, application)
}

// TestRenderInputCoverageKustomizeKSOPSGenerator pins that the sops files
// referenced by a KSOPS generator manifest (including cross-directory targets
// above the source path) AND by an inline KSOPS generators: entry are covered
// by the persistent render cache digest — a sops edit must rotate the render
// cache key under --enable-ksops-compat.
func TestRenderInputCoverageKustomizeKSOPSGenerator(t *testing.T) {
	repoRoot := t.TempDir()
	writeTestFile(t, repoRoot+"/manifests/app/kustomization.yaml", "generators:\n  - ./secret-generator.yaml\n  - |\n    apiVersion: viaduct.ai/v1\n    kind: ksops\n    metadata:\n      name: inline-secret-generator\n    files:\n      - ./inline-secret.sops.yaml\n")
	writeTestFile(t, repoRoot+"/manifests/app/secret-generator.yaml", "apiVersion: viaduct.ai/v1\nkind: ksops\nmetadata:\n  name: demo-secret-generator\nfiles:\n  - ./demo-secret.sops.yaml\n  - ../../secrets/shared.sops.yaml\n")
	writeTestFile(t, repoRoot+"/manifests/app/demo-secret.sops.yaml", "apiVersion: v1\nkind: Secret\nmetadata:\n    name: demo-secret\ndata:\n    TOKEN: ENC[AES256_GCM,data:abc123,type:str]\nsops:\n    version: 3.10.2\n")
	writeTestFile(t, repoRoot+"/manifests/app/inline-secret.sops.yaml", "apiVersion: v1\nkind: Secret\nmetadata:\n    name: inline-secret\ndata:\n    INLINE: ENC[AES256_GCM,data:ghi789,type:str]\nsops:\n    version: 3.10.2\n")
	writeTestFile(t, repoRoot+"/secrets/shared.sops.yaml", "apiVersion: v1\nkind: Secret\nmetadata:\n    name: shared-secret\ndata:\n    SHARED: ENC[AES256_GCM,data:def456,type:str]\nsops:\n    version: 3.10.2\n")
	writeTestFile(t, repoRoot+"/unrelated/README.md", "not a render input\n")
	application := argoappv1.Application{
		Name: "app", Namespace: "argocd",
		Spec: argoappv1.ApplicationSpec{
			Source: &argoappv1.ApplicationSource{
				RepoURL: "https://git.example.test/org/repo.git", Path: "manifests/app", TargetRevision: "main",
			},
			Destination: argoappv1.ApplicationDestination{Namespace: "default"},
		},
	}
	assertRenderInputCoverage(t, repoRoot, application, PluginOptions{EnableKSOPSCompat: true})
}

// TestRenderInputCoverageHelmSourceOutOfChartDirValueFile pins that a valueFile
// and a fileParameter whose paths traverse above the chart directory (but remain
// within the repository root) are reported as local input paths and therefore
// covered by the persistent render cache digest.  If either file's mutation
// changes the render outcome without being covered, the cache would serve stale
// content — this test catches that regression.
func TestRenderInputCoverageHelmSourceOutOfChartDirValueFile(t *testing.T) {
	repoRoot := t.TempDir()
	writeTestFile(t, repoRoot+"/charts/demo/Chart.yaml", "apiVersion: v2\nname: demo\nversion: 0.1.0\n")
	writeTestFile(t, repoRoot+"/charts/demo/values.yaml", "value: from-default\nfileValue: from-default\n")
	writeTestFile(t, repoRoot+"/charts/demo/templates/cm.yaml",
		"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: {{ .Chart.Name }}\ndata:\n  value: {{ .Values.value }}\n  fileValue: {{ .Values.fileValue | quote }}\n")
	writeTestFile(t, repoRoot+"/values/shared.yaml", "value: from-shared\n")
	writeTestFile(t, repoRoot+"/shared-files/message.txt", "from-file\n")
	writeTestFile(t, repoRoot+"/unrelated/README.md", "not a render input\n")
	application := argoappv1.Application{
		Name: "demo", Namespace: "argocd",
		Spec: argoappv1.ApplicationSpec{
			Source: &argoappv1.ApplicationSource{
				RepoURL: "https://git.example.test/org/repo.git", Path: "charts/demo", TargetRevision: "main",
				Helm: &argoappv1.ApplicationSourceHelm{
					ValueFiles: []string{"../../values/shared.yaml"},
					FileParameters: []argoappv1.HelmFileParameter{
						{Name: "fileValue", Path: "../../shared-files/message.txt"},
					},
				},
			},
			Destination: argoappv1.ApplicationDestination{Namespace: "default"},
		},
	}
	assertRenderInputCoverage(t, repoRoot, application)
}

const renderCoverageDemoConfigMap = "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\ndata:\n  value: base\n"

const renderCoveragePatchedConfigMap = "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\ndata:\n  value: patched\n"

// TestRenderInputCoverageKustomizeBuiltinPluginConfigReferents pins that the
// files builtin plugin configs read through the listing kustomization's
// loader are covered by the persistent render cache digest, for every kind
// with referents and for inline and directory entries. The referents sit next
// to the listing kustomization, outside the config's own directory, which is
// where kustomize resolves them.
func TestRenderInputCoverageKustomizeBuiltinPluginConfigReferents(t *testing.T) {
	const transformerEntry = "resources:\n  - cm.yaml\ntransformers:\n  - cfg/transformer.yaml\n"
	for _, tt := range []struct {
		name  string
		files map[string]string
	}{
		{
			name: "PatchTransformer path",
			files: map[string]string{
				"kustomization.yaml":   transformerEntry,
				"cfg/transformer.yaml": "apiVersion: builtin\nkind: PatchTransformer\nmetadata:\n  name: patch\npath: patch.yaml\ntarget:\n  kind: ConfigMap\n  name: demo\n",
				"patch.yaml":           renderCoveragePatchedConfigMap,
			},
		},
		{
			name: "PatchJson6902Transformer path",
			files: map[string]string{
				"kustomization.yaml":   transformerEntry,
				"cfg/transformer.yaml": "apiVersion: builtin\nkind: PatchJson6902Transformer\nmetadata:\n  name: ops\ntarget:\n  version: v1\n  kind: ConfigMap\n  name: demo\npath: ops.yaml\n",
				"ops.yaml":             "- op: replace\n  path: /data/value\n  value: patched\n",
			},
		},
		{
			name: "PatchStrategicMergeTransformer paths",
			files: map[string]string{
				"kustomization.yaml":   transformerEntry,
				"cfg/transformer.yaml": "apiVersion: builtin\nkind: PatchStrategicMergeTransformer\nmetadata:\n  name: smp\npaths:\n  - patch.yaml\n",
				"patch.yaml":           renderCoveragePatchedConfigMap,
			},
		},
		{
			name: "ReplacementTransformer replacements path",
			files: map[string]string{
				"kustomization.yaml":   "resources:\n  - cm.yaml\n  - source.yaml\ntransformers:\n  - cfg/transformer.yaml\n",
				"source.yaml":          "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: source\ndata:\n  value: replaced\n",
				"cfg/transformer.yaml": "apiVersion: builtin\nkind: ReplacementTransformer\nmetadata:\n  name: replace\nreplacements:\n  - path: replacement.yaml\n",
				"replacement.yaml":     "source:\n  kind: ConfigMap\n  name: source\n  fieldPath: data.value\ntargets:\n  - select:\n      kind: ConfigMap\n      name: demo\n    fieldPaths:\n      - data.value\n",
			},
		},
		{
			// env: is never read by a builtin generator config (only
			// kustomization-level generators merge it into envs:), so
			// corrupting ignored.env must leave the render unchanged; if
			// kustomize ever read it, the undigested file would trip here.
			name: "ConfigMapGenerator files and envs",
			files: map[string]string{
				"kustomization.yaml": "generators:\n  - cfg/generator.yaml\n",
				"cfg/generator.yaml": "apiVersion: builtin\nkind: ConfigMapGenerator\nmetadata:\n  name: generated\nfiles:\n  - key=data.txt\nenvs:\n  - vars.env\nenv: ignored.env\n",
				"data.txt":           "data\n",
				"vars.env":           "A=a\n",
				"ignored.env":        "IGNORED=x\n",
			},
		},
		{
			name: "SecretGenerator files and envs",
			files: map[string]string{
				"kustomization.yaml": "generators:\n  - cfg/generator.yaml\n",
				"cfg/generator.yaml": "apiVersion: builtin\nkind: SecretGenerator\nmetadata:\n  name: generated\nfiles:\n  - token.txt\nenvs:\n  - secret.env\n",
				"token.txt":          "token\n",
				"secret.env":         "A=a\n",
			},
		},
		{
			name: "ValueAddTransformer targetFilePath",
			files: map[string]string{
				"kustomization.yaml":   transformerEntry,
				"cfg/transformer.yaml": "apiVersion: builtin\nkind: ValueAddTransformer\nmetadata:\n  name: add\nvalue: added\ntargetFilePath: targets.yaml\n",
				"targets.yaml":         "targets:\n  - selector:\n      kind: ConfigMap\n      name: demo\n    fieldPath: data/marker\n",
			},
		},
		{
			name: "inline transformers entry",
			files: map[string]string{
				"kustomization.yaml": "resources:\n  - cm.yaml\ntransformers:\n  - |\n    apiVersion: builtin\n    kind: PatchStrategicMergeTransformer\n    metadata:\n      name: inline\n    paths:\n      - patch.yaml\n",
				"patch.yaml":         renderCoveragePatchedConfigMap,
			},
		},
		{
			name: "directory transformers entry",
			files: map[string]string{
				"kustomization.yaml":     "resources:\n  - cm.yaml\ntransformers:\n  - ./cfg\n",
				"cfg/kustomization.yaml": "resources:\n  - transformer.yaml\n",
				"cfg/transformer.yaml":   "apiVersion: builtin\nkind: PatchTransformer\nmetadata:\n  name: patch\npath: patch.yaml\ntarget:\n  kind: ConfigMap\n  name: demo\n",
				"patch.yaml":             renderCoveragePatchedConfigMap,
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repoRoot := t.TempDir()
			appDir := filepath.Join(repoRoot, "manifests", "app")
			writeTestFile(t, filepath.Join(appDir, "cm.yaml"), renderCoverageDemoConfigMap)
			for name, content := range tt.files {
				writeTestFile(t, filepath.Join(appDir, filepath.FromSlash(name)), content)
			}
			writeTestFile(t, filepath.Join(repoRoot, "unrelated", "README.md"), "not a render input\n")
			application := argoappv1.Application{
				Name: "app", Namespace: "argocd",
				Spec: argoappv1.ApplicationSpec{
					Source: &argoappv1.ApplicationSource{
						RepoURL: "https://git.example.test/org/repo.git", Path: "manifests/app", TargetRevision: "main",
					},
					Destination: argoappv1.ApplicationDestination{Namespace: "default"},
				},
			}
			assertRenderInputCoverage(t, repoRoot, application)
		})
	}
}
