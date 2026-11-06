package app

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/render"
	sourcepkg "github.com/sholdee/drydock/internal/source"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// The tests in this file pin ARGOCD_APP_REVISION, ARGOCD_APP_REVISION_SHORT,
// and ARGOCD_APP_REVISION_SHORT_8 to the revision the Argo CD v3.6.1
// repo-server resolves (reposerver/repository/repository.go newEnv and
// runRepoOperation): the commit SHA of a Git source and the manifest digest
// of an OCI source, cut to 7 and 8 bytes for the short forms.

const appRevisionTestRepoURL = "https://github.com/example/repo"

// writeAppRevisionChart writes a chart whose ConfigMap <release>-revision
// echoes the revision, short, and short8 values.
func writeAppRevisionChart(t *testing.T, dir string) {
	t.Helper()
	for name, content := range appRevisionChartFiles() {
		writeTestFile(t, filepath.Join(dir, filepath.FromSlash(name)), content)
	}
}

func appRevisionChartFiles() map[string]string {
	return map[string]string{
		"Chart.yaml":  "apiVersion: v2\nname: revision\nversion: 0.1.0\n",
		"values.yaml": "revision: unset\nshort: unset\nshort8: unset\n",
		"templates/cm.yaml": `apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Release.Name }}-revision
data:
  revision: {{ .Values.revision | quote }}
  short: {{ .Values.short | quote }}
  short8: {{ .Values.short8 | quote }}
`,
	}
}

// appRevisionHelmParameters is the helm block that substitutes the three
// variables into the chart values. forceString keeps an all-digit short SHA
// a string, as it would be under Argo CD with the same spec.
const appRevisionHelmParameters = `      parameters:
        - name: revision
          value: $ARGOCD_APP_REVISION
          forceString: true
        - name: short
          value: $ARGOCD_APP_REVISION_SHORT
          forceString: true
        - name: short8
          value: ${ARGOCD_APP_REVISION_SHORT_8}
          forceString: true
`

func writeAppRevisionHelmApp(t *testing.T, root, name, repoURL, targetRevision, chartPath string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", name+".yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: `+name+`
  namespace: argocd
spec:
  project: default
  source:
    repoURL: `+repoURL+`
    targetRevision: `+targetRevision+`
    path: `+chartPath+`
    helm:
`+appRevisionHelmParameters+`  destination:
    name: in-cluster
    namespace: default
`)
}

// writeAppRevisionKustomizeApp writes a Kustomize Application whose
// commonAnnotations (with commonAnnotationsEnvsubst) and commonLabels name
// the variables.
func writeAppRevisionKustomizeApp(t *testing.T, root string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", "kustomize-revision.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: kustomize-revision
  namespace: argocd
spec:
  project: default
  source:
    repoURL: `+appRevisionTestRepoURL+`
    targetRevision: main
    path: manifests/kustomize-revision
    kustomize:
      commonAnnotationsEnvsubst: true
      commonAnnotations:
        example.com/revision: $ARGOCD_APP_REVISION
      commonLabels:
        example.com/short: $ARGOCD_APP_REVISION_SHORT
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "manifests", "kustomize-revision", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cm.yaml
`)
	writeTestFile(t, filepath.Join(root, "manifests", "kustomize-revision", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: kustomize-revision
data: {}
`)
}

// writeAppRevisionJsonnetApp writes a Jsonnet directory Application whose
// extVar and TLA name the variables.
func writeAppRevisionJsonnetApp(t *testing.T, root string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", "jsonnet-revision.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: jsonnet-revision
  namespace: argocd
spec:
  project: default
  source:
    repoURL: `+appRevisionTestRepoURL+`
    targetRevision: main
    path: manifests/jsonnet-revision
    directory:
      jsonnet:
        extVars:
          - name: short8
            value: $ARGOCD_APP_REVISION_SHORT_8
        tlas:
          - name: revision
            value: $ARGOCD_APP_REVISION
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "manifests", "jsonnet-revision", "main.jsonnet"), `function(revision) {
  apiVersion: 'v1',
  kind: 'ConfigMap',
  metadata: { name: 'jsonnet-revision' },
  data: { revision: revision, short8: std.extVar('short8') },
}
`)
}

func appRevisionData(t *testing.T, manifests []render.Manifest, name string) map[string]string {
	t.Helper()
	manifest := assertManifestNamed(t, manifests, name)
	data, _, err := unstructured.NestedStringMap(manifest.Object.Object, "data")
	if err != nil {
		t.Fatalf("%s data error = %v", name, err)
	}
	return data
}

func assertAppRevisionChartData(t *testing.T, manifests []render.Manifest, name, revision string) {
	t.Helper()
	data := appRevisionData(t, manifests, name)
	want := map[string]string{
		"revision": revision,
		"short":    revision[:min(7, len(revision))],
		"short8":   revision[:min(8, len(revision))],
	}
	for key, value := range want {
		if data[key] != value {
			t.Fatalf("%s data[%s] = %q, want %q (all data %#v)", name, key, data[key], value, data)
		}
	}
}

// A local source renders the tree at --path, so it reports the HEAD commit
// of that tree, also while the worktree has uncommitted changes. The
// variables reach Helm parameters, Kustomize commonAnnotations and
// commonLabels, and Jsonnet extVars and TLAs.
func TestBuildLocalSourcesSeeHeadCommitAsAppRevision(t *testing.T) {
	root := t.TempDir()
	writeAppRevisionChart(t, filepath.Join(root, "charts", "revision"))
	writeAppRevisionHelmApp(t, root, "helm-revision", appRevisionTestRepoURL, "main", "charts/revision")
	writeAppRevisionKustomizeApp(t, root)
	writeAppRevisionJsonnetApp(t, root)
	head := gitCommitAll(t, root, "initial")
	writeTestFile(t, filepath.Join(root, "untracked.txt"), "dirty worktree\n")

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("Build() error = %v\nDiagnostics: %#v", err, result.Diagnostics)
	}
	assertAppRevisionChartData(t, result.Manifests, "helm-revision-revision", head)

	kustomized := assertManifestNamed(t, result.Manifests, "kustomize-revision")
	if got := kustomized.Object.GetAnnotations()["example.com/revision"]; got != head {
		t.Fatalf("Kustomize commonAnnotations revision = %q, want %q", got, head)
	}
	if got := kustomized.Object.GetLabels()["example.com/short"]; got != head[:7] {
		t.Fatalf("Kustomize commonLabels short = %q, want %q", got, head[:7])
	}

	jsonnetData := appRevisionData(t, result.Manifests, "jsonnet-revision")
	if jsonnetData["revision"] != head || jsonnetData["short8"] != head[:8] {
		t.Fatalf("Jsonnet data = %#v, want revision %q and short8 %q", jsonnetData, head, head[:8])
	}
}

// drydock cannot tell the commit of a tree that is not the top of a Git
// repository, so the variables keep the spec targetRevision there.
func TestBuildLocalSourceOutsideGitKeepsTargetRevision(t *testing.T) {
	root := t.TempDir()
	writeAppRevisionChart(t, filepath.Join(root, "charts", "revision"))
	writeAppRevisionHelmApp(t, root, "helm-revision", appRevisionTestRepoURL, "main", "charts/revision")

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("Build() error = %v\nDiagnostics: %#v", err, result.Diagnostics)
	}
	assertAppRevisionChartData(t, result.Manifests, "helm-revision-revision", "main")
}

// Each rendered source of a multi-source Application gets the revision of
// its own repository: the resolved commit of a fetched Git source and the
// manifest digest of an OCI source. A $ref source only supplies value files;
// its revision never reaches the build environment. The short forms are a
// byte cut, so a digest gives "sha256:" and "sha256:<first hex digit>".
func TestBuildMultiSourceSeesEachSourceResolvedRevision(t *testing.T) {
	const (
		chartRepoURL  = "https://git.example.test/charts.git"
		valuesRepoURL = "https://git.example.test/values.git"
		ociRepoURL    = "oci://registry.example.test/org/revision"
		chartCommit   = "1111111111111111111111111111111111111111"
		valuesCommit  = "2222222222222222222222222222222222222222"
		ociDigest     = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
	)
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "multi.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: multi
  namespace: argocd
spec:
  project: default
  sources:
    - repoURL: `+chartRepoURL+`
      targetRevision: v1.0.0
      path: charts/revision
      helm:
        releaseName: git
        valueFiles:
          - $values/values.yaml
`+strings.ReplaceAll(appRevisionHelmParameters, "      ", "        ")+`    - repoURL: `+ociRepoURL+`
      targetRevision: "1.2.3"
      path: .
      helm:
        releaseName: oci
`+strings.ReplaceAll(appRevisionHelmParameters, "      ", "        ")+`    - repoURL: `+valuesRepoURL+`
      targetRevision: main
      ref: values
  destination:
    name: in-cluster
    namespace: default
`)
	chartDir := t.TempDir()
	writeAppRevisionChart(t, filepath.Join(chartDir, "charts", "revision"))
	valuesDir := t.TempDir()
	writeTestFile(t, filepath.Join(valuesDir, "values.yaml"), "revision: from-values\n")

	gitAcquirer := &recordingGitAcquirer{
		paths:     map[string]string{"v1.0.0": chartDir, "main": valuesDir},
		revisions: map[string]string{"v1.0.0": chartCommit, "main": valuesCommit},
	}
	ociAcquirer := &fakeOCIArtifactAcquirer{digest: ociDigest, files: appRevisionChartFiles()}
	request := ociBuildRequest(t, root)
	result, err := Orchestrator{GitAcquirer: gitAcquirer, OCIArtifactAcquirer: ociAcquirer}.Build(context.Background(), request)
	if err != nil {
		t.Fatalf("Build() error = %v\nDiagnostics: %#v", err, result.Diagnostics)
	}
	assertAppRevisionChartData(t, result.Manifests, "git-revision", chartCommit)
	ociData := appRevisionData(t, result.Manifests, "oci-revision")
	if ociData["revision"] != ociDigest || ociData["short"] != "sha256:" || ociData["short8"] != "sha256:3" {
		t.Fatalf("OCI data = %#v, want digest %q, short %q, short8 %q", ociData, ociDigest, "sha256:", "sha256:3")
	}
}

// A --repo-map target renders the tree in that directory, so a source mapped
// to the top of a Git repository reports that repository's HEAD commit. A
// mapped directory outside Git keeps the spec targetRevision.
func TestBuildRepoMappedSourceSeesMappedHeadCommit(t *testing.T) {
	const mappedURL = "https://git.example.test/mapped/repo.git"
	root := t.TempDir()
	writeAppRevisionHelmApp(t, root, "mapped", mappedURL, "main", "charts/revision")
	gitCommitAll(t, root, "root")

	mappedDir := t.TempDir()
	writeAppRevisionChart(t, filepath.Join(mappedDir, "charts", "revision"))
	mappedHead := gitCommitAll(t, mappedDir, "mapped")
	request := BuildRequest{Path: root, RepoMaps: []sourcepkg.RepoMap{{URL: mappedURL, Path: mappedDir}}}
	result, err := Orchestrator{}.Build(context.Background(), request)
	if err != nil {
		t.Fatalf("Build() error = %v\nDiagnostics: %#v", err, result.Diagnostics)
	}
	assertAppRevisionChartData(t, result.Manifests, "mapped-revision", mappedHead)

	plainDir := t.TempDir()
	writeAppRevisionChart(t, filepath.Join(plainDir, "charts", "revision"))
	request.RepoMaps = []sourcepkg.RepoMap{{URL: mappedURL, Path: plainDir}}
	result, err = Orchestrator{}.Build(context.Background(), request)
	if err != nil {
		t.Fatalf("Build() error = %v\nDiagnostics: %#v", err, result.Diagnostics)
	}
	assertAppRevisionChartData(t, result.Manifests, "mapped-revision", "main")
}

// Each side of a --ref/--ref-orig diff renders a snapshot of its own commit,
// so each side reports that commit.
func TestDiffRefSidesSeeTheirOwnCommitAsAppRevision(t *testing.T) {
	root := t.TempDir()
	writeAppRevisionChart(t, filepath.Join(root, "charts", "revision"))
	writeAppRevisionHelmApp(t, root, "helm-revision", appRevisionTestRepoURL, "main", "charts/revision")
	base := gitCommitAll(t, root, "base")
	writeTestFile(t, filepath.Join(root, "README.md"), "unrelated change\n")
	head := gitCommitAll(t, root, "head")

	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		RightPath: root,
		Repo:      root,
		Ref:       "HEAD",
		RefOrig:   base,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v\nDiagnostics: %#v", err, result.Diagnostics)
	}
	var found bool
	for _, item := range result.Results {
		if item.Resource.Name != "helm-revision-revision" {
			continue
		}
		found = true
		if !strings.Contains(item.Before, "revision: "+base) || !strings.Contains(item.After, "revision: "+head) {
			t.Fatalf("diff before/after = %q / %q, want base %s and head %s", item.Before, item.After, base, head)
		}
	}
	if !found {
		t.Fatalf("Results = %#v, want a change to helm-revision-revision", result.Results)
	}
}

// The persistent render cache keys a local source on its input files. A
// source that names an ARGOCD_APP_REVISION variable also keys on the
// revision, so a new commit re-renders it with the new SHA. A source that
// does not name one keeps its key and still hits.
func TestPersistentCacheAppRevisionReaderRerendersOnNewCommit(t *testing.T) {
	root := t.TempDir()
	writeAppRevisionChart(t, filepath.Join(root, "charts", "revision"))
	writeAppRevisionHelmApp(t, root, "reader", appRevisionTestRepoURL, "main", "charts/revision")
	writePersistentCacheApp(t, root, "plain")
	gitCommitAll(t, root, "initial")
	cacheDir := t.TempDir()
	request := persistentBuildRequest(root, cacheDir)
	request.RecordCacheEvents = true
	if _, err := (Orchestrator{}).Build(context.Background(), request); err != nil {
		t.Fatalf("cold Build() error = %v", err)
	}

	writeTestFile(t, filepath.Join(root, "README.md"), "unrelated change\n")
	head := gitCommitAll(t, root, "unrelated")
	var renderedMu sync.Mutex
	renderedPaths := map[string]int{}
	orchestrator := Orchestrator{}
	orchestrator.renderObserver = func(source render.ResolvedSource) {
		renderedMu.Lock()
		defer renderedMu.Unlock()
		renderedPaths[filepath.ToSlash(source.Path)]++
	}
	result, err := orchestrator.Build(context.Background(), request)
	if err != nil {
		t.Fatalf("warm Build() error = %v", err)
	}
	if got := renderedPaths["charts/revision"]; got != 1 {
		t.Fatalf("reader renders = %d, want 1; all renders = %#v", got, renderedPaths)
	}
	if got := renderedPaths["charts/plain"]; got != 0 {
		t.Fatalf("plain renders = %d, want 0 persistent hit; all renders = %#v", got, renderedPaths)
	}
	if !hasRenderCacheEvent(result.CacheEvents, cacheevent.ActionHit, "argocd/plain", "") {
		t.Fatalf("CacheEvents = %#v, want plain persistent hit", result.CacheEvents)
	}
	assertAppRevisionChartData(t, result.Manifests, "reader-revision", head)
}
