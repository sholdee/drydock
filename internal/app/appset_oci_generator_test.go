package app

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/ociartifact/ocitest"
	sourcepkg "github.com/sholdee/drydock/internal/source"
)

// writeOCIGeneratorAppSet writes apps/platform-appset.yaml: an oci
// directories generator over platform/* whose generated Applications render
// the same artifact at the matched path.
func writeOCIGeneratorAppSet(t *testing.T, root, repoURL, revision string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", "platform-appset.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: platform
  namespace: argocd
spec:
  goTemplate: true
  goTemplateOptions: ["missingkey=error"]
  generators:
    - oci:
        repoURL: `+repoURL+`
        revision: "`+revision+`"
        directories:
          - path: platform/*
          - path: platform/excluded
            exclude: true
  template:
    metadata:
      name: '{{.path.basename}}'
    spec:
      project: default
      source:
        repoURL: `+repoURL+`
        targetRevision: "`+revision+`"
        path: '{{.path.path}}'
      destination:
        name: in-cluster
        namespace: '{{.path.basenameNormalized}}'
`)
}

func pushOCIPlatformArtifact(t *testing.T, reg *ocitest.Registry) {
	t.Helper()
	ocitest.PushPlainManifestsArtifact(t, reg, "platform", "v1", map[string]string{
		"platform/cert-manager/cm.yaml": plainConfigMapYAML("cert-manager-from-oci"),
		"platform/prometheus/cm.yaml":   plainConfigMapYAML("prometheus-from-oci"),
		"platform/excluded/cm.yaml":     plainConfigMapYAML("excluded-from-oci"),
		"platform/.hidden/cm.yaml":      plainConfigMapYAML("hidden-from-oci"),
	})
}

func generatedApplicationNames(result BuildResult) []string {
	names := make([]string, 0, len(result.Applications))
	for _, application := range result.Applications {
		names = append(names, application.Name)
	}
	slices.Sort(names)
	return names
}

func TestBuildOCIGeneratorGeneratesAndRendersApplications(t *testing.T) {
	reg := ocitest.StartRegistry(t)
	pushOCIPlatformArtifact(t, reg)
	root := t.TempDir()
	repoURL := reg.RepoURL("platform")
	writeOCIGeneratorAppSet(t, root, repoURL, "v1")
	// A local directory with an artifact-shaped path must not be listed.
	writeTestFile(t, filepath.Join(root, "platform", "local-only", "cm.yaml"), plainConfigMapYAML("local-only"))

	result, err := Orchestrator{}.Build(context.Background(), ociBuildRequest(t, root))
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got, want := generatedApplicationNames(result), []string{"cert-manager", "prometheus"}; !slices.Equal(got, want) {
		t.Fatalf("applications = %v, want %v", got, want)
	}
	assertManifestNamed(t, result.Manifests, "cert-manager-from-oci")
	assertManifestNamed(t, result.Manifests, "prometheus-from-oci")
	for _, name := range []string{"excluded-from-oci", "hidden-from-oci", "local-only"} {
		if _, ok := manifestByName(result.Manifests, name); ok {
			t.Fatalf("manifest %q rendered, want it filtered out", name)
		}
	}
	// One real acquisition: the generator's fetch and both Applications'
	// renders share the session memo.
	if got := ociEventCount(result.CacheEvents); got != 1 {
		t.Fatalf("oci cache events = %d, want 1: %#v", got, result.CacheEvents)
	}
	// Artifact paths are not repository paths: generated Applications own
	// only the ApplicationSet file for changed-only selection and the
	// persistent render cache's local input digests.
	for _, input := range result.ApplicationInputs {
		if got := input.Paths; !slices.Equal(got, []string{"apps/platform-appset.yaml"}) {
			t.Fatalf("%s input paths = %v, want only the ApplicationSet file", input.Application.Name, got)
		}
	}
	// Changed-only: a local edit at an artifact-shaped path selects nothing;
	// an ApplicationSet edit selects every generated Application.
	if selected, _ := SelectChangedApplicationInputs(result.ApplicationInputs, []string{"platform/cert-manager/cm.yaml"}); len(selected) != 0 {
		t.Fatalf("local artifact-shaped change selected %d applications, want 0", len(selected))
	}
	if selected, _ := SelectChangedApplicationInputs(result.ApplicationInputs, []string{"apps/platform-appset.yaml"}); len(selected) != 2 {
		t.Fatalf("ApplicationSet change selected %d applications, want 2", len(selected))
	}
}

func TestBuildOCIGeneratorOffline(t *testing.T) {
	reg := ocitest.StartRegistry(t)
	pushOCIPlatformArtifact(t, reg)
	repoURL := reg.RepoURL("platform")
	ociCacheDir := t.TempDir()

	root := t.TempDir()
	writeOCIGeneratorAppSet(t, root, repoURL, "v1")
	if _, err := (Orchestrator{}).Build(context.Background(), BuildRequest{Path: root, OCICacheDir: ociCacheDir}); err != nil {
		t.Fatalf("online Build() error = %v", err)
	}
	reg.Server.Close()

	offline, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root, OCICacheDir: ociCacheDir, Offline: true, RecordCacheEvents: true})
	if err != nil {
		t.Fatalf("offline Build() error = %v", err)
	}
	if got, want := generatedApplicationNames(offline), []string{"cert-manager", "prometheus"}; !slices.Equal(got, want) {
		t.Fatalf("offline applications = %v, want %v", got, want)
	}
	sawOfflineHit := false
	for _, event := range offline.CacheEvents {
		if event.Source == cacheevent.SourceOCI && event.Action == cacheevent.ActionHit && event.Offline {
			sawOfflineHit = true
		}
	}
	if !sawOfflineHit {
		t.Fatalf("offline events missing oci hit: %#v", offline.CacheEvents)
	}

	unseen := t.TempDir()
	writeOCIGeneratorAppSet(t, unseen, repoURL, "v9")
	_, err = Orchestrator{}.Build(context.Background(), BuildRequest{Path: unseen, OCICacheDir: ociCacheDir, Offline: true})
	assertBuildErrorContains(t, err, "offline cache miss")
	assertBuildErrorContains(t, err, "oci generator")
}

// Nested use: a matrix of a local Git directories generator and an oci files
// generator. Params come from the artifact, manifests from the local tree,
// and the generated Applications own only local paths.
func TestBuildOCIGeneratorNestedInMatrixWithGitDirectories(t *testing.T) {
	reg := ocitest.StartRegistry(t)
	ocitest.PushPlainManifestsArtifact(t, reg, "configs", "2.0.0", map[string]string{
		"clusters/east.yaml":   "cluster: east\nlabel: blue\n",
		"clusters/west.yaml":   "cluster: west\nlabel: green\n",
		"clusters/ignored.txt": "not a param file\n",
	})
	repoURL := reg.RepoURL("configs")
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "workloads", "web", "cm.yaml"), plainConfigMapYAML("web"))
	writeTestFile(t, filepath.Join(root, "apps", "matrix-appset.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: matrix
  namespace: argocd
spec:
  goTemplate: true
  goTemplateOptions: ["missingkey=error"]
  generators:
    - matrix:
        generators:
          - git:
              repoURL: https://github.com/example/repo
              revision: HEAD
              directories:
                - path: workloads/*
          - oci:
              repoURL: `+repoURL+`
              revision: "2.0.0"
              pathParamPrefix: cfg
              files:
                - path: clusters/*.yaml
  template:
    metadata:
      name: '{{.path.basename}}-{{.cluster}}'
      labels:
        color: '{{.label}}'
        cfg-dir: '{{.cfg.path.basename}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        targetRevision: HEAD
        path: '{{.path.path}}'
      destination:
        name: in-cluster
        namespace: '{{.cluster}}'
`)

	result, err := Orchestrator{}.Build(context.Background(), ociBuildRequest(t, root))
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got, want := generatedApplicationNames(result), []string{"web-east", "web-west"}; !slices.Equal(got, want) {
		t.Fatalf("applications = %v, want %v", got, want)
	}
	for _, application := range result.Applications {
		if application.Labels["cfg-dir"] != "clusters" || application.Labels["color"] == "" {
			t.Fatalf("%s labels = %v, want artifact params", application.Name, application.Labels)
		}
	}
	for _, input := range result.ApplicationInputs {
		want := []string{"apps/matrix-appset.yaml", "workloads/web"}
		if !slices.Equal(input.Paths, want) {
			t.Fatalf("%s input paths = %v, want %v", input.Application.Name, input.Paths, want)
		}
	}
}

// A --repo-map entry for the generator's repoURL wins over the registry,
// exactly as it does for oci:// Application sources.
func TestBuildOCIGeneratorHonoursRepoMap(t *testing.T) {
	mapped := t.TempDir()
	writeTestFile(t, filepath.Join(mapped, "platform", "mapped-app", "cm.yaml"), plainConfigMapYAML("mapped"))
	root := t.TempDir()
	repoURL := "oci://registry.example.invalid/platform"
	writeOCIGeneratorAppSet(t, root, repoURL, "v1")
	fake := &fakeOCIArtifactAcquirer{digest: "sha256:" + strings.Repeat("ab", 32)}
	request := ociBuildRequest(t, root)
	request.RepoMaps = []sourcepkg.RepoMap{{URL: repoURL, Path: mapped}}

	result, err := Orchestrator{OCIArtifactAcquirer: fake}.Build(context.Background(), request)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got := generatedApplicationNames(result); !slices.Equal(got, []string{"mapped-app"}) {
		t.Fatalf("applications = %v, want [mapped-app]", got)
	}
	assertManifestNamed(t, result.Manifests, "mapped")
	if resolves, extracts := fake.counts(); resolves != 0 || extracts != 0 {
		t.Fatalf("artifact acquirer hit despite repo-map: %d resolves %d extracts", resolves, extracts)
	}
}

// Without oci generators the fetcher never builds a provider, so a broken
// --registry-config is never opened.
func TestBuildWithoutOCIGeneratorIgnoresMissingRegistryConfig(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "list-appset.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: list
  namespace: argocd
spec:
  generators:
    - list:
        elements:
          - name: one
  template:
    metadata:
      name: '{{name}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        targetRevision: HEAD
        path: workloads
      destination:
        name: in-cluster
        namespace: default
`)
	writeTestFile(t, filepath.Join(root, "workloads", "cm.yaml"), plainConfigMapYAML("one"))
	request := ociBuildRequest(t, root)
	request.OCICredentials.RegistryConfig = filepath.Join(t.TempDir(), "missing.json")
	if _, err := (Orchestrator{}).Build(context.Background(), request); err != nil {
		t.Fatalf("Build() error = %v", err)
	}
}

// The other half of the lazy contract: an oci generator reads
// --registry-config exactly like an oci:// source, failing before the
// acquirer is asked anything.
func TestBuildOCIGeneratorFailsOnMissingRegistryConfig(t *testing.T) {
	root := t.TempDir()
	writeOCIGeneratorAppSet(t, root, "oci://registry.example.test/org/platform", "v1")
	fake := &fakeOCIArtifactAcquirer{digest: "sha256:" + strings.Repeat("ab", 32)}
	request := ociBuildRequest(t, root)
	request.OCICredentials.RegistryConfig = filepath.Join(t.TempDir(), "missing.json")

	_, err := Orchestrator{OCIArtifactAcquirer: fake}.Build(context.Background(), request)
	assertBuildErrorContains(t, err, "--registry-config")
	assertBuildErrorContains(t, err, "oci generator")
	if resolves, extracts := fake.counts(); resolves != 0 || extracts != 0 {
		t.Fatalf("acquirer calls = (%d resolves, %d extracts), want none", resolves, extracts)
	}
}

// Two-sided diff at one revision: the generator fetch is memoized across
// sides with the Applications' own renders.
func TestDiffOCIGeneratorTwoSidedSingleAcquisition(t *testing.T) {
	reg := ocitest.StartRegistry(t)
	pushOCIPlatformArtifact(t, reg)
	repoURL := reg.RepoURL("platform")
	left := t.TempDir()
	right := t.TempDir()
	writeOCIGeneratorAppSet(t, left, repoURL, "v1")
	writeOCIGeneratorAppSet(t, right, repoURL, "v1")

	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		LeftPath:          left,
		RightPath:         right,
		Unified:           3,
		OCICacheDir:       t.TempDir(),
		RecordCacheEvents: true,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v", err)
	}
	if len(result.Results) != 0 {
		t.Fatalf("Results = %#v, want none for identical sides", result.Results)
	}
	if got := ociEventCount(result.CacheEvents); got != 1 {
		t.Fatalf("oci cache events = %d, want 1: %#v", got, result.CacheEvents)
	}
}

// Persistent render cache keying for OCI-generated Applications: artifact
// content reaches renders only through the generated Application spec,
// which the cache key covers. Re-pushing the generator artifact's tag with a
// changed param must re-render the affected Application even though the
// local repository is clean and unchanged, while an Application whose
// generated spec did not change still hits.
func TestPersistentCacheOCIGeneratorParamChangeInvalidatesOnlyAffectedApp(t *testing.T) {
	reg := ocitest.StartRegistry(t)
	push := func(alphaDir string) {
		ocitest.PushPlainManifestsArtifact(t, reg, "configs", "stable", map[string]string{
			"clusters/alpha.yaml": "name: alpha\ndir: " + alphaDir + "\n",
			"clusters/beta.yaml":  "name: beta\ndir: beta\n",
		})
	}
	push("alpha")
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "appset.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: generated
  namespace: argocd
spec:
  goTemplate: true
  generators:
    - oci:
        repoURL: `+reg.RepoURL("configs")+`
        revision: stable
        files:
          - path: clusters/*.yaml
  template:
    metadata:
      name: "{{.name}}"
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        targetRevision: main
        path: "manifests/{{.dir}}"
        directory: {}
      destination:
        name: in-cluster
        namespace: default
`)
	writePersistentDirectorySource(t, root, "alpha", "from-alpha")
	writePersistentDirectorySource(t, root, "alpha-next", "from-alpha-next")
	writePersistentDirectorySource(t, root, "beta", "from-beta")
	gitCommitAll(t, root, "initial")
	request := persistentBuildRequest(root, t.TempDir())
	request.OCICacheDir = t.TempDir()
	request.RecordCacheEvents = true

	cold, err := (Orchestrator{}).Build(context.Background(), request)
	if err != nil {
		t.Fatalf("cold Build() error = %v", err)
	}
	if got := persistentConfigMapDataValue(t, cold, "alpha", "alpha", "value"); got != "from-alpha" {
		t.Fatalf("cold alpha value = %q", got)
	}

	push("alpha-next")
	orchestrator := Orchestrator{}
	renderedPaths := renderedSourceCounts(&orchestrator)
	warm, err := orchestrator.Build(context.Background(), request)
	if err != nil {
		t.Fatalf("warm Build() error = %v", err)
	}
	if got := persistentConfigMapDataValue(t, warm, "alpha", "alpha-next", "value"); got != "from-alpha-next" {
		t.Fatalf("warm alpha value = %q, want the re-pushed artifact's param to take effect", got)
	}
	if got := renderedPaths["manifests/alpha-next"]; got != 1 {
		t.Fatalf("alpha renders after artifact repush = %d, want 1; all renders = %#v", got, renderedPaths)
	}
	if got := renderedPaths["manifests/beta"]; got != 0 {
		t.Fatalf("beta renders after artifact repush = %d, want 0 (cache hit); all renders = %#v", got, renderedPaths)
	}
	if !hasRenderCacheEvent(warm.CacheEvents, cacheevent.ActionHit, "argocd/beta", "") {
		t.Fatalf("CacheEvents = %#v, want beta persistent hit", warm.CacheEvents)
	}
}
