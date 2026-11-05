package appset

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/diagnostic"
)

// fakeOCIFetcher serves one local directory as every artifact and records
// the (repoURL, revision) pairs it was asked for.
type fakeOCIFetcher struct {
	root     string
	err      error
	requests []string
}

func (f *fakeOCIFetcher) FetchOCIArtifact(repoURL, revision string) (string, error) {
	f.requests = append(f.requests, repoURL+"@"+revision)
	if f.err != nil {
		return "", f.err
	}
	return f.root, nil
}

// writeOCIParityFixture lays out the artifact tree the upstream parity
// expectations below were produced from. It includes a hidden directory, a
// hidden top-level directory, names that sort differently by full path than
// in filepath.WalkDir order (a/b vs a-c), empty/comment-only/[]/list param
// files, a symlink inside the root, and a symlink escaping it to a file
// that exists.
func writeOCIParityFixture(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "artifact")
	files := map[string]string{
		"apps/alpha/config.yaml":    "name: alpha\nreplicas: 2\nnested:\n  key: v\nlist:\n  - x\n  - y\n",
		"apps/Beta_One/config.json": `{"name":"beta","enabled":true}`,
		"apps/.hidden/config.yaml":  "name: hidden\n",
		"apps/gamma/empty.yaml":     "",
		"apps/gamma/comment.yaml":   "# only a comment\n",
		"apps/gamma/list.yaml":      "- name: g1\n- name: g2\n",
		"apps/gamma/none.yaml":      "[]",
		"skip/config.yaml":          "name: skipped\n",
		"a/b/c.yaml":                "name: deep\n",
		"a-c/d.yaml":                "name: dash\n",
		"root.yaml":                 "name: root\n",
		".dotdir/x.yaml":            "name: dotdir\n",
	}
	for rel, content := range files {
		writeAppsetTestFile(t, filepath.Join(root, filepath.FromSlash(rel)), content)
	}
	writeAppsetTestFile(t, filepath.Join(base, "outside.yaml"), "name: outside\n")
	if err := os.Symlink("../../../outside.yaml", filepath.Join(root, "apps", "alpha", "escape.yaml")); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	if err := os.Symlink("config.yaml", filepath.Join(root, "apps", "alpha", "link.yaml")); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	return root
}

func ociParamSetsJSON(t *testing.T, root string, generatorJSON string, goTemplate bool) (string, []diagnostic.Diagnostic, error) {
	t.Helper()
	var oci argoappv1.OciGenerator
	if err := json.Unmarshal([]byte(generatorJSON), &oci); err != nil {
		t.Fatalf("decode generator: %v", err)
	}
	ctx := generatorContext{
		ManifestPath: "appset.yaml",
		AppSet:       argoappv1.ApplicationSet{Spec: argoappv1.ApplicationSetSpec{GoTemplate: goTemplate}},
		Options:      Options{OCI: &fakeOCIFetcher{root: root}},
	}
	paramSets, diags, supported, err := ociGeneratorParamSets(ctx, &oci)
	if err != nil {
		return "", diags, err
	}
	if !supported {
		t.Fatalf("ociGeneratorParamSets() unsupported, diags = %#v", diags)
	}
	params := make([]map[string]any, 0, len(paramSets))
	for _, paramSet := range paramSets {
		if paramSet.SourcePath != "" || len(paramSet.SourcePaths) != 0 {
			t.Fatalf("oci param set claims local source paths %q %q", paramSet.SourcePath, paramSet.SourcePaths)
		}
		params = append(params, paramSet.Params)
	}
	data, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	return string(data), diags, nil
}

func assertSameJSON(t *testing.T, got, want string) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal([]byte(got), &gotValue); err != nil {
		t.Fatalf("decode got: %v", err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("decode want: %v", err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("params mismatch\n got: %s\nwant: %s", got, want)
	}
}

// The expected params in TestOciGeneratorParamsMatchArgoCD come from running
// Argo CD v3.6.1's own OciGenerator.GenerateParams
// (applicationset/generators/oci.go, repo_path_utils.go) on the same tree,
// behind a services.Repos whose GetOciDirectories/GetOciFiles copy the
// v3.6.1 repo-server bodies (reposerver/repository/repository.go GetOciFiles,
// utils.go walkDirectoryTree with includeHidden false).
func TestOciGeneratorParamsMatchArgoCD(t *testing.T) {
	root := writeOCIParityFixture(t)
	tests := []struct {
		name       string
		generator  string
		goTemplate bool
		want       string
		wantDiags  int
	}{
		{
			// WalkDir order (a, a/b, a-c, ...), hidden directories skipped,
			// exclude wins over include, values templated from path params.
			name:       "directories go template",
			generator:  `{"directories":[{"path":"*"},{"path":"apps/*"},{"path":"a/*"},{"path":"apps/gamma","exclude":true}],"values":{"env":"{{.path.basename}}"}}`,
			goTemplate: true,
			want:       `[{"path":{"basename":"a","basenameNormalized":"a","path":"a","segments":["a"]},"values":{"env":"a"}},{"path":{"basename":"b","basenameNormalized":"b","path":"a/b","segments":["a","b"]},"values":{"env":"b"}},{"path":{"basename":"a-c","basenameNormalized":"a-c","path":"a-c","segments":["a-c"]},"values":{"env":"a-c"}},{"path":{"basename":"apps","basenameNormalized":"apps","path":"apps","segments":["apps"]},"values":{"env":"apps"}},{"path":{"basename":"Beta_One","basenameNormalized":"beta-one","path":"apps/Beta_One","segments":["apps","Beta_One"]},"values":{"env":"Beta_One"}},{"path":{"basename":"alpha","basenameNormalized":"alpha","path":"apps/alpha","segments":["apps","alpha"]},"values":{"env":"alpha"}},{"path":{"basename":"skip","basenameNormalized":"skip","path":"skip","segments":["skip"]},"values":{"env":"skip"}}]`,
		},
		{
			// Patterns are not normalized ("apps/*/" and "./apps/*" match
			// nothing) and a malformed pattern is skipped, not fatal.
			name:      "directories flat with prefix",
			generator: `{"directories":[{"path":"apps/*"},{"path":"apps/*/"},{"path":"./apps/*"},{"path":"["}],"pathParamPrefix":"pp","values":{"env":"{{pp.path.basename}}"}}`,
			want:      `[{"pp.path":"apps/Beta_One","pp.path.basename":"Beta_One","pp.path.basenameNormalized":"beta-one","pp.path[0]":"apps","pp.path[1]":"Beta_One","values.env":"Beta_One"},{"pp.path":"apps/alpha","pp.path.basename":"alpha","pp.path.basenameNormalized":"alpha","pp.path[0]":"apps","pp.path[1]":"alpha","values.env":"alpha"},{"pp.path":"apps/gamma","pp.path.basename":"gamma","pp.path.basenameNormalized":"gamma","pp.path[0]":"apps","pp.path[1]":"gamma","values.env":"gamma"}]`,
		},
		{
			name:      "directories trailing slash and dot prefix match nothing",
			generator: `{"directories":[{"path":"apps/*/"},{"path":"./apps/*"}]}`,
			want:      `[]`,
		},
		{
			// Doublestar matches hidden files and directories, the escaping
			// symlink is skipped, the in-root symlink is read, and files are
			// sorted by path.
			name:       "files go template with prefix",
			generator:  `{"files":[{"path":"**/*.yaml"},{"path":"apps/*/*.json"},{"path":"skip/**","exclude":true}],"pathParamPrefix":"pp"}`,
			goTemplate: true,
			want:       `[{"name":"dotdir","pp":{"path":{"basename":".dotdir","basenameNormalized":"dotdir","filename":"x.yaml","filenameNormalized":"x.yaml","path":".dotdir","segments":[".dotdir"]}}},{"name":"dash","pp":{"path":{"basename":"a-c","basenameNormalized":"a-c","filename":"d.yaml","filenameNormalized":"d.yaml","path":"a-c","segments":["a-c"]}}},{"name":"deep","pp":{"path":{"basename":"b","basenameNormalized":"b","filename":"c.yaml","filenameNormalized":"c.yaml","path":"a/b","segments":["a","b"]}}},{"name":"hidden","pp":{"path":{"basename":".hidden","basenameNormalized":"hidden","filename":"config.yaml","filenameNormalized":"config.yaml","path":"apps/.hidden","segments":["apps",".hidden"]}}},{"enabled":true,"name":"beta","pp":{"path":{"basename":"Beta_One","basenameNormalized":"beta-one","filename":"config.json","filenameNormalized":"config.json","path":"apps/Beta_One","segments":["apps","Beta_One"]}}},{"list":["x",true],"name":"alpha","nested":{"key":"v"},"pp":{"path":{"basename":"alpha","basenameNormalized":"alpha","filename":"config.yaml","filenameNormalized":"config.yaml","path":"apps/alpha","segments":["apps","alpha"]}},"replicas":2},{"list":["x",true],"name":"alpha","nested":{"key":"v"},"pp":{"path":{"basename":"alpha","basenameNormalized":"alpha","filename":"link.yaml","filenameNormalized":"link.yaml","path":"apps/alpha","segments":["apps","alpha"]}},"replicas":2},{"pp":{"path":{"basename":"gamma","basenameNormalized":"gamma","filename":"comment.yaml","filenameNormalized":"comment.yaml","path":"apps/gamma","segments":["apps","gamma"]}}},{"pp":{"path":{"basename":"gamma","basenameNormalized":"gamma","filename":"empty.yaml","filenameNormalized":"empty.yaml","path":"apps/gamma","segments":["apps","gamma"]}}},{"name":"g1","pp":{"path":{"basename":"gamma","basenameNormalized":"gamma","filename":"list.yaml","filenameNormalized":"list.yaml","path":"apps/gamma","segments":["apps","gamma"]}}},{"name":"g2","pp":{"path":{"basename":"gamma","basenameNormalized":"gamma","filename":"list.yaml","filenameNormalized":"list.yaml","path":"apps/gamma","segments":["apps","gamma"]}}},{"name":"root","pp":{"path":{"basename":".","basenameNormalized":"","filename":"root.yaml","filenameNormalized":"root.yaml","path":".","segments":["."]}}}]`,
		},
		{
			name:      "files flat with values",
			generator: `{"files":[{"path":"**/*.yaml"},{"path":"apps/*/*.json"},{"path":"skip/**","exclude":true}],"values":{"env":"{{name}}"}}`,
			want:      `[{"name":"dotdir","path":".dotdir","path.basename":".dotdir","path.basenameNormalized":"dotdir","path.filename":"x.yaml","path.filenameNormalized":"x.yaml","path[0]":".dotdir","values.env":"dotdir"},{"name":"dash","path":"a-c","path.basename":"a-c","path.basenameNormalized":"a-c","path.filename":"d.yaml","path.filenameNormalized":"d.yaml","path[0]":"a-c","values.env":"dash"},{"name":"deep","path":"a/b","path.basename":"b","path.basenameNormalized":"b","path.filename":"c.yaml","path.filenameNormalized":"c.yaml","path[0]":"a","path[1]":"b","values.env":"deep"},{"name":"hidden","path":"apps/.hidden","path.basename":".hidden","path.basenameNormalized":"hidden","path.filename":"config.yaml","path.filenameNormalized":"config.yaml","path[0]":"apps","path[1]":".hidden","values.env":"hidden"},{"enabled":"true","name":"beta","path":"apps/Beta_One","path.basename":"Beta_One","path.basenameNormalized":"beta-one","path.filename":"config.json","path.filenameNormalized":"config.json","path[0]":"apps","path[1]":"Beta_One","values.env":"beta"},{"list.0":"x","list.1":"true","name":"alpha","nested.key":"v","path":"apps/alpha","path.basename":"alpha","path.basenameNormalized":"alpha","path.filename":"config.yaml","path.filenameNormalized":"config.yaml","path[0]":"apps","path[1]":"alpha","replicas":"2","values.env":"alpha"},{"list.0":"x","list.1":"true","name":"alpha","nested.key":"v","path":"apps/alpha","path.basename":"alpha","path.basenameNormalized":"alpha","path.filename":"link.yaml","path.filenameNormalized":"link.yaml","path[0]":"apps","path[1]":"alpha","replicas":"2","values.env":"alpha"},{"path":"apps/gamma","path.basename":"gamma","path.basenameNormalized":"gamma","path.filename":"comment.yaml","path.filenameNormalized":"comment.yaml","path[0]":"apps","path[1]":"gamma","values.env":"{{name}}"},{"path":"apps/gamma","path.basename":"gamma","path.basenameNormalized":"gamma","path.filename":"empty.yaml","path.filenameNormalized":"empty.yaml","path[0]":"apps","path[1]":"gamma","values.env":"{{name}}"},{"name":"g1","path":"apps/gamma","path.basename":"gamma","path.basenameNormalized":"gamma","path.filename":"list.yaml","path.filenameNormalized":"list.yaml","path[0]":"apps","path[1]":"gamma","values.env":"g1"},{"name":"g2","path":"apps/gamma","path.basename":"gamma","path.basenameNormalized":"gamma","path.filename":"list.yaml","path.filenameNormalized":"list.yaml","path[0]":"apps","path[1]":"gamma","values.env":"g2"},{"name":"root","path":".","path.basename":".","path.basenameNormalized":"","path.filename":"root.yaml","path.filenameNormalized":"root.yaml","path[0]":".","values.env":"root"}]`,
		},
		{
			// "." and an empty pattern both mean "**".
			name:       "files dot pattern",
			generator:  `{"files":[{"path":"."}]}`,
			goTemplate: true,
			want:       ociDotPatternWant,
		},
		{
			name:       "files empty pattern",
			generator:  `{"files":[{"path":""}]}`,
			goTemplate: true,
			want:       ociDotPatternWant,
		},
		{
			name:       "directories win over files",
			generator:  `{"directories":[{"path":"skip"}],"files":[{"path":"root.yaml"}]}`,
			goTemplate: true,
			want:       `[{"path":{"basename":"skip","basenameNormalized":"skip","path":"skip","segments":["skip"]}}]`,
		},
		{
			name:       "exclude-only files generate nothing",
			generator:  `{"files":[{"path":"**","exclude":true}]}`,
			goTemplate: true,
			want:       `[]`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, diags, err := ociParamSetsJSON(t, root, tt.generator, tt.goTemplate)
			if err != nil {
				t.Fatalf("ociGeneratorParamSets() error = %v", err)
			}
			if len(diags) != tt.wantDiags {
				t.Fatalf("diagnostics = %#v, want %d", diags, tt.wantDiags)
			}
			assertSameJSON(t, got, tt.want)
		})
	}
}

const ociDotPatternWant = `[{"name":"dotdir","path":{"basename":".dotdir","basenameNormalized":"dotdir","filename":"x.yaml","filenameNormalized":"x.yaml","path":".dotdir","segments":[".dotdir"]}},{"name":"dash","path":{"basename":"a-c","basenameNormalized":"a-c","filename":"d.yaml","filenameNormalized":"d.yaml","path":"a-c","segments":["a-c"]}},{"name":"deep","path":{"basename":"b","basenameNormalized":"b","filename":"c.yaml","filenameNormalized":"c.yaml","path":"a/b","segments":["a","b"]}},{"name":"hidden","path":{"basename":".hidden","basenameNormalized":"hidden","filename":"config.yaml","filenameNormalized":"config.yaml","path":"apps/.hidden","segments":["apps",".hidden"]}},{"enabled":true,"name":"beta","path":{"basename":"Beta_One","basenameNormalized":"beta-one","filename":"config.json","filenameNormalized":"config.json","path":"apps/Beta_One","segments":["apps","Beta_One"]}},{"list":["x",true],"name":"alpha","nested":{"key":"v"},"path":{"basename":"alpha","basenameNormalized":"alpha","filename":"config.yaml","filenameNormalized":"config.yaml","path":"apps/alpha","segments":["apps","alpha"]},"replicas":2},{"list":["x",true],"name":"alpha","nested":{"key":"v"},"path":{"basename":"alpha","basenameNormalized":"alpha","filename":"link.yaml","filenameNormalized":"link.yaml","path":"apps/alpha","segments":["apps","alpha"]},"replicas":2},{"path":{"basename":"gamma","basenameNormalized":"gamma","filename":"comment.yaml","filenameNormalized":"comment.yaml","path":"apps/gamma","segments":["apps","gamma"]}},{"path":{"basename":"gamma","basenameNormalized":"gamma","filename":"empty.yaml","filenameNormalized":"empty.yaml","path":"apps/gamma","segments":["apps","gamma"]}},{"name":"g1","path":{"basename":"gamma","basenameNormalized":"gamma","filename":"list.yaml","filenameNormalized":"list.yaml","path":"apps/gamma","segments":["apps","gamma"]}},{"name":"g2","path":{"basename":"gamma","basenameNormalized":"gamma","filename":"list.yaml","filenameNormalized":"list.yaml","path":"apps/gamma","segments":["apps","gamma"]}},{"name":"root","path":{"basename":".","basenameNormalized":"","filename":"root.yaml","filenameNormalized":"root.yaml","path":".","segments":["."]}},{"name":"skipped","path":{"basename":"skip","basenameNormalized":"skip","filename":"config.yaml","filenameNormalized":"config.yaml","path":"skip","segments":["skip"]}}]`

func TestOciGeneratorMalformedFilePatternIsFatal(t *testing.T) {
	// Argo CD fails GetOciFiles on a doublestar syntax error.
	_, _, err := ociParamSetsJSON(t, writeOCIParityFixture(t), `{"files":[{"path":"[["}]}`, true)
	if err == nil || !strings.Contains(err.Error(), "syntax error in pattern") {
		t.Fatalf("error = %v, want doublestar pattern syntax error", err)
	}
}

func TestOciGeneratorInvalidParamFileSharesGitFilesDiagnostic(t *testing.T) {
	root := t.TempDir()
	writeAppsetTestFile(t, filepath.Join(root, "envs", "bad.yaml"), "name: [unclosed\n")
	writeAppsetTestFile(t, filepath.Join(root, "envs", "good.yaml"), "name: good\n")
	got, diags, err := ociParamSetsJSON(t, root, `{"files":[{"path":"envs/*.yaml"}]}`, false)
	if err != nil {
		t.Fatalf("ociGeneratorParamSets() error = %v", err)
	}
	if len(diags) != 1 || !strings.Contains(diags[0].Message, `oci files match "envs/bad.yaml" is not valid YAML`) {
		t.Fatalf("diagnostics = %#v, want one invalid oci files diagnostic", diags)
	}
	assertSameJSON(t, got, `[{"name":"good","path":"envs","path.basename":"envs","path.basenameNormalized":"envs","path.filename":"good.yaml","path.filenameNormalized":"good.yaml","path[0]":"envs"}]`)
}

const ociDirectoriesAppSet = `
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: platform
  namespace: argocd
spec:
  goTemplate: true
  goTemplateOptions: ["missingkey=error"]
  generators:
    - oci:
        repoURL: oci://registry.example.invalid/platform
        revision: v1.2.3
        directories:
          - path: platform/*
          - path: platform/skipped
            exclude: true
  template:
    metadata:
      name: '{{.path.basename}}'
    spec:
      project: default
      source:
        repoURL: oci://registry.example.invalid/platform
        targetRevision: v1.2.3
        path: '{{.path.path}}'
      destination:
        server: https://kubernetes.default.svc
        namespace: '{{.path.basenameNormalized}}'
`

func writeOCIPlatformArtifact(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"cert-manager", "Prometheus_Stack", "skipped", ".git"} {
		writeAppsetTestFile(t, filepath.Join(root, "platform", dir, "manifest.yaml"), "kind: ConfigMap\n")
	}
	return root
}

func TestGenerateOciDirectoriesGenerator(t *testing.T) {
	fetcher := &fakeOCIFetcher{root: writeOCIPlatformArtifact(t)}
	repoRoot := t.TempDir()
	// A same-named local directory must not leak into the generator.
	writeAppsetTestFile(t, filepath.Join(repoRoot, "platform", "local-only", "x.yaml"), "kind: ConfigMap\n")

	apps, diags, err := GenerateFromYAMLWithOptions(repoRoot, "appset.yaml", []byte(ociDirectoriesAppSet), Options{OCI: fetcher})
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got, want := generatedNames(apps), []string{"Prometheus_Stack", "cert-manager"}; !slices.Equal(got, want) {
		t.Fatalf("names = %v, want %v", got, want)
	}
	if got, want := fetcher.requests, []string{"oci://registry.example.invalid/platform@v1.2.3"}; !slices.Equal(got, want) {
		t.Fatalf("fetch requests = %v, want %v", got, want)
	}
	prom := apps[0]
	if prom.Application.Spec.Source.Path != "platform/Prometheus_Stack" || prom.Application.Spec.Destination.Namespace != "prometheus-stack" {
		t.Fatalf("generated spec = %#v", prom.Application.Spec)
	}
	for _, app := range apps {
		if app.SourcePath != "" || len(app.SourcePaths) != 0 {
			t.Fatalf("oci generated app %s claims local source paths %q %q", app.Application.Name, app.SourcePath, app.SourcePaths)
		}
		if app.Generator != "oci-directories" {
			t.Fatalf("generator = %q, want oci-directories", app.Generator)
		}
	}
}

func TestGenerateOciFilesGeneratorWithTemplateOverrideAndSelector(t *testing.T) {
	root := t.TempDir()
	writeAppsetTestFile(t, filepath.Join(root, "clusters", "staging.yaml"), "environment: staging\nreplicas: 2\ntier: test\n")
	writeAppsetTestFile(t, filepath.Join(root, "clusters", "production.yaml"), "environment: production\nreplicas: 5\ntier: prod\n")
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: cluster-configs
  namespace: argocd
spec:
  generators:
    - oci:
        repoURL: oci://registry.example.invalid/configs
        revision: v2.1.0
        files:
          - path: "clusters/*.yaml"
        template:
          metadata:
            labels:
              replicas: '{{replicas}}'
      selector:
        matchLabels:
          tier: prod
  template:
    metadata:
      name: '{{environment}}-app'
    spec:
      project: default
      source:
        repoURL: https://github.com/my-org/app-manifests
        targetRevision: HEAD
        path: 'overlays/{{environment}}'
      destination:
        server: https://kubernetes.default.svc
        namespace: '{{environment}}'
`)
	apps, diags, err := GenerateFromYAMLWithOptions(t.TempDir(), "appset.yaml", data, Options{OCI: &fakeOCIFetcher{root: root}})
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"production-app"}) {
		t.Fatalf("names = %v, want [production-app]", got)
	}
	if got := apps[0].Application.Labels["replicas"]; got != "5" {
		t.Fatalf("generator template override label = %q, want 5", got)
	}
	if apps[0].Generator != "oci-files" {
		t.Fatalf("generator = %q, want oci-files", apps[0].Generator)
	}
}

func TestGenerateOciGeneratorMatrixWithListInterpolatesRevision(t *testing.T) {
	fetcher := &fakeOCIFetcher{root: writeOCIPlatformArtifact(t)}
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: platform
  namespace: argocd
spec:
  goTemplate: true
  generators:
    - matrix:
        generators:
          - list:
              elements:
                - cluster: east
                  version: v1.2.3
          - oci:
              repoURL: oci://registry.example.invalid/platform
              revision: '{{.version}}'
              pathParamPrefix: artifact
              directories:
                - path: platform/cert-manager
  template:
    metadata:
      name: '{{.cluster}}-{{.artifact.path.basename}}'
    spec:
      project: default
      source:
        repoURL: oci://registry.example.invalid/platform
        targetRevision: '{{.version}}'
        path: '{{.artifact.path.path}}'
      destination:
        server: https://kubernetes.default.svc
        namespace: default
`)
	apps, diags, err := GenerateFromYAMLWithOptions(t.TempDir(), "appset.yaml", data, Options{OCI: fetcher})
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"east-cert-manager"}) {
		t.Fatalf("names = %v", got)
	}
	if got, want := fetcher.requests, []string{"oci://registry.example.invalid/platform@v1.2.3"}; !slices.Equal(got, want) {
		t.Fatalf("fetch requests = %v, want interpolated revision %v", got, want)
	}
	if apps[0].Application.Spec.Source.Path != "platform/cert-manager" {
		t.Fatalf("source path = %q", apps[0].Application.Spec.Source.Path)
	}
}

func TestGenerateOciGeneratorMatrixWithGitKeepsOnlyGitSourcePaths(t *testing.T) {
	repoRoot := t.TempDir()
	writeAppsetTestFile(t, filepath.Join(repoRoot, "overlays", "east", "kustomization.yaml"), "resources: []\n")
	fetcher := &fakeOCIFetcher{root: writeOCIPlatformArtifact(t)}
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: platform
  namespace: argocd
spec:
  goTemplate: true
  generators:
    - matrix:
        generators:
          - git:
              repoURL: https://github.com/example/repo
              revision: HEAD
              directories:
                - path: overlays/*
          - oci:
              repoURL: oci://registry.example.invalid/platform
              revision: v1
              pathParamPrefix: artifact
              directories:
                - path: platform/cert-manager
  template:
    metadata:
      name: '{{.path.basename}}-{{.artifact.path.basename}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        targetRevision: HEAD
        path: '{{.path.path}}'
      destination:
        server: https://kubernetes.default.svc
        namespace: default
`)
	apps, _, err := GenerateFromYAMLWithOptions(repoRoot, "appset.yaml", data, Options{OCI: fetcher})
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"east-cert-manager"}) {
		t.Fatalf("names = %v", got)
	}
	if got := apps[0].SourcePaths; !slices.Equal(got, []string{"overlays/east"}) {
		t.Fatalf("SourcePaths = %v, want only the local git directory", got)
	}
}

func TestGenerateOciGeneratorInMergeAndNestedMatrix(t *testing.T) {
	root := t.TempDir()
	writeAppsetTestFile(t, filepath.Join(root, "clusters", "east.yaml"), "cluster: east\nregion: us-east-1\n")
	writeAppsetTestFile(t, filepath.Join(root, "clusters", "west.yaml"), "cluster: west\nregion: us-west-2\n")
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: merged
  namespace: argocd
spec:
  goTemplate: true
  generators:
    - merge:
        mergeKeys: [cluster]
        generators:
          - list:
              elements:
                - cluster: east
                  tier: gold
                - cluster: west
                  tier: silver
          - matrix:
              generators:
                - oci:
                    repoURL: oci://registry.example.invalid/configs
                    revision: v1
                    files:
                      - path: clusters/*.yaml
                - list:
                    elements:
                      - suffix: x
  template:
    metadata:
      name: '{{.cluster}}-{{.tier}}-{{.region}}-{{.suffix}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        targetRevision: HEAD
        path: apps
      destination:
        server: https://kubernetes.default.svc
        namespace: default
`)
	apps, diags, err := GenerateFromYAMLWithOptions(t.TempDir(), "appset.yaml", data, Options{OCI: &fakeOCIFetcher{root: root}})
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got, want := generatedNames(apps), []string{"east-gold-us-east-1-x", "west-silver-us-west-2-x"}; !slices.Equal(got, want) {
		t.Fatalf("names = %v, want %v", got, want)
	}
}

func TestGenerateOciGeneratorValuesTemplateErrorIsAppSetScoped(t *testing.T) {
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: platform
  namespace: argocd
spec:
  goTemplate: true
  goTemplateOptions: ["missingkey=error"]
  generators:
    - oci:
        repoURL: oci://registry.example.invalid/platform
        revision: v1
        directories:
          - path: platform/*
        values:
          broken: '{{.missing}}'
  template:
    metadata:
      name: '{{.path.basename}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        targetRevision: HEAD
        path: apps
      destination:
        server: https://kubernetes.default.svc
        namespace: default
`)
	apps, diags, err := GenerateFromYAMLWithOptions(t.TempDir(), "appset.yaml", data, Options{OCI: &fakeOCIFetcher{root: writeOCIPlatformArtifact(t)}})
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
	}
	if len(apps) != 0 {
		t.Fatalf("apps = %v, want none", generatedNames(apps))
	}
	if len(diags) != 1 || diagnostic.StableCode(diags[0]) != "appset.template-render-failed" {
		t.Fatalf("diagnostics = %#v, want one appset.template-render-failed", diags)
	}
}

func TestGenerateOciGeneratorFetchErrorIsFatal(t *testing.T) {
	fetchErr := errors.New("offline cache miss for OCI artifact registry.example.invalid/platform revision \"v1.2.3\"")
	_, _, err := GenerateFromYAMLWithOptions(t.TempDir(), "appset.yaml", []byte(ociDirectoriesAppSet), Options{OCI: &fakeOCIFetcher{err: fetchErr}})
	if !errors.Is(err, fetchErr) {
		t.Fatalf("error = %v, want wrapped fetch error", err)
	}
}

func TestGenerateOciGeneratorWithoutPathsIsUnsupported(t *testing.T) {
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: empty
spec:
  generators:
    - oci:
        repoURL: oci://registry.example.invalid/platform
        revision: v1
  template:
    metadata:
      name: x
`)
	fetcher := &fakeOCIFetcher{root: t.TempDir()}
	_, diags, err := GenerateFromYAMLWithOptions(t.TempDir(), "appset.yaml", data, Options{OCI: fetcher})
	if !errors.Is(err, ErrUnsupportedGenerator) {
		t.Fatalf("error = %v, want ErrUnsupportedGenerator", err)
	}
	if len(diags) != 1 || diagnostic.StableCode(diags[0]) != "appset.unsupported-generator" {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(fetcher.requests) != 0 {
		t.Fatalf("fetch requests = %v, want none", fetcher.requests)
	}
}
