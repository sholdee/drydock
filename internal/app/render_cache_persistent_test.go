package app

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/filedigest"
	"github.com/sholdee/drydock/internal/gitref"
	"github.com/sholdee/drydock/internal/render"
	"github.com/sholdee/drydock/internal/rendercache"
	sourcepkg "github.com/sholdee/drydock/internal/source"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func testEngineFingerprint() rendercache.EngineFingerprint {
	return rendercache.EngineFingerprint{
		Version:            "test",
		Commit:             "0123456789abcdef0123456789abcdef01234567",
		ArgoCDModule:       "github.com/argoproj/argo-cd/v3@v3.0.0-test",
		GitOpsEngineModule: "github.com/argoproj/argo-cd/gitops-engine@v0.0.0-test",
		HelmModule:         "helm.sh/helm/v4@v4.0.0-test",
		KustomizeModule:    "sigs.k8s.io/kustomize/api@v0.0.0-test",
		JsonnetModule:      "github.com/google/go-jsonnet@v0.0.0-test",
		KubernetesModule:   "k8s.io/apimachinery@v0.0.0-test",
	}
}

func testRenderCacheOptions(t *testing.T) RenderCacheOptions {
	t.Helper()
	return RenderCacheOptions{
		RenderCacheEnabled: true,
		RenderCacheDir:     t.TempDir(),
		EngineFingerprint:  testEngineFingerprint(),
	}
}

func TestEnsurePersistentRenderCacheDisabledByDefault(t *testing.T) {
	request, release := ensurePersistentRenderCache(BuildRequest{})
	if request.persistentRenderCache != nil {
		t.Fatalf("persistentRenderCache = %v, want nil when disabled", request.persistentRenderCache)
	}
	if events := release(); len(events) != 0 {
		t.Fatalf("release() = %v, want no events", events)
	}
}

func TestEnsurePersistentRenderCacheOpensStoreAndReusesHandle(t *testing.T) {
	request := BuildRequest{RenderCacheOptions: testRenderCacheOptions(t)}
	prepared, release := ensurePersistentRenderCache(request)
	if prepared.persistentRenderCache == nil || !prepared.persistentRenderCache.active() {
		t.Fatalf("persistentRenderCache not active after ensure")
	}
	again, releaseAgain := ensurePersistentRenderCache(prepared)
	if again.persistentRenderCache != prepared.persistentRenderCache {
		t.Fatalf("ensure created a second handle instead of reusing")
	}
	if events := releaseAgain(); len(events) != 0 {
		t.Fatalf("inner release() = %v, want no events (creator owns the sweep)", events)
	}
	_ = release()
}

func TestEnsurePersistentRenderCacheDevBuildGuard(t *testing.T) {
	options := testRenderCacheOptions(t)
	options.EngineFingerprint = rendercache.EngineFingerprint{Version: "dev", Commit: "none"}
	request, release := ensurePersistentRenderCache(BuildRequest{RenderCacheOptions: options})
	if request.persistentRenderCache.active() {
		t.Fatalf("persistent cache active for unknown fingerprint")
	}
	_ = release()
}

func TestEnsurePersistentRenderCacheOpenFailureDegrades(t *testing.T) {
	dir := t.TempDir()
	blocking := dir + "/occupied"
	writeTestFile(t, blocking, "not a directory")
	options := testRenderCacheOptions(t)
	options.RenderCacheDir = blocking
	options.RenderCacheEnabled = true
	request, release := ensurePersistentRenderCache(BuildRequest{
		RenderCacheOptions: options,
		AcquisitionOptions: AcquisitionOptions{RecordCacheEvents: true},
	})
	if request.persistentRenderCache.active() {
		t.Fatalf("persistent cache active after open failure")
	}
	events := release()
	if len(events) != 1 || events[0].Action != "error" || events[0].Source != "render" {
		t.Fatalf("release() = %#v, want one render/error event", events)
	}
}

func TestHandleWorktreeChangeSetMemoizesPerRoot(t *testing.T) {
	repoRoot := t.TempDir()
	writeTestFile(t, repoRoot+"/apps/alpha/cm.yaml", "a: 1\n")
	revision := gitCommitAll(t, repoRoot, "initial")
	writeTestFile(t, repoRoot+"/apps/alpha/untracked.yaml", "u: 1\n")
	handle := newPersistentRenderCache(testRenderCacheOptions(t), false)

	first := handle.worktreeChangeSet(context.Background(), repoRoot)
	if first.State != gitref.WorktreeStateDirty || first.Revision != revision {
		t.Fatalf("changeSet = %+v, want dirty/%s", first, revision)
	}
	if len(first.DirtyPaths) != 1 || first.DirtyPaths[0] != "apps/alpha/untracked.yaml" {
		t.Fatalf("DirtyPaths = %#v, want the untracked file", first.DirtyPaths)
	}

	// Memoized: a second dirty file added after the first call is invisible
	// within the run (same contract as the old worktreeStatus memo).
	writeTestFile(t, repoRoot+"/apps/alpha/second.yaml", "s: 1\n")
	again := handle.worktreeChangeSet(context.Background(), repoRoot)
	if len(again.DirtyPaths) != 1 {
		t.Fatalf("memoized DirtyPaths = %#v, want the first result", again.DirtyPaths)
	}
}

func TestPersistentRenderCacheRootIdentityForRequestModes(t *testing.T) {
	snapshotRevision := "1111111111111111111111111111111111111111"
	snapshotIdentity, snapshotMode, _ := rootIdentityForRequest(context.Background(), t.TempDir(), BuildRequest{rootRevision: snapshotRevision})
	if snapshotIdentity != (SourceIdentity{Kind: sourceIdentityKindRoot, Revision: snapshotRevision}) || snapshotMode != rootInputModeSnapshot {
		t.Fatalf("snapshot root identity = %#v/%s, want revision snapshot", snapshotIdentity, snapshotMode)
	}

	disabledIdentity, disabledMode, _ := rootIdentityForRequest(context.Background(), t.TempDir(), BuildRequest{})
	if disabledIdentity != (SourceIdentity{Kind: sourceIdentityKindRoot}) || disabledMode != rootInputModeUnknown {
		t.Fatalf("cache-disabled root identity = %#v/%s, want unknown root", disabledIdentity, disabledMode)
	}

	cleanRoot := t.TempDir()
	writeTestFile(t, cleanRoot+"/manifests/demo/kustomization.yaml", "kind: Kustomization\n")
	cleanRevision := gitCommitAll(t, cleanRoot, "initial")
	cleanHandle := newPersistentRenderCache(testRenderCacheOptions(t), false)
	cleanIdentity, cleanMode, _ := rootIdentityForRequest(context.Background(), cleanRoot, BuildRequest{persistentRenderCache: cleanHandle})
	if cleanIdentity != (SourceIdentity{Kind: sourceIdentityKindRoot, Revision: cleanRevision}) || cleanMode != rootInputModeClean {
		t.Fatalf("clean root identity = %#v/%s, want clean HEAD %s", cleanIdentity, cleanMode, cleanRevision)
	}

	dirtyRoot := t.TempDir()
	writeTestFile(t, dirtyRoot+"/manifests/demo/kustomization.yaml", "kind: Kustomization\n")
	dirtyRevision := gitCommitAll(t, dirtyRoot, "initial")
	writeTestFile(t, dirtyRoot+"/scratch.yaml", "kind: ConfigMap\n")
	dirtyHandle := newPersistentRenderCache(testRenderCacheOptions(t), false)
	dirtyIdentity, dirtyMode, dirtyPaths := rootIdentityForRequest(context.Background(), dirtyRoot, BuildRequest{persistentRenderCache: dirtyHandle})
	if dirtyIdentity != (SourceIdentity{Kind: sourceIdentityKindRoot, Revision: dirtyRevision}) || dirtyMode != rootInputModeDirty {
		t.Fatalf("dirty root identity = %#v/%s, want dirty HEAD %s", dirtyIdentity, dirtyMode, dirtyRevision)
	}
	if len(dirtyPaths) != 1 || dirtyPaths[0] != "scratch.yaml" {
		t.Fatalf("dirty paths = %#v, want [scratch.yaml]", dirtyPaths)
	}

	unknownHandle := newPersistentRenderCache(testRenderCacheOptions(t), false)
	unknownIdentity, unknownMode, _ := rootIdentityForRequest(context.Background(), t.TempDir(), BuildRequest{persistentRenderCache: unknownHandle})
	if unknownIdentity != (SourceIdentity{Kind: sourceIdentityKindRoot}) || unknownMode != rootInputModeUnknown {
		t.Fatalf("non-git root identity = %#v/%s, want unknown", unknownIdentity, unknownMode)
	}
}

func TestResolveSourceRootIdentityVariants(t *testing.T) {
	repoRoot := t.TempDir()
	writeTestFile(t, repoRoot+"/manifests/demo/kustomization.yaml", "kind: Kustomization\n")
	mappedDir := t.TempDir()
	acquiredDir := t.TempDir()
	rootIdentity := SourceIdentity{Kind: sourceIdentityKindRoot, Revision: "1111111111111111111111111111111111111111"}

	provider := localProvider{
		repoRoot: repoRoot,
		sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{
			RepoMaps: []sourcepkg.RepoMap{{URL: "https://git.example.test/mapped/repo.git", Path: mappedDir}},
		}),
		gitAcquirer: &recordingGitAcquirer{
			path:     acquiredDir,
			revision: "2222222222222222222222222222222222222222",
		},
		rootIdentity: rootIdentity,
	}

	cases := []struct {
		name         string
		source       render.ResolvedSource
		wantIdentity SourceIdentity
	}{
		{
			name:         "chart-only source keys on spec version",
			source:       render.ResolvedSource{Chart: "demo", RepoURL: "https://charts.example.test", TargetRevision: "1.2.3"},
			wantIdentity: SourceIdentity{Kind: sourceIdentityKindChart, Locator: "https://charts.example.test::demo", Revision: "1.2.3"},
		},
		{
			name:         "chart-only without version yields no identity",
			source:       render.ResolvedSource{Chart: "demo", RepoURL: "https://charts.example.test"},
			wantIdentity: SourceIdentity{},
		},
		{
			name:         "repo-mapped source yields no identity",
			source:       render.ResolvedSource{RepoURL: "https://git.example.test/mapped/repo.git", Path: "manifests/demo", TargetRevision: "main"},
			wantIdentity: SourceIdentity{},
		},
		{
			name:         "local path uses provider root identity",
			source:       render.ResolvedSource{RepoURL: "https://git.example.test/org/repo.git", Path: "manifests/demo", TargetRevision: "main"},
			wantIdentity: rootIdentity,
		},
		{
			name:         "external git uses acquired revision",
			source:       render.ResolvedSource{RepoURL: "https://git.example.test/other/repo.git", Path: "elsewhere", TargetRevision: "main"},
			wantIdentity: SourceIdentity{Kind: sourceIdentityKindGit, Locator: sourcepkg.NormalizeURL("https://git.example.test/other/repo.git"), Revision: "2222222222222222222222222222222222222222"},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, identity, err := provider.resolveSourceRootIdentity(context.Background(), testCase.source)
			if err != nil {
				t.Fatalf("resolveSourceRootIdentity() error = %v", err)
			}
			if identity != testCase.wantIdentity {
				t.Fatalf("identity = %#v, want %#v", identity, testCase.wantIdentity)
			}
		})
	}
}

func mustPlan(t *testing.T, application argoappv1.Application) PlanResult {
	t.Helper()
	plan, err := Plan(application)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	return plan
}

func mustCollectSingleSourceIdentity(t *testing.T, provider localProvider, plan PlanResult) SourceIdentity {
	t.Helper()
	identities := mustCollectSourceIdentities(t, provider, plan)
	if len(identities) != 1 {
		t.Fatalf("identities = %#v, want one identity", identities)
	}
	return identities[0]
}

func mustCollectSourceIdentities(t *testing.T, provider localProvider, plan PlanResult) []SourceIdentity {
	t.Helper()
	handle := &persistentRenderCache{
		digests:           map[string]gitref.PathDigestResult{},
		filesystemDigests: map[string]filedigest.PathDigestResult{},
	}
	identities, reason, ok := collectSourceIdentities(context.Background(), handle, provider, plan)
	if !ok {
		t.Fatalf("collectSourceIdentities() ok = false, reason = %s", reason)
	}
	return identities
}

func TestCollectSourceIdentitiesOrdersSourcesThenRefs(t *testing.T) {
	repoRoot := t.TempDir()
	writeTestFile(t, repoRoot+"/manifests/demo/kustomization.yaml", "kind: Kustomization\n")
	rootIdentity := SourceIdentity{Kind: sourceIdentityKindRoot, Revision: "1111111111111111111111111111111111111111"}
	provider := localProvider{
		repoRoot:       repoRoot,
		sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{}),
		gitAcquirer: &recordingGitAcquirer{
			path:     t.TempDir(),
			revision: "3333333333333333333333333333333333333333",
		},
		rootIdentity:  rootIdentity,
		rootInputMode: rootInputModeSnapshot,
	}
	application := argoappv1.Application{Spec: argoappv1.ApplicationSpec{Sources: argoappv1.ApplicationSources{
		{RepoURL: "https://git.example.test/org/repo.git", Path: "manifests/demo", TargetRevision: "main"},
		{RepoURL: "https://git.example.test/org/values.git", TargetRevision: "main", Ref: "values"},
	}}}
	plan := mustPlan(t, application)

	identities, reason, ok := collectSourceIdentities(context.Background(), nil, provider, plan)
	if !ok {
		t.Fatalf("collectSourceIdentities() ok = false, reason = %s", reason)
	}
	valuesIdentity := SourceIdentity{Kind: sourceIdentityKindGit, Locator: sourcepkg.NormalizeURL("https://git.example.test/org/values.git"), Revision: "3333333333333333333333333333333333333333"}
	want := []SourceIdentity{rootIdentity, valuesIdentity, valuesIdentity}
	if !reflect.DeepEqual(identities, want) {
		t.Fatalf("identities = %#v, want %#v", identities, want)
	}
}

func TestCollectSourceIdentitiesSameRepoRefReusesSourceIdentity(t *testing.T) {
	// A ref-only source sharing repo+revision with a render source must not be
	// acquired externally; the real render path roots it at the render source.
	repoRoot := t.TempDir()
	writeTestFile(t, repoRoot+"/manifests/demo/kustomization.yaml", "kind: Kustomization\n")
	rootIdentity := SourceIdentity{Kind: sourceIdentityKindRoot, Revision: "1111111111111111111111111111111111111111"}
	failingAcquirer := &recordingGitAcquirer{err: fmt.Errorf("must not acquire same-repo ref")}
	provider := localProvider{
		repoRoot:       repoRoot,
		sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{}),
		gitAcquirer:    failingAcquirer,
		rootIdentity:   rootIdentity,
		rootInputMode:  rootInputModeSnapshot,
	}
	application := argoappv1.Application{Spec: argoappv1.ApplicationSpec{Sources: argoappv1.ApplicationSources{
		{RepoURL: "https://git.example.test/org/repo.git", Path: "manifests/demo", TargetRevision: "main"},
		{RepoURL: "https://git.example.test/org/repo.git", TargetRevision: "main", Ref: "self"},
	}}}
	plan := mustPlan(t, application)

	identities, reason, ok := collectSourceIdentities(context.Background(), nil, provider, plan)
	if !ok {
		t.Fatalf("collectSourceIdentities() ok = false, reason = %s", reason)
	}
	want := []SourceIdentity{rootIdentity, rootIdentity, rootIdentity}
	if !reflect.DeepEqual(identities, want) {
		t.Fatalf("identities = %#v, want %#v", identities, want)
	}
	if len(failingAcquirer.requests) != 0 {
		t.Fatalf("same-repo ref was acquired externally: %v", failingAcquirer.requests)
	}
}

func TestCollectSourceIdentitiesExternalSameRepoRefDoesNotAcquireTwice(t *testing.T) {
	acquirer := &recordingGitAcquirer{
		path:     t.TempDir(),
		revision: "2222222222222222222222222222222222222222",
	}
	provider := localProvider{
		repoRoot:       t.TempDir(),
		sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{}),
		gitAcquirer:    acquirer,
		rootIdentity:   SourceIdentity{Kind: sourceIdentityKindRoot},
	}
	application := argoappv1.Application{Spec: argoappv1.ApplicationSpec{Sources: argoappv1.ApplicationSources{
		{RepoURL: "https://git.example.test/org/repo.git", Path: "remote-path", TargetRevision: "main"},
		{RepoURL: "https://git.example.test/org/repo.git", TargetRevision: "main", Ref: "values"},
	}}}
	plan := mustPlan(t, application)

	identities, reason, ok := collectSourceIdentities(context.Background(), nil, provider, plan)
	if !ok {
		t.Fatalf("collectSourceIdentities() ok = false, reason = %s", reason)
	}
	wantIdentity := SourceIdentity{Kind: sourceIdentityKindGit, Locator: sourcepkg.NormalizeURL("https://git.example.test/org/repo.git"), Revision: "2222222222222222222222222222222222222222"}
	want := []SourceIdentity{wantIdentity, wantIdentity, wantIdentity}
	if !reflect.DeepEqual(identities, want) {
		t.Fatalf("identities = %#v, want %#v", identities, want)
	}
	if len(acquirer.requests) != 1 {
		t.Fatalf("git acquisitions = %d, want exactly one for external same-repo source+ref: %v", len(acquirer.requests), acquirer.requests)
	}
}

func TestPersistentRenderCacheCollectSourceIdentitiesDirtyDirectoryUsesWorktreeInputs(t *testing.T) {
	repoRoot := t.TempDir()
	writeTestFile(t, repoRoot+"/manifests/demo/cm.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\n")
	rootRevision := gitCommitAll(t, repoRoot, "initial")
	writeTestFile(t, repoRoot+"/manifests/demo/dirty.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: dirty\n")
	handle := &persistentRenderCache{filesystemDigests: map[string]filedigest.PathDigestResult{}}
	provider := localProvider{
		repoRoot:       repoRoot,
		sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{}),
		rootIdentity:   SourceIdentity{Kind: sourceIdentityKindRoot, Revision: rootRevision},
		rootInputMode:  rootInputModeDirty,
	}
	application := argoappv1.Application{Spec: argoappv1.ApplicationSpec{Source: &argoappv1.ApplicationSource{
		RepoURL: "https://git.example.test/org/repo.git", Path: "manifests/demo", TargetRevision: "main",
	}}}
	plan := mustPlan(t, application)

	identities, reason, ok := collectSourceIdentities(context.Background(), handle, provider, plan)
	if !ok {
		t.Fatalf("collectSourceIdentities() ok = false, reason = %s", reason)
	}
	if len(identities) != 1 {
		t.Fatalf("identities = %#v, want one identity", identities)
	}
	if identities[0].Kind != sourceIdentityKindWorktreeInputs || identities[0].Digest == "" || identities[0].Revision != "" {
		t.Fatalf("identity = %#v, want worktree-inputs digest without revision", identities[0])
	}
}

func TestPersistentRenderCacheCollectSourceIdentitiesDirtyHelmDigestTracksSourceGraph(t *testing.T) {
	repoRoot := t.TempDir()
	writeTestFile(t, repoRoot+"/charts/demo/Chart.yaml", "apiVersion: v2\nname: demo\nversion: 0.1.0\n")
	writeTestFile(t, repoRoot+"/charts/demo/templates/cm.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\n")
	writeTestFile(t, repoRoot+"/charts/demo/values.yaml", "name: demo\n")
	rootRevision := gitCommitAll(t, repoRoot, "initial")
	provider := localProvider{
		repoRoot:       repoRoot,
		sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{}),
		rootIdentity:   SourceIdentity{Kind: sourceIdentityKindRoot, Revision: rootRevision},
		rootInputMode:  rootInputModeDirty,
	}
	application := argoappv1.Application{Spec: argoappv1.ApplicationSpec{Source: &argoappv1.ApplicationSource{
		RepoURL:        "https://git.example.test/org/repo.git",
		Path:           "charts/demo",
		TargetRevision: "main",
		Helm:           &argoappv1.ApplicationSourceHelm{ValueFiles: []string{"values.yaml"}},
	}}}
	plan := mustPlan(t, application)

	first := mustCollectSingleSourceIdentity(t, provider, plan)
	if first.Kind != sourceIdentityKindWorktreeInputs || first.Digest == "" {
		t.Fatalf("identity = %#v, want worktree-inputs digest", first)
	}
	again := mustCollectSingleSourceIdentity(t, provider, plan)
	if again != first {
		t.Fatalf("identity changed without source content changes:\nfirst=%#v\nagain=%#v", first, again)
	}

	writeTestFile(t, repoRoot+"/README.md", "unrelated dirty file\n")
	unrelated := mustCollectSingleSourceIdentity(t, provider, plan)
	if unrelated != first {
		t.Fatalf("identity changed for dirty file outside source graph:\nfirst=%#v\nunrelated=%#v", first, unrelated)
	}

	writeTestFile(t, repoRoot+"/charts/demo/values.yaml", "name: changed\n")
	changed := mustCollectSingleSourceIdentity(t, provider, plan)
	if changed.Kind != sourceIdentityKindWorktreeInputs || changed.Digest == "" {
		t.Fatalf("changed identity = %#v, want worktree-inputs digest", changed)
	}
	if changed == first {
		t.Fatalf("identity did not change after dirty source file mutation: %#v", changed)
	}
}

func TestPersistentRenderCacheCollectSourceIdentitiesDirtyKustomizeBaseTracksGraph(t *testing.T) {
	repoRoot := t.TempDir()
	writeTestFile(t, repoRoot+"/manifests/demo/kustomization.yaml", `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ../../bases/shared
`)
	writeTestFile(t, repoRoot+"/bases/shared/kustomization.yaml", `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cm.yaml
`)
	writeTestFile(t, repoRoot+"/bases/shared/cm.yaml", `apiVersion: v1
kind: ConfigMap
metadata:
  name: shared
data:
  value: initial
`)
	rootRevision := gitCommitAll(t, repoRoot, "initial")
	provider := localProvider{
		repoRoot:       repoRoot,
		sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{}),
		rootIdentity:   SourceIdentity{Kind: sourceIdentityKindRoot, Revision: rootRevision},
		rootInputMode:  rootInputModeDirty,
	}
	application := argoappv1.Application{Spec: argoappv1.ApplicationSpec{Source: &argoappv1.ApplicationSource{
		RepoURL:        "https://git.example.test/org/repo.git",
		Path:           "manifests/demo",
		TargetRevision: "main",
	}}}
	plan := mustPlan(t, application)

	first := mustCollectSingleSourceIdentity(t, provider, plan)
	if first.Kind != sourceIdentityKindWorktreeInputs || first.Digest == "" {
		t.Fatalf("identity = %#v, want worktree-inputs digest", first)
	}

	writeTestFile(t, repoRoot+"/README.md", "unrelated dirty file\n")
	unrelated := mustCollectSingleSourceIdentity(t, provider, plan)
	if unrelated != first {
		t.Fatalf("identity changed for dirty file outside Kustomize graph:\nfirst=%#v\nunrelated=%#v", first, unrelated)
	}

	writeTestFile(t, repoRoot+"/bases/shared/cm.yaml", `apiVersion: v1
kind: ConfigMap
metadata:
  name: shared
data:
  value: changed
`)
	changed := mustCollectSingleSourceIdentity(t, provider, plan)
	if changed.Kind != sourceIdentityKindWorktreeInputs || changed.Digest == "" {
		t.Fatalf("changed identity = %#v, want worktree-inputs digest", changed)
	}
	if changed == first {
		t.Fatalf("identity did not change after dirty Kustomize base mutation: %#v", changed)
	}
}

func TestPersistentRenderCacheCollectSourceIdentitiesDirtySameRepoValuesDigestTracksRefFile(t *testing.T) {
	repoRoot := t.TempDir()
	writeTestFile(t, repoRoot+"/charts/demo/Chart.yaml", "apiVersion: v2\nname: demo\nversion: 0.1.0\n")
	writeTestFile(t, repoRoot+"/charts/demo/templates/cm.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\n")
	writeTestFile(t, repoRoot+"/shared/values.yaml", "name: demo\n")
	rootRevision := gitCommitAll(t, repoRoot, "initial")
	provider := localProvider{
		repoRoot:       repoRoot,
		sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{}),
		rootIdentity:   SourceIdentity{Kind: sourceIdentityKindRoot, Revision: rootRevision},
		rootInputMode:  rootInputModeDirty,
	}
	application := argoappv1.Application{Spec: argoappv1.ApplicationSpec{Sources: argoappv1.ApplicationSources{
		{RepoURL: "https://git.example.test/org/repo.git", TargetRevision: "main", Ref: "values"},
		{
			RepoURL:        "https://git.example.test/org/repo.git",
			Path:           "charts/demo",
			TargetRevision: "main",
			Helm:           &argoappv1.ApplicationSourceHelm{ValueFiles: []string{"$values/shared/values.yaml"}},
		},
	}}}
	plan := mustPlan(t, application)

	first := mustCollectSourceIdentities(t, provider, plan)
	if len(first) != 3 {
		t.Fatalf("identities = %#v, want render source, ref source, and ref vector identity", first)
	}
	for _, identity := range first {
		if identity.Kind != sourceIdentityKindWorktreeInputs || identity.Digest == "" {
			t.Fatalf("identity = %#v, want worktree-inputs digest", identity)
		}
	}

	writeTestFile(t, repoRoot+"/unrelated.yaml", "kind: ConfigMap\n")
	unrelated := mustCollectSourceIdentities(t, provider, plan)
	if !reflect.DeepEqual(unrelated, first) {
		t.Fatalf("identities changed for dirty file outside source graph:\nfirst=%#v\nunrelated=%#v", first, unrelated)
	}

	writeTestFile(t, repoRoot+"/shared/values.yaml", "name: changed\n")
	changed := mustCollectSourceIdentities(t, provider, plan)
	if reflect.DeepEqual(changed, first) {
		t.Fatalf("identities did not change after same-repo values mutation: %#v", changed)
	}
}

func TestPersistentRenderCacheCollectSourceIdentitiesDirtyKustomizeDigestTracksGraph(t *testing.T) {
	repoRoot := t.TempDir()
	writeTestFile(t, repoRoot+"/manifests/demo/kustomization.yaml", "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - cm.yaml\n")
	writeTestFile(t, repoRoot+"/manifests/demo/cm.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\n")
	rootRevision := gitCommitAll(t, repoRoot, "initial")
	provider := localProvider{
		repoRoot:       repoRoot,
		sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{}),
		rootIdentity:   SourceIdentity{Kind: sourceIdentityKindRoot, Revision: rootRevision},
		rootInputMode:  rootInputModeDirty,
	}
	application := argoappv1.Application{Spec: argoappv1.ApplicationSpec{Source: &argoappv1.ApplicationSource{
		RepoURL: "https://git.example.test/org/repo.git", Path: "manifests/demo", TargetRevision: "main",
	}}}
	plan := mustPlan(t, application)

	first := mustCollectSingleSourceIdentity(t, provider, plan)
	writeTestFile(t, repoRoot+"/README.md", "unrelated\n")
	unrelated := mustCollectSingleSourceIdentity(t, provider, plan)
	if unrelated != first {
		t.Fatalf("identity changed for dirty file outside kustomize graph:\nfirst=%#v\nunrelated=%#v", first, unrelated)
	}

	writeTestFile(t, repoRoot+"/manifests/demo/cm.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: changed\n")
	changed := mustCollectSingleSourceIdentity(t, provider, plan)
	if changed == first {
		t.Fatalf("identity did not change after kustomize graph mutation: %#v", changed)
	}
}

func TestPersistentRenderCacheCollectSourceIdentitiesDirtyDirectoryDigestIncludesUntrackedFiles(t *testing.T) {
	repoRoot := t.TempDir()
	writeTestFile(t, repoRoot+"/manifests/demo/cm.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\n")
	rootRevision := gitCommitAll(t, repoRoot, "initial")
	provider := localProvider{
		repoRoot:       repoRoot,
		sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{}),
		rootIdentity:   SourceIdentity{Kind: sourceIdentityKindRoot, Revision: rootRevision},
		rootInputMode:  rootInputModeDirty,
	}
	application := argoappv1.Application{Spec: argoappv1.ApplicationSpec{Source: &argoappv1.ApplicationSource{
		RepoURL: "https://git.example.test/org/repo.git", Path: "manifests/demo", TargetRevision: "main", Directory: &argoappv1.ApplicationSourceDirectory{},
	}}}
	plan := mustPlan(t, application)

	first := mustCollectSingleSourceIdentity(t, provider, plan)
	writeTestFile(t, repoRoot+"/manifests/other/untracked.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: other\n")
	outside := mustCollectSingleSourceIdentity(t, provider, plan)
	if outside != first {
		t.Fatalf("identity changed for untracked file outside directory source:\nfirst=%#v\noutside=%#v", first, outside)
	}

	writeTestFile(t, repoRoot+"/manifests/demo/untracked.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: untracked\n")
	inside := mustCollectSingleSourceIdentity(t, provider, plan)
	if inside == first {
		t.Fatalf("identity did not change after untracked file inside directory source: %#v", inside)
	}
}

func TestPersistentRenderCacheCollectSourceIdentitiesDirtyJsonnetDigestTracksDeclaredLib(t *testing.T) {
	repoRoot := t.TempDir()
	writeTestFile(t, repoRoot+"/manifests/demo/cm.jsonnet", "local helper = import 'helper.libsonnet'; { apiVersion: 'v1', kind: 'ConfigMap', metadata: { name: helper.name } }\n")
	writeTestFile(t, repoRoot+"/lib/helper.libsonnet", "{ name: 'demo' }\n")
	rootRevision := gitCommitAll(t, repoRoot, "initial")
	provider := localProvider{
		repoRoot:       repoRoot,
		sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{}),
		rootIdentity:   SourceIdentity{Kind: sourceIdentityKindRoot, Revision: rootRevision},
		rootInputMode:  rootInputModeDirty,
	}
	application := argoappv1.Application{Spec: argoappv1.ApplicationSpec{Source: &argoappv1.ApplicationSource{
		RepoURL: "https://git.example.test/org/repo.git", Path: "manifests/demo", TargetRevision: "main",
		Directory: &argoappv1.ApplicationSourceDirectory{Jsonnet: argoappv1.ApplicationSourceJsonnet{Libs: []string{"lib"}}},
	}}}
	plan := mustPlan(t, application)

	first := mustCollectSingleSourceIdentity(t, provider, plan)
	writeTestFile(t, repoRoot+"/lib/helper.libsonnet", "{ name: 'changed' }\n")
	changed := mustCollectSingleSourceIdentity(t, provider, plan)
	if changed == first {
		t.Fatalf("identity did not change after declared jsonnet lib mutation: %#v", changed)
	}
}

func TestPersistentRenderCacheCollectSourceIdentitiesDirtyHelmGlobSkips(t *testing.T) {
	repoRoot := t.TempDir()
	writeTestFile(t, repoRoot+"/charts/demo/Chart.yaml", "apiVersion: v2\nname: demo\nversion: 0.1.0\n")
	writeTestFile(t, repoRoot+"/charts/demo/templates/cm.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\n")
	writeTestFile(t, repoRoot+"/charts/demo/values/one.yaml", "name: one\n")
	rootRevision := gitCommitAll(t, repoRoot, "initial")
	handle := &persistentRenderCache{filesystemDigests: map[string]filedigest.PathDigestResult{}}
	provider := localProvider{
		repoRoot:       repoRoot,
		sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{}),
		rootIdentity:   SourceIdentity{Kind: sourceIdentityKindRoot, Revision: rootRevision},
		rootInputMode:  rootInputModeDirty,
	}
	application := argoappv1.Application{Spec: argoappv1.ApplicationSpec{Source: &argoappv1.ApplicationSource{
		RepoURL:        "https://git.example.test/org/repo.git",
		Path:           "charts/demo",
		TargetRevision: "main",
		Helm:           &argoappv1.ApplicationSourceHelm{ValueFiles: []string{"values/*.yaml"}},
	}}}
	plan := mustPlan(t, application)

	_, reason, ok := collectSourceIdentities(context.Background(), handle, provider, plan)
	if ok {
		t.Fatalf("collectSourceIdentities() ok = true, want dirty Helm glob to skip persistence")
	}
	if reason != renderCacheReasonHelmGlobInputs {
		t.Fatalf("reason = %q, want helm glob inputs", reason)
	}
}

func TestPersistentRenderCacheCollectSourceIdentitiesDirtyMissingRequiredInputSkips(t *testing.T) {
	repoRoot := t.TempDir()
	writeTestFile(t, repoRoot+"/charts/demo/Chart.yaml", "apiVersion: v2\nname: demo\nversion: 0.1.0\n")
	writeTestFile(t, repoRoot+"/charts/demo/templates/cm.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\n")
	rootRevision := gitCommitAll(t, repoRoot, "initial")
	handle := &persistentRenderCache{filesystemDigests: map[string]filedigest.PathDigestResult{}}
	provider := localProvider{
		repoRoot:       repoRoot,
		sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{}),
		rootIdentity:   SourceIdentity{Kind: sourceIdentityKindRoot, Revision: rootRevision},
		rootInputMode:  rootInputModeDirty,
	}
	application := argoappv1.Application{Spec: argoappv1.ApplicationSpec{Source: &argoappv1.ApplicationSource{
		RepoURL:        "https://git.example.test/org/repo.git",
		Path:           "charts/demo",
		TargetRevision: "main",
		Helm:           &argoappv1.ApplicationSourceHelm{ValueFiles: []string{"missing-required.yaml"}},
	}}}
	plan := mustPlan(t, application)

	_, reason, ok := collectSourceIdentities(context.Background(), handle, provider, plan)
	if ok {
		t.Fatalf("collectSourceIdentities() ok = true, want required missing input to skip persistence")
	}
	if reason != renderCacheReasonInputGraph && reason != renderCacheReasonInputDigest {
		t.Fatalf("reason = %q, want input graph or digest reason", reason)
	}
}

func TestPersistentRenderCacheCollectSourceIdentitiesDirtyUnsafeGraphSkips(t *testing.T) {
	repoRoot := t.TempDir()
	writeTestFile(t, repoRoot+"/manifests/demo/kustomization.yaml", "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - ../../../outside/cm.yaml\n")
	rootRevision := gitCommitAll(t, repoRoot, "initial")
	handle := &persistentRenderCache{filesystemDigests: map[string]filedigest.PathDigestResult{}}
	provider := localProvider{
		repoRoot:       repoRoot,
		sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{}),
		rootIdentity:   SourceIdentity{Kind: sourceIdentityKindRoot, Revision: rootRevision},
		rootInputMode:  rootInputModeDirty,
	}
	application := argoappv1.Application{Spec: argoappv1.ApplicationSpec{Source: &argoappv1.ApplicationSource{
		RepoURL: "https://git.example.test/org/repo.git", Path: "manifests/demo", TargetRevision: "main",
	}}}
	plan := mustPlan(t, application)

	_, reason, ok := collectSourceIdentities(context.Background(), handle, provider, plan)
	if ok {
		t.Fatalf("collectSourceIdentities() ok = true, want unsafe graph to skip persistence")
	}
	if reason != renderCacheReasonInputGraph {
		t.Fatalf("reason = %q, want input graph", reason)
	}
}

func TestPersistentRenderCacheCollectSourceIdentitiesUnknownRootReason(t *testing.T) {
	repoRoot := t.TempDir()
	writeTestFile(t, repoRoot+"/manifests/demo/kustomization.yaml", "kind: Kustomization\n")
	provider := localProvider{
		repoRoot:       repoRoot,
		sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{}),
		rootIdentity:   SourceIdentity{Kind: sourceIdentityKindRoot},
		rootInputMode:  rootInputModeUnknown,
	}
	application := argoappv1.Application{Spec: argoappv1.ApplicationSpec{Source: &argoappv1.ApplicationSource{
		RepoURL: "https://git.example.test/org/repo.git", Path: "manifests/demo", TargetRevision: "main",
	}}}
	plan := mustPlan(t, application)

	_, reason, ok := collectSourceIdentities(context.Background(), nil, provider, plan)
	if ok {
		t.Fatalf("collectSourceIdentities() ok = true, want ineligible for unknown root")
	}
	if reason != "ineligible-source" {
		t.Fatalf("reason = %q, want ineligible-source", reason)
	}
}

func TestPersistentRenderCacheCollectSourceIdentitiesRepoMappedReason(t *testing.T) {
	mappedDir := t.TempDir()
	writeTestFile(t, mappedDir+"/manifests/demo/kustomization.yaml", "kind: Kustomization\n")
	provider := localProvider{
		repoRoot: t.TempDir(),
		sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{
			RepoMaps: []sourcepkg.RepoMap{{URL: "https://git.example.test/mapped/repo.git", Path: mappedDir}},
		}),
		rootIdentity:  SourceIdentity{Kind: sourceIdentityKindRoot, Revision: "1111111111111111111111111111111111111111"},
		rootInputMode: rootInputModeDirty,
	}
	application := argoappv1.Application{Spec: argoappv1.ApplicationSpec{Source: &argoappv1.ApplicationSource{
		RepoURL: "https://git.example.test/mapped/repo.git", Path: "manifests/demo", TargetRevision: "main",
	}}}
	plan := mustPlan(t, application)

	_, reason, ok := collectSourceIdentities(context.Background(), nil, provider, plan)
	if ok {
		t.Fatalf("collectSourceIdentities() ok = true, want ineligible for repo-mapped source")
	}
	if reason != "ineligible-source" {
		t.Fatalf("reason = %q, want ineligible-source", reason)
	}
}

func TestPersistentRenderCachePrepareDirtyPluginSourceIneligible(t *testing.T) {
	repoRoot := t.TempDir()
	writeTestFile(t, repoRoot+"/manifests/demo/plugin-input.yaml", "kind: ConfigMap\n")
	rootRevision := gitCommitAll(t, repoRoot, "initial")
	recorder := cacheevent.NewRecorder(true)
	handle := &persistentRenderCache{
		store:             &rendercache.Store{},
		fingerprint:       testEngineFingerprint(),
		filesystemDigests: map[string]filedigest.PathDigestResult{},
	}
	provider := localProvider{
		repoRoot:       repoRoot,
		sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{}),
		rootIdentity:   SourceIdentity{Kind: sourceIdentityKindRoot, Revision: rootRevision},
		rootInputMode:  rootInputModeDirty,
		cacheEvents:    recorder,
	}
	application := argoappv1.Application{
		ObjectMeta: metav1.ObjectMeta{Name: "plugin-app", Namespace: "argocd"},
		Spec: argoappv1.ApplicationSpec{Source: &argoappv1.ApplicationSource{
			RepoURL:        "https://git.example.test/org/repo.git",
			Path:           "manifests/demo",
			TargetRevision: "main",
			Plugin:         &argoappv1.ApplicationSourcePlugin{Name: "example"},
		}},
	}
	plan := mustPlan(t, application)

	state := preparePersistentRender(context.Background(), application, plan, provider, ApplicationRenderOptions{
		persistent: persistentRenderOptions{cache: handle},
	})
	if state.enabled {
		t.Fatalf("preparePersistentRender() enabled plugin source persistence")
	}
	events := recorder.Events()
	if len(events) != 1 || events[0].Reason != renderCacheReasonIneligibleSource {
		t.Fatalf("events = %#v, want one ineligible-source skipped event", events)
	}
}

func TestCollectSourceIdentitiesAcquireErrorIsIneligibleNotFatal(t *testing.T) {
	// Identity resolution must never fail the render: the real error surfaces
	// from the render itself.
	provider := localProvider{
		repoRoot:       t.TempDir(),
		sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{}),
		gitAcquirer:    &recordingGitAcquirer{err: fmt.Errorf("network down")},
		rootIdentity:   SourceIdentity{Kind: sourceIdentityKindRoot, Revision: "1111111111111111111111111111111111111111"},
	}
	application := argoappv1.Application{Spec: argoappv1.ApplicationSpec{Source: &argoappv1.ApplicationSource{
		RepoURL: "https://git.example.test/org/repo.git", Path: "manifests/demo", TargetRevision: "main",
	}}}
	plan := mustPlan(t, application)

	_, reason, ok := collectSourceIdentities(context.Background(), nil, provider, plan)
	if ok {
		t.Fatalf("collectSourceIdentities() ok = true, want ineligible on acquire error")
	}
	if reason != "ineligible-source" {
		t.Fatalf("reason = %q, want ineligible-source", reason)
	}
}

func persistentKeyFixtureInput() persistentRenderKeyInput {
	return persistentRenderKeyInput{
		Application: applicationRenderCacheInput{
			Name:      "demo",
			Namespace: "argocd",
			Spec: argoappv1.ApplicationSpec{
				Project: "default",
				Source: &argoappv1.ApplicationSource{
					RepoURL:        "https://git.example.test/org/repo.git",
					Path:           "manifests/demo",
					TargetRevision: "main",
				},
				Destination: argoappv1.ApplicationDestination{Name: "in-cluster", Namespace: "default"},
			},
		},
		SettingsSignature: "settings-sig",
		PluginTimeout:     "1m0s",
		TrackingOptions:   normalizeTrackingOptions(TrackingOptions{}),
		Sources: []SourceIdentity{
			{Kind: sourceIdentityKindRoot, Revision: "1111111111111111111111111111111111111111"},
			{Kind: sourceIdentityKindGit, Locator: "https://git.example.test/org/values.git", Revision: "2222222222222222222222222222222222222222"},
		},
		Engine: testEngineFingerprint(),
	}
}

func TestPersistentRenderCacheKeyStableAcrossCalls(t *testing.T) {
	first, err := persistentRenderCacheKey(persistentKeyFixtureInput())
	if err != nil {
		t.Fatalf("persistentRenderCacheKey() error = %v", err)
	}
	second, err := persistentRenderCacheKey(persistentKeyFixtureInput())
	if err != nil {
		t.Fatalf("persistentRenderCacheKey() error = %v", err)
	}
	if first != second {
		t.Fatalf("key not stable: %s != %s", first, second)
	}
	if len(first) != 64 {
		t.Fatalf("key length = %d, want 64 hex chars", len(first))
	}
}

func TestPersistentRenderCacheKeyIgnoresLocalInputRevision(t *testing.T) {
	input := persistentKeyFixtureInput()
	input.Sources = []SourceIdentity{{
		Kind:     sourceIdentityKindLocalInputs,
		Revision: "1111111111111111111111111111111111111111",
		Digest:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}}
	first, err := persistentRenderCacheKey(input)
	if err != nil {
		t.Fatalf("persistentRenderCacheKey() error = %v", err)
	}
	input.Sources[0].Revision = "2222222222222222222222222222222222222222"
	second, err := persistentRenderCacheKey(input)
	if err != nil {
		t.Fatalf("persistentRenderCacheKey() error = %v", err)
	}
	if first != second {
		t.Fatalf("local-inputs revision changed persistent key: %s != %s", first, second)
	}
	input.Sources[0].Digest = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	third, err := persistentRenderCacheKey(input)
	if err != nil {
		t.Fatalf("persistentRenderCacheKey() error = %v", err)
	}
	if third == first {
		t.Fatalf("local-inputs digest mutation did not rotate persistent key")
	}

	input = persistentKeyFixtureInput()
	input.ApplicationInputs = SourceIdentity{
		Kind:     sourceIdentityKindLocalInputs,
		Revision: "1111111111111111111111111111111111111111",
		Digest:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	first, err = persistentRenderCacheKey(input)
	if err != nil {
		t.Fatalf("persistentRenderCacheKey() error = %v", err)
	}
	input.ApplicationInputs.Revision = "2222222222222222222222222222222222222222"
	second, err = persistentRenderCacheKey(input)
	if err != nil {
		t.Fatalf("persistentRenderCacheKey() error = %v", err)
	}
	if first != second {
		t.Fatalf("application input revision changed persistent key: %s != %s", first, second)
	}
	input.ApplicationInputs.Digest = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	third, err = persistentRenderCacheKey(input)
	if err != nil {
		t.Fatalf("persistentRenderCacheKey() error = %v", err)
	}
	if third == first {
		t.Fatalf("application input digest mutation did not rotate persistent key")
	}
}

func TestPersistentRenderCacheKeyIgnoresWorktreeInputRevision(t *testing.T) {
	input := persistentKeyFixtureInput()
	input.Sources = []SourceIdentity{{
		Kind:     sourceIdentityKindWorktreeInputs,
		Revision: "1111111111111111111111111111111111111111",
		Digest:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}}
	first, err := persistentRenderCacheKey(input)
	if err != nil {
		t.Fatalf("persistentRenderCacheKey() error = %v", err)
	}
	input.Sources[0].Revision = "2222222222222222222222222222222222222222"
	second, err := persistentRenderCacheKey(input)
	if err != nil {
		t.Fatalf("persistentRenderCacheKey() error = %v", err)
	}
	if first != second {
		t.Fatalf("worktree-inputs revision changed persistent key: %s != %s", first, second)
	}
	input.Sources[0].Digest = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	third, err := persistentRenderCacheKey(input)
	if err != nil {
		t.Fatalf("persistentRenderCacheKey() error = %v", err)
	}
	if third == first {
		t.Fatalf("worktree-inputs digest mutation did not rotate persistent key")
	}

	input = persistentKeyFixtureInput()
	input.ApplicationInputs = SourceIdentity{
		Kind:     sourceIdentityKindWorktreeInputs,
		Revision: "1111111111111111111111111111111111111111",
		Digest:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	first, err = persistentRenderCacheKey(input)
	if err != nil {
		t.Fatalf("persistentRenderCacheKey() error = %v", err)
	}
	input.ApplicationInputs.Revision = "2222222222222222222222222222222222222222"
	second, err = persistentRenderCacheKey(input)
	if err != nil {
		t.Fatalf("persistentRenderCacheKey() error = %v", err)
	}
	if first != second {
		t.Fatalf("worktree application input revision changed persistent key: %s != %s", first, second)
	}
	input.ApplicationInputs.Digest = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	third, err = persistentRenderCacheKey(input)
	if err != nil {
		t.Fatalf("persistentRenderCacheKey() error = %v", err)
	}
	if third == first {
		t.Fatalf("worktree application input digest mutation did not rotate persistent key")
	}
}

func TestPersistentRenderCacheKeyRotatesOnEveryInput(t *testing.T) {
	base, err := persistentRenderCacheKey(persistentKeyFixtureInput())
	if err != nil {
		t.Fatalf("persistentRenderCacheKey() error = %v", err)
	}
	mutations := []struct {
		name   string
		mutate func(*persistentRenderKeyInput)
	}{
		{"app spec", func(input *persistentRenderKeyInput) { input.Application.Spec.Project = "other" }},
		{"app name", func(input *persistentRenderKeyInput) { input.Application.Name = "other" }},
		{"application input digest", func(input *persistentRenderKeyInput) {
			input.ApplicationInputs = SourceIdentity{Kind: sourceIdentityKindLocalInputs, Digest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
		}},
		{"settings signature", func(input *persistentRenderKeyInput) { input.SettingsSignature = "other" }},
		{"plugin timeout", func(input *persistentRenderKeyInput) { input.PluginTimeout = "2m0s" }},
		{"avp compat", func(input *persistentRenderKeyInput) { input.EnableAVPCompat = true }},
		{"enable plugins", func(input *persistentRenderKeyInput) { input.EnablePlugins = true }},
		{"policy fingerprint", func(input *persistentRenderKeyInput) { input.PluginPolicyFingerprint = "policy" }},
		{"injected plugin render", func(input *persistentRenderKeyInput) { input.HasInjectedPluginRender = true }},
		{"tracking options", func(input *persistentRenderKeyInput) { input.TrackingOptions.InstanceLabelKey = "custom" }},
		{"source revision", func(input *persistentRenderKeyInput) {
			input.Sources[0].Revision = "9999999999999999999999999999999999999999"
		}},
		{"ref-only source revision", func(input *persistentRenderKeyInput) {
			input.Sources[1].Revision = "9999999999999999999999999999999999999999"
		}},
		{"source order", func(input *persistentRenderKeyInput) {
			input.Sources[0], input.Sources[1] = input.Sources[1], input.Sources[0]
		}},
		{"drydock version", func(input *persistentRenderKeyInput) { input.Engine.Version = "other" }},
		{"drydock commit", func(input *persistentRenderKeyInput) {
			input.Engine.Commit = "fedcba9876543210fedcba9876543210fedcba98"
		}},
		{"argo module", func(input *persistentRenderKeyInput) { input.Engine.ArgoCDModule = "other" }},
		{"gitops engine module", func(input *persistentRenderKeyInput) { input.Engine.GitOpsEngineModule = "other" }},
		{"helm module", func(input *persistentRenderKeyInput) { input.Engine.HelmModule = "other" }},
		{"kustomize module", func(input *persistentRenderKeyInput) { input.Engine.KustomizeModule = "other" }},
		{"jsonnet module", func(input *persistentRenderKeyInput) { input.Engine.JsonnetModule = "other" }},
		{"kubernetes module", func(input *persistentRenderKeyInput) { input.Engine.KubernetesModule = "other" }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			input := persistentKeyFixtureInput()
			mutation.mutate(&input)
			key, err := persistentRenderCacheKey(input)
			if err != nil {
				t.Fatalf("persistentRenderCacheKey() error = %v", err)
			}
			if key == base {
				t.Fatalf("mutating %s did not rotate the key", mutation.name)
			}
		})
	}
}

func TestPersistentRenderCacheFilesystemDigestMemoizesByInputs(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root+"/apps/demo/values.yaml", "value: one\n")
	handle := &persistentRenderCache{}
	paths := []filedigest.PathDigestPath{{Path: "apps/demo/values.yaml"}}

	first, err := handle.filesystemPathDigest(context.Background(), root, paths, nil)
	if err != nil {
		t.Fatalf("filesystemPathDigest() error = %v", err)
	}
	writeTestFile(t, root+"/apps/demo/values.yaml", "value: two\n")
	memoized, err := handle.filesystemPathDigest(context.Background(), root, paths, nil)
	if err != nil {
		t.Fatalf("filesystemPathDigest() memoized error = %v", err)
	}
	if memoized != first {
		t.Fatalf("filesystemPathDigest() = %#v after file change, want memoized %#v", memoized, first)
	}

	pathChanged, err := handle.filesystemPathDigest(context.Background(), root, []filedigest.PathDigestPath{
		{Path: "apps/demo/values.yaml"},
		{Path: "apps/demo/missing.yaml", Optional: true},
	}, nil)
	if err != nil {
		t.Fatalf("filesystemPathDigest() with changed paths error = %v", err)
	}
	if pathChanged == first {
		t.Fatalf("filesystemPathDigest() did not recompute after path list changed")
	}

	writeTestFile(t, root+"/apps/demo/values.yaml", "value: three\n")
	forbiddenChanged, err := handle.filesystemPathDigest(context.Background(), root, paths, []string{t.TempDir()})
	if err != nil {
		t.Fatalf("filesystemPathDigest() with changed forbidden roots error = %v", err)
	}
	if forbiddenChanged == first || forbiddenChanged == pathChanged {
		t.Fatalf("filesystemPathDigest() did not recompute after forbidden roots changed: first=%#v pathChanged=%#v forbiddenChanged=%#v", first, pathChanged, forbiddenChanged)
	}
}

func TestPersistentRenderCacheFilesystemPathDigestMemoKeyNormalizesPathsAndForbiddenRoots(t *testing.T) {
	root := t.TempDir()
	forbidden := t.TempDir()
	firstKey, firstPaths, firstForbidden, err := filesystemPathDigestMemoKey(root, []filedigest.PathDigestPath{
		{Path: "apps/demo/missing.yaml", Optional: true},
		{Path: "apps/demo/../demo/values.yaml", MarkerKind: "marker"},
		{Path: "apps/demo/values.yaml", MarkerKind: "marker", Optional: true},
	}, []string{forbidden})
	if err != nil {
		t.Fatalf("filesystemPathDigestMemoKey() error = %v", err)
	}
	secondKey, secondPaths, secondForbidden, err := filesystemPathDigestMemoKey(root, []filedigest.PathDigestPath{
		{Path: "apps/demo/values.yaml", MarkerKind: "marker", Optional: true},
		{Path: "apps/demo/missing.yaml", Optional: true},
		{Path: "apps/demo/values.yaml", MarkerKind: "marker"},
	}, []string{forbidden})
	if err != nil {
		t.Fatalf("filesystemPathDigestMemoKey() second error = %v", err)
	}
	if firstKey != secondKey {
		t.Fatalf("memo key did not normalize equivalent paths:\nfirst=%q\nsecond=%q", firstKey, secondKey)
	}
	if !reflect.DeepEqual(firstPaths, secondPaths) {
		t.Fatalf("normalized paths differ: %#v != %#v", firstPaths, secondPaths)
	}
	if !reflect.DeepEqual(firstForbidden, secondForbidden) {
		t.Fatalf("normalized forbidden roots differ: %#v != %#v", firstForbidden, secondForbidden)
	}

	markerChangedKey, _, _, err := filesystemPathDigestMemoKey(root, []filedigest.PathDigestPath{
		{Path: "apps/demo/values.yaml", MarkerKind: "other-marker"},
		{Path: "apps/demo/missing.yaml", Optional: true},
	}, []string{forbidden})
	if err != nil {
		t.Fatalf("filesystemPathDigestMemoKey() marker change error = %v", err)
	}
	if markerChangedKey == firstKey {
		t.Fatalf("memo key did not include marker kind")
	}

	forbiddenChangedKey, _, _, err := filesystemPathDigestMemoKey(root, []filedigest.PathDigestPath{
		{Path: "apps/demo/values.yaml", MarkerKind: "marker"},
		{Path: "apps/demo/missing.yaml", Optional: true},
	}, []string{t.TempDir()})
	if err != nil {
		t.Fatalf("filesystemPathDigestMemoKey() forbidden change error = %v", err)
	}
	if forbiddenChangedKey == firstKey {
		t.Fatalf("memo key did not include forbidden roots")
	}
}

func TestPersistentRenderCacheFilesystemPathDigestMemoKeyDoesNotCollideOnDelimiters(t *testing.T) {
	root := t.TempDir()
	firstKey, firstPaths, _, err := filesystemPathDigestMemoKey(root, []filedigest.PathDigestPath{
		{Path: "aa", MarkerKind: "b:optional:c"},
	}, nil)
	if err != nil {
		t.Fatalf("filesystemPathDigestMemoKey() error = %v", err)
	}
	secondKey, secondPaths, _, err := filesystemPathDigestMemoKey(root, []filedigest.PathDigestPath{
		{Path: "aa:required:b", Optional: true, MarkerKind: "c"},
	}, nil)
	if err != nil {
		t.Fatalf("filesystemPathDigestMemoKey() second error = %v", err)
	}
	if reflect.DeepEqual(firstPaths, secondPaths) {
		t.Fatalf("test inputs unexpectedly normalized to same paths: %#v", firstPaths)
	}
	if firstKey == secondKey {
		t.Fatalf("memo keys collided for distinct path/optional/marker inputs:\nfirst=%#v\nsecond=%#v\nkey=%q", firstPaths, secondPaths, firstKey)
	}
}

func TestPersistentRenderCacheCollectApplicationInputIdentityRootModes(t *testing.T) {
	repoRoot := t.TempDir()
	writeTestFile(t, repoRoot+"/apps/demo.yaml", "kind: Application\n")
	revision := gitCommitAll(t, repoRoot, "initial")
	handle := &persistentRenderCache{
		digests:           map[string]gitref.PathDigestResult{},
		filesystemDigests: map[string]filedigest.PathDigestResult{},
	}

	cleanIdentity, reason, ok := collectApplicationInputIdentity(context.Background(), handle, localProvider{
		repoRoot:      repoRoot,
		rootIdentity:  SourceIdentity{Kind: sourceIdentityKindRoot, Revision: revision},
		rootInputMode: rootInputModeClean,
	}, []string{"apps/demo.yaml"}, true, false)
	if !ok {
		t.Fatalf("clean collectApplicationInputIdentity() ok = false, reason = %s", reason)
	}
	if cleanIdentity.Kind != sourceIdentityKindLocalInputs || cleanIdentity.Digest == "" {
		t.Fatalf("clean identity = %#v, want local-inputs digest", cleanIdentity)
	}

	snapshotIdentity, reason, ok := collectApplicationInputIdentity(context.Background(), handle, localProvider{
		repoRoot:      repoRoot,
		rootIdentity:  SourceIdentity{Kind: sourceIdentityKindRoot, Revision: revision},
		rootInputMode: rootInputModeSnapshot,
	}, []string{"apps/demo.yaml"}, true, false)
	if !ok || reason != "" || snapshotIdentity != (SourceIdentity{}) {
		t.Fatalf("snapshot collectApplicationInputIdentity() = %#v/%q/%t, want empty ok", snapshotIdentity, reason, ok)
	}

	dirtyIdentity, reason, ok := collectApplicationInputIdentity(context.Background(), handle, localProvider{
		repoRoot:      repoRoot,
		rootIdentity:  SourceIdentity{Kind: sourceIdentityKindRoot, Revision: revision},
		rootInputMode: rootInputModeDirty,
	}, []string{"apps/demo.yaml"}, true, false)
	if !ok {
		t.Fatalf("dirty collectApplicationInputIdentity() ok = false, reason = %s", reason)
	}
	if dirtyIdentity.Kind != sourceIdentityKindWorktreeInputs || dirtyIdentity.Digest == "" {
		t.Fatalf("dirty identity = %#v, want worktree-inputs digest", dirtyIdentity)
	}

	_, reason, ok = collectApplicationInputIdentity(context.Background(), handle, localProvider{
		repoRoot:      repoRoot,
		rootIdentity:  SourceIdentity{Kind: sourceIdentityKindRoot},
		rootInputMode: rootInputModeUnknown,
	}, []string{"apps/demo.yaml"}, true, false)
	if ok || reason != renderCacheReasonIneligibleSource {
		t.Fatalf("unknown collectApplicationInputIdentity() ok=%t reason=%q, want ineligible skip", ok, reason)
	}
}

func TestCollectApplicationInputIdentityDuplicateApplicationReason(t *testing.T) {
	repoRoot := t.TempDir()
	writeTestFile(t, repoRoot+"/apps/alpha/cm.yaml", "a: 1\n")
	revision := gitCommitAll(t, repoRoot, "initial")
	handle := &persistentRenderCache{
		digests:           map[string]gitref.PathDigestResult{},
		filesystemDigests: map[string]filedigest.PathDigestResult{},
	}
	provider := localProvider{
		repoRoot:       repoRoot,
		sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{}),
		rootIdentity:   SourceIdentity{Kind: sourceIdentityKindRoot, Revision: revision},
		rootInputMode:  rootInputModeClean,
	}
	_, reason, ok := collectApplicationInputIdentity(context.Background(), handle, provider, nil, true, true)
	if ok || reason != renderCacheReasonDuplicateApplication {
		t.Fatalf("ok/reason = %v/%s, want duplicate-application skip", ok, reason)
	}
}

func mustCollectApplicationInputIdentity(t *testing.T, provider localProvider, inputPaths []string) SourceIdentity {
	t.Helper()
	handle := &persistentRenderCache{
		digests:           map[string]gitref.PathDigestResult{},
		filesystemDigests: map[string]filedigest.PathDigestResult{},
	}
	identity, reason, ok := collectApplicationInputIdentity(context.Background(), handle, provider, inputPaths, true, false)
	if !ok {
		t.Fatalf("collectApplicationInputIdentity() ok = false, reason = %s", reason)
	}
	return identity
}

func TestPersistentRenderCacheCollectApplicationInputIdentityDirtyDigestTracksAppInputs(t *testing.T) {
	repoRoot := t.TempDir()
	writeTestFile(t, repoRoot+"/apps/demo.yaml", "kind: Application\n# comment one\n")
	rootRevision := gitCommitAll(t, repoRoot, "initial")
	provider := localProvider{
		repoRoot:      repoRoot,
		rootIdentity:  SourceIdentity{Kind: sourceIdentityKindRoot, Revision: rootRevision},
		rootInputMode: rootInputModeDirty,
	}

	first := mustCollectApplicationInputIdentity(t, provider, []string{"apps/demo.yaml"})
	if first.Kind != sourceIdentityKindWorktreeInputs || first.Digest == "" {
		t.Fatalf("identity = %#v, want worktree-inputs digest", first)
	}

	writeTestFile(t, repoRoot+"/README.md", "unrelated dirty file\n")
	unrelated := mustCollectApplicationInputIdentity(t, provider, []string{"apps/demo.yaml"})
	if unrelated != first {
		t.Fatalf("identity changed for dirty file outside app inputs:\nfirst=%#v\nunrelated=%#v", first, unrelated)
	}

	writeTestFile(t, repoRoot+"/apps/demo.yaml", "kind: Application\n# comment two\n")
	changed := mustCollectApplicationInputIdentity(t, provider, []string{"apps/demo.yaml"})
	if changed.Kind != sourceIdentityKindWorktreeInputs || changed.Digest == "" {
		t.Fatalf("changed identity = %#v, want worktree-inputs digest", changed)
	}
	if changed == first {
		t.Fatalf("identity did not change after dirty application input mutation: %#v", changed)
	}
}

func TestPersistentRenderCacheCollectApplicationInputIdentityDirtyMissingRequiredInputSkips(t *testing.T) {
	repoRoot := t.TempDir()
	writeTestFile(t, repoRoot+"/apps/demo.yaml", "kind: Application\n")
	rootRevision := gitCommitAll(t, repoRoot, "initial")
	handle := &persistentRenderCache{filesystemDigests: map[string]filedigest.PathDigestResult{}}
	_, reason, ok := collectApplicationInputIdentity(context.Background(), handle, localProvider{
		repoRoot:      repoRoot,
		rootIdentity:  SourceIdentity{Kind: sourceIdentityKindRoot, Revision: rootRevision},
		rootInputMode: rootInputModeDirty,
	}, []string{"apps/missing.yaml"}, true, false)
	if ok {
		t.Fatalf("collectApplicationInputIdentity() ok = true, want missing required app input to skip persistence")
	}
	if reason != renderCacheReasonInputDigest {
		t.Fatalf("reason = %q, want input digest", reason)
	}
}

func TestPersistentRenderCacheCollectApplicationInputIdentityEmptyKnownInputSkips(t *testing.T) {
	repoRoot := t.TempDir()
	writeTestFile(t, repoRoot+"/apps/demo.yaml", "kind: Application\n")
	rootRevision := gitCommitAll(t, repoRoot, "initial")
	handle := &persistentRenderCache{filesystemDigests: map[string]filedigest.PathDigestResult{}}
	_, reason, ok := collectApplicationInputIdentity(context.Background(), handle, localProvider{
		repoRoot:      repoRoot,
		rootIdentity:  SourceIdentity{Kind: sourceIdentityKindRoot, Revision: rootRevision},
		rootInputMode: rootInputModeDirty,
	}, nil, true, false)
	if ok {
		t.Fatalf("collectApplicationInputIdentity() ok = true, want empty known app inputs to skip persistence")
	}
	if reason != renderCacheReasonInputGraph {
		t.Fatalf("reason = %q, want input graph", reason)
	}
}

func TestPersistentRenderCacheCollectApplicationInputIdentityDirtySymlinkSkips(t *testing.T) {
	repoRoot := t.TempDir()
	writeTestFile(t, repoRoot+"/apps/target.yaml", "kind: Application\n")
	rootRevision := gitCommitAll(t, repoRoot, "initial")
	if err := os.Symlink("target.yaml", repoRoot+"/apps/link.yaml"); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	handle := &persistentRenderCache{filesystemDigests: map[string]filedigest.PathDigestResult{}}
	_, reason, ok := collectApplicationInputIdentity(context.Background(), handle, localProvider{
		repoRoot:      repoRoot,
		rootIdentity:  SourceIdentity{Kind: sourceIdentityKindRoot, Revision: rootRevision},
		rootInputMode: rootInputModeDirty,
	}, []string{"apps/link.yaml"}, true, false)
	if ok {
		t.Fatalf("collectApplicationInputIdentity() ok = true, want symlink app input to skip persistence")
	}
	if reason != renderCacheReasonInputDigest && reason != renderCacheReasonInputGraph {
		t.Fatalf("reason = %q, want input digest or graph", reason)
	}
}

func TestPersistentRenderCacheCollectSourceIdentitiesLocalInputDigestStableAcrossHeadChange(t *testing.T) {
	repoRoot := t.TempDir()
	writeTestFile(t, repoRoot+"/charts/demo/Chart.yaml", "apiVersion: v2\nname: demo\nversion: 0.1.0\n")
	writeTestFile(t, repoRoot+"/charts/demo/templates/cm.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\n")
	firstRevision := gitCommitAll(t, repoRoot, "initial")
	handle := &persistentRenderCache{digests: map[string]gitref.PathDigestResult{}}
	provider := localProvider{
		repoRoot:       repoRoot,
		sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{}),
		rootIdentity:   SourceIdentity{Kind: sourceIdentityKindRoot, Revision: firstRevision},
		rootInputMode:  rootInputModeClean,
	}
	application := argoappv1.Application{Spec: argoappv1.ApplicationSpec{Source: &argoappv1.ApplicationSource{
		RepoURL: "https://git.example.test/org/repo.git", Path: "charts/demo", TargetRevision: "main",
	}}}
	plan := mustPlan(t, application)

	first, reason, ok := collectSourceIdentities(context.Background(), handle, provider, plan)
	if !ok {
		t.Fatalf("collectSourceIdentities() ok = false, reason = %s", reason)
	}
	writeTestFile(t, repoRoot+"/README.md", "unrelated\n")
	secondRevision := gitCommitAll(t, repoRoot, "unrelated")
	provider.rootIdentity.Revision = secondRevision
	second, reason, ok := collectSourceIdentities(context.Background(), handle, provider, plan)
	if !ok {
		t.Fatalf("collectSourceIdentities() after unrelated commit ok = false, reason = %s", reason)
	}
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("identity counts = %d/%d, want 1/1", len(first), len(second))
	}
	if first[0].Kind != sourceIdentityKindLocalInputs {
		t.Fatalf("identity kind = %q, want %q", first[0].Kind, sourceIdentityKindLocalInputs)
	}
	if first[0].Revision != "" || second[0].Revision != "" {
		t.Fatalf("local input identities carried revisions: %#v %#v", first[0], second[0])
	}
	if first[0].Digest == "" {
		t.Fatalf("local input digest is empty: %#v", first[0])
	}
	if first[0] != second[0] {
		t.Fatalf("local input identity changed across unrelated HEAD commit:\nfirst=%#v\nsecond=%#v", first[0], second[0])
	}
}

func TestAcquisitionsPinStable(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	cases := []struct {
		name    string
		records []cacheevent.AcquisitionRecord
		want    bool
	}{
		{name: "no records", records: nil, want: true},
		{name: "git always passes even floating", records: []cacheevent.AcquisitionRecord{
			{Kind: cacheevent.AcquisitionGit, RequestedRevision: "main", ResolvedRevision: sha},
		}, want: true},
		{name: "exact chart version passes", records: []cacheevent.AcquisitionRecord{
			{Kind: cacheevent.AcquisitionChart, RequestedRevision: "1.2.3", ResolvedRevision: "1.2.3"},
		}, want: true},
		{name: "empty chart version blocks", records: []cacheevent.AcquisitionRecord{
			{Kind: cacheevent.AcquisitionChart, RequestedRevision: " ", ResolvedRevision: "1.2.3"},
		}, want: false},
		{name: "sha-pinned remote git passes", records: []cacheevent.AcquisitionRecord{
			{Kind: cacheevent.AcquisitionRemoteGit, RequestedRevision: sha, ResolvedRevision: sha},
		}, want: true},
		{name: "floating remote git blocks", records: []cacheevent.AcquisitionRecord{
			{Kind: cacheevent.AcquisitionRemoteGit, RequestedRevision: "main", ResolvedRevision: sha},
		}, want: false},
		{name: "short-sha remote git blocks", records: []cacheevent.AcquisitionRecord{
			{Kind: cacheevent.AcquisitionRemoteGit, RequestedRevision: sha[:12], ResolvedRevision: sha},
		}, want: false},
		{name: "remote http always blocks", records: []cacheevent.AcquisitionRecord{
			{Kind: cacheevent.AcquisitionRemoteHTTP, RequestedRevision: "", ResolvedRevision: "etag"},
		}, want: false},
		{name: "unknown kind blocks", records: []cacheevent.AcquisitionRecord{
			{Kind: cacheevent.AcquisitionKind("future"), RequestedRevision: sha, ResolvedRevision: sha},
		}, want: false},
		{name: "one unstable blocks the set", records: []cacheevent.AcquisitionRecord{
			{Kind: cacheevent.AcquisitionGit, RequestedRevision: "main", ResolvedRevision: sha},
			{Kind: cacheevent.AcquisitionRemoteGit, RequestedRevision: "main", ResolvedRevision: sha},
		}, want: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := acquisitionsPinStable(testCase.records); got != testCase.want {
				t.Fatalf("acquisitionsPinStable() = %t, want %t", got, testCase.want)
			}
		})
	}
}

func TestIsFullCommitSHA(t *testing.T) {
	cases := []struct {
		revision string
		want     bool
	}{
		{"0123456789abcdef0123456789abcdef01234567", true},
		{"0123456789ABCDEF0123456789ABCDEF01234567", false},
		{"main", false},
		{"", false},
		{"0123456789abcdef0123456789abcdef0123456", false},
		{"0123456789abcdef0123456789abcdef012345678", false},
		{"012345678zabcdef0123456789abcdef01234567", false},
	}
	for _, testCase := range cases {
		if got := isFullCommitSHA(testCase.revision); got != testCase.want {
			t.Fatalf("isFullCommitSHA(%q) = %t, want %t", testCase.revision, got, testCase.want)
		}
	}
}

func TestPersistentLookupSkippedWhenSourceOverrideIntroducesPlugin(t *testing.T) {
	repoRoot := t.TempDir()
	writeTestFile(t, repoRoot+"/manifests/demo/cm.yaml", `apiVersion: v1
kind: ConfigMap
metadata:
  name: live
`)
	writeTestFile(t, repoRoot+"/manifests/demo/.argocd-source.yaml", `plugin:
  name: cue
`)
	rootRevision := "1111111111111111111111111111111111111111"
	cacheOptions := testRenderCacheOptions(t)
	handle := newPersistentRenderCache(cacheOptions, true)
	if !handle.active() {
		t.Fatalf("persistent cache did not open")
	}
	provider := localProvider{
		repoRoot:      repoRoot,
		cacheEvents:   cacheevent.NewRecorder(true),
		acquisitions:  cacheevent.NewAcquisitionCollector(),
		rootIdentity:  SourceIdentity{Kind: sourceIdentityKindRoot, Revision: rootRevision},
		rootInputMode: rootInputModeSnapshot,
	}
	application := argoappv1.Application{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "argocd"},
		Spec: argoappv1.ApplicationSpec{
			Source: &argoappv1.ApplicationSource{
				RepoURL:        "https://git.example.test/org/repo.git",
				Path:           "manifests/demo",
				TargetRevision: "main",
			},
			Destination: argoappv1.ApplicationDestination{Namespace: "default"},
		},
	}
	key, err := persistentRenderCacheKey(persistentRenderKeyInput{
		Application:       newApplicationRenderCacheInput(application),
		SettingsSignature: "settings",
		PluginTimeout:     "0s",
		TrackingOptions:   normalizeTrackingOptions(TrackingOptions{}),
		Sources:           []SourceIdentity{{Kind: sourceIdentityKindRoot, Revision: rootRevision}},
		Engine:            cacheOptions.EngineFingerprint,
	})
	if err != nil {
		t.Fatalf("persistentRenderCacheKey() error = %v", err)
	}
	payload, err := marshalRenderResultPayload(RenderResult{
		Manifests: []render.Manifest{{Object: cm("cached", "value")}},
	})
	if err != nil {
		t.Fatalf("marshalRenderResultPayload() error = %v", err)
	}
	if err := handle.store.Put(key, payload, rendercache.EntryMeta{}); err != nil {
		t.Fatalf("Put() error = %v", err)
	}

	result, err := RenderApplicationWithOptions(context.Background(), application, provider, ApplicationRenderOptions{
		TrackingOptions: TrackingOptions{},
		persistent: persistentRenderOptions{
			cache:             handle,
			settingsSignature: "settings",
		},
	})
	if err == nil {
		t.Fatalf("RenderApplicationWithOptions() error = nil, want plugin override to bypass cached result; manifests = %#v", result.Manifests)
	}
	for _, rendered := range result.Manifests {
		if rendered.Object != nil && rendered.Object.GetName() == "cached" {
			t.Fatalf("returned stale persistent cached manifest despite plugin override: %#v", result.Manifests)
		}
	}
}

func TestValidateRenderCacheRootRejectsUnopenableStore(t *testing.T) {
	// A regular file where the cache dir should be makes MkdirAll fail
	// deterministically on every platform.
	blocker := t.TempDir() + "/not-a-dir"
	writeTestFile(t, blocker, "blocker")

	options := RenderCacheOptions{
		RenderCacheEnabled: true,
		RenderCacheDir:     blocker,
		EngineFingerprint:  testEngineFingerprint(),
	}

	buildErr := validateBuildRenderCacheRoot(BuildRequest{Path: t.TempDir(), RenderCacheOptions: options})
	if buildErr == nil {
		t.Fatalf("validateBuildRenderCacheRoot() = nil, want store open failure")
	}

	diffRequest := DiffRequest{LeftPath: t.TempDir(), RightPath: t.TempDir()}
	diffRequest.RenderCacheOptions = options
	if err := validateDiffRenderCacheRoot(diffRequest); err == nil {
		t.Fatalf("validateDiffRenderCacheRoot() = nil, want store open failure")
	}
}

func TestEnsurePersistentRenderCacheAcquisitionRefreshFlagsBypassLookup(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*BuildRequest)
		refresh bool
	}{
		{"refresh-charts", func(r *BuildRequest) { r.RefreshCharts = true }, true},
		{"refresh-git", func(r *BuildRequest) { r.RefreshGit = true }, true},
		{"refresh-remote-resources", func(r *BuildRequest) { r.RefreshRemoteResources = true }, true},
		{"offline-does-not-refresh", func(r *BuildRequest) { r.Offline = true }, false},
		{"no-flags", func(r *BuildRequest) {}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := BuildRequest{RenderCacheOptions: testRenderCacheOptions(t)}
			tc.mutate(&request)
			prepared, _ := ensurePersistentRenderCache(request)
			if prepared.persistentRenderCache.refresh != tc.refresh {
				t.Fatalf("handle.refresh = %v, want %v", prepared.persistentRenderCache.refresh, tc.refresh)
			}
		})
	}
}

func TestEnsureDiffPersistentRenderCacheAcquisitionRefreshFlagsBypassLookup(t *testing.T) {
	request := DiffRequest{}
	request.RenderCacheOptions = testRenderCacheOptions(t)
	request.RefreshCharts = true
	prepared, _ := ensureDiffPersistentRenderCache(request)
	if !prepared.persistentRenderCache.refresh {
		t.Fatalf("handle.refresh = false, want true with --refresh-charts")
	}
}

func TestLocalInputDigestPathsIncludeArgocdSourceOverrides(t *testing.T) {
	repoRoot := t.TempDir()
	writeTestFile(t, repoRoot+"/manifests/demo/kustomization.yaml", "resources:\n  - cm.yaml\n")
	writeTestFile(t, repoRoot+"/manifests/demo/cm.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\n")
	application := argoappv1.Application{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-app"},
		Spec: argoappv1.ApplicationSpec{Source: &argoappv1.ApplicationSource{
			RepoURL: "https://git.example.test/org/repo.git", Path: "manifests/demo", TargetRevision: "main",
		}},
	}
	plan := mustPlan(t, application)

	paths, _, err := localInputDigestPathsForSource(context.Background(), plan, plan.Sources[0], repoRoot)
	if err != nil {
		t.Fatalf("localInputDigestPathsForSource() error = %v", err)
	}
	want := map[string]bool{
		"manifests/demo/.argocd-source.yaml":          true,
		"manifests/demo/.argocd-source-demo-app.yaml": true,
	}
	for _, item := range paths {
		if _, ok := want[item.Path]; ok {
			if !item.Optional {
				t.Fatalf("override path %q must be optional", item.Path)
			}
			delete(want, item.Path)
		}
	}
	if len(want) != 0 {
		t.Fatalf("digest paths missing override files %v; got %#v", want, paths)
	}
}

func TestPersistentRenderCacheDirtyKustomizeDigestTracksSourceOverrides(t *testing.T) {
	repoRoot := t.TempDir()
	writeTestFile(t, repoRoot+"/manifests/demo/kustomization.yaml", "resources:\n  - cm.yaml\n")
	writeTestFile(t, repoRoot+"/manifests/demo/cm.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\n")
	rootRevision := gitCommitAll(t, repoRoot, "initial")
	provider := localProvider{
		repoRoot:       repoRoot,
		sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{}),
		rootIdentity:   SourceIdentity{Kind: sourceIdentityKindRoot, Revision: rootRevision},
		rootInputMode:  rootInputModeDirty,
	}
	application := argoappv1.Application{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-app"},
		Spec: argoappv1.ApplicationSpec{Source: &argoappv1.ApplicationSource{
			RepoURL: "https://git.example.test/org/repo.git", Path: "manifests/demo", TargetRevision: "main",
		}},
	}
	plan := mustPlan(t, application)

	before := mustCollectSingleSourceIdentity(t, provider, plan)
	writeTestFile(t, repoRoot+"/manifests/demo/.argocd-source.yaml", "kustomize:\n  namePrefix: prod-\n")
	after := mustCollectSingleSourceIdentity(t, provider, plan)
	if after == before {
		t.Fatalf("identity did not change after adding .argocd-source.yaml override: %#v", after)
	}
}

func TestLocalToolInputDigestPathsMatchSelectLocalRendererPrecedence(t *testing.T) {
	repoRoot := t.TempDir()
	// Both Chart.yaml and kustomization.yaml: the renderer picks Kustomize,
	// so the digest must track the kustomization graph (including ../base).
	writeTestFile(t, repoRoot+"/base/cm.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: base\n")
	writeTestFile(t, repoRoot+"/base/kustomization.yaml", "resources:\n  - cm.yaml\n")
	writeTestFile(t, repoRoot+"/manifests/demo/Chart.yaml", "apiVersion: v2\nname: demo\nversion: 0.1.0\n")
	writeTestFile(t, repoRoot+"/manifests/demo/kustomization.yaml", "resources:\n  - ../../base\n")
	application := argoappv1.Application{Spec: argoappv1.ApplicationSpec{Source: &argoappv1.ApplicationSource{
		RepoURL: "https://git.example.test/org/repo.git", Path: "manifests/demo", TargetRevision: "main",
	}}}
	plan := mustPlan(t, application)

	paths, _, err := localInputDigestPathsForSource(context.Background(), plan, plan.Sources[0], repoRoot)
	if err != nil {
		t.Fatalf("localInputDigestPathsForSource() error = %v", err)
	}
	foundBase := false
	for _, item := range paths {
		if item.Path == "base" || strings.HasPrefix(item.Path, "base/") {
			foundBase = true
		}
	}
	if !foundBase {
		t.Fatalf("digest paths %#v do not track the kustomize base; helm classification won over the renderer's kustomize choice", paths)
	}
}

func TestCollectSourceIdentitiesDirtyModePopulatesInputVerifier(t *testing.T) {
	repoRoot := t.TempDir()
	writeTestFile(t, repoRoot+"/manifests/demo/cm.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\n")
	rootRevision := gitCommitAll(t, repoRoot, "initial")
	handle := &persistentRenderCache{filesystemDigests: map[string]filedigest.PathDigestResult{}}
	verifier := &renderInputVerifier{}
	provider := localProvider{
		repoRoot:       repoRoot,
		sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{}),
		rootIdentity:   SourceIdentity{Kind: sourceIdentityKindRoot, Revision: rootRevision},
		rootInputMode:  rootInputModeDirty,
		inputVerifier:  verifier,
	}
	application := argoappv1.Application{Spec: argoappv1.ApplicationSpec{Source: &argoappv1.ApplicationSource{
		RepoURL: "https://git.example.test/org/repo.git", Path: "manifests/demo", TargetRevision: "main",
	}}}
	plan := mustPlan(t, application)

	identities, reason, ok := collectSourceIdentities(context.Background(), handle, provider, plan)
	if !ok {
		t.Fatalf("collectSourceIdentities() ok = false, reason = %s", reason)
	}
	if len(verifier.entries) != 1 {
		t.Fatalf("verifier entries = %#v, want one entry per digested source", verifier.entries)
	}
	if verifier.entries[0].digest != identities[0].Digest {
		t.Fatalf("verifier baseline %q != source identity digest %q", verifier.entries[0].digest, identities[0].Digest)
	}
}

func TestCollectSourceIdentitiesCleanModePopulatesCommittedVerification(t *testing.T) {
	repoRoot := t.TempDir()
	writeTestFile(t, repoRoot+"/manifests/demo/cm.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\n")
	rootRevision := gitCommitAll(t, repoRoot, "initial")
	handle := &persistentRenderCache{
		digests:           map[string]gitref.PathDigestResult{},
		filesystemDigests: map[string]filedigest.PathDigestResult{},
	}
	verifier := &renderInputVerifier{}
	provider := localProvider{
		repoRoot:       repoRoot,
		sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{}),
		rootIdentity:   SourceIdentity{Kind: sourceIdentityKindRoot, Revision: rootRevision},
		rootInputMode:  rootInputModeClean,
		inputVerifier:  verifier,
	}
	application := argoappv1.Application{Spec: argoappv1.ApplicationSpec{Source: &argoappv1.ApplicationSource{
		RepoURL: "https://git.example.test/org/repo.git", Path: "manifests/demo", TargetRevision: "main",
	}}}
	plan := mustPlan(t, application)

	_, reason, ok := collectSourceIdentities(context.Background(), handle, provider, plan)
	if !ok {
		t.Fatalf("collectSourceIdentities() ok = false, reason = %s", reason)
	}
	if len(verifier.entries) != 1 {
		t.Fatalf("verifier entries = %#v, want one committed verification", verifier.entries)
	}
	entry := verifier.entries[0]
	if entry.revision != rootRevision || len(entry.committedPaths) == 0 || entry.digest != "" || len(entry.paths) != 0 {
		t.Fatalf("entry = %#v, want committed verification (revision + paths, no filesystem baseline)", entry)
	}
}

func TestCommittedPathDigestMemoKeyDedupesEquivalentPaths(t *testing.T) {
	_, normalized, err := committedPathDigestMemoKey(t.TempDir(), "HEAD", []gitref.PathDigestPath{
		{Path: `a\b`, Optional: true},
		{Path: "a/./b", Optional: false},
	})
	if err != nil {
		t.Fatalf("committedPathDigestMemoKey() error = %v", err)
	}
	if len(normalized) != 1 || normalized[0].Path != "a/b" || normalized[0].Optional {
		t.Fatalf("normalized = %#v, want single required a/b entry", normalized)
	}
}

func TestHandleFilesystemPathDigestSharesPerFileCache(t *testing.T) {
	repoRoot := t.TempDir()
	writeTestFile(t, repoRoot+"/manifests/demo/cm.yaml", "a: 1\n")
	handle := newPersistentRenderCache(testRenderCacheOptions(t), false)

	pathsA := []filedigest.PathDigestPath{{Path: "manifests/demo"}}
	if _, err := handle.filesystemPathDigest(context.Background(), repoRoot, pathsA, nil); err != nil {
		t.Fatalf("warm digest error = %v", err)
	}

	// Edit the file, then digest a DIFFERENT path set (different memo key)
	// covering the same file: with a shared per-file cache the stale content
	// hash is reused, so the result matches a pre-edit computation.
	writeTestFile(t, repoRoot+"/manifests/demo/cm.yaml", "a: 2\n")
	pathsB := []filedigest.PathDigestPath{{Path: "manifests/demo"}, {Path: "manifests/missing.yaml", Optional: true}}
	viaHandle, err := handle.filesystemPathDigest(context.Background(), repoRoot, pathsB, nil)
	if err != nil {
		t.Fatalf("handle digest error = %v", err)
	}
	fresh, err := filedigest.PathDigest(context.Background(), filedigest.PathDigestInput{RepoRoot: repoRoot, Paths: pathsB})
	if err != nil {
		t.Fatalf("direct digest error = %v", err)
	}
	if viaHandle.Digest == fresh.Digest {
		t.Fatalf("handle digest saw the edit; the per-file cache is not wired into filesystemPathDigest")
	}
}

func TestKustomizeDigestPathsIncludeFilenameVariants(t *testing.T) {
	repoRoot := t.TempDir()
	writeTestFile(t, repoRoot+"/apps/alpha/kustomization.yaml", "resources:\n  - cm.yaml\n")
	writeTestFile(t, repoRoot+"/apps/alpha/cm.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: alpha\n")
	application := argoappv1.Application{Spec: argoappv1.ApplicationSpec{Source: &argoappv1.ApplicationSource{
		RepoURL: "https://git.example.test/org/repo.git", Path: "apps/alpha", TargetRevision: "main",
	}}}
	plan := mustPlan(t, application)
	paths, _, err := localInputDigestPathsForSource(context.Background(), plan, plan.Sources[0], repoRoot)
	if err != nil {
		t.Fatalf("localInputDigestPathsForSource() error = %v", err)
	}
	want := map[string]bool{
		"apps/alpha/kustomization.yml": false,
		"apps/alpha/Kustomization":     false,
	}
	for _, item := range paths {
		if _, ok := want[item.Path]; ok && item.Optional {
			want[item.Path] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Fatalf("digest paths missing optional kustomization variant %q: %#v", name, paths)
		}
	}
	// required-wins dedup pin: the committed kustomization.yaml must appear as
	// required (Optional=false) even though the optional-variant loop also emits
	// it. The normalizer deduplicates optional-AND-required to required.
	foundRequired := false
	for _, item := range paths {
		if item.Path == "apps/alpha/kustomization.yaml" && !item.Optional {
			foundRequired = true
			break
		}
	}
	if !foundRequired {
		t.Fatalf("apps/alpha/kustomization.yaml must appear as required (Optional=false) — required-wins dedup broken: %#v", paths)
	}
}

func TestDirtyPathsTouch(t *testing.T) {
	dirty := []string{"apps/alpha/cm.yaml", "apps/beta/.git", "newdir/x.yaml"}
	cases := []struct {
		name  string
		paths []gitref.PathDigestPath
		want  bool
	}{
		{"exact file", []gitref.PathDigestPath{{Path: "apps/alpha/cm.yaml"}}, true},
		{"dir covering dirty file", []gitref.PathDigestPath{{Path: "apps/alpha"}}, true},
		{"untouched dir", []gitref.PathDigestPath{{Path: "apps/gamma"}}, false},
		{"sibling prefix is not a dir match", []gitref.PathDigestPath{{Path: "apps/alph"}}, false},
		{"optional path appearing dirty", []gitref.PathDigestPath{{Path: "newdir/x.yaml", Optional: true}}, true},
		{"dot covers everything", []gitref.PathDigestPath{{Path: "."}}, true},
		{"backslash path canonicalized", []gitref.PathDigestPath{{Path: `apps\alpha`}}, true},
		{"invalid path fails closed", []gitref.PathDigestPath{{Path: "../escape"}}, true},
		{"no paths", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := dirtyPathsTouch(dirty, tc.paths); got != tc.want {
				t.Fatalf("dirtyPathsTouch(%v) = %v, want %v", tc.paths, got, tc.want)
			}
		})
	}
}

func TestRootIdentityForRequestReturnsDirtyPaths(t *testing.T) {
	repoRoot := t.TempDir()
	writeTestFile(t, repoRoot+"/apps/alpha/cm.yaml", "a: 1\n")
	revision := gitCommitAll(t, repoRoot, "initial")
	writeTestFile(t, repoRoot+"/apps/alpha/edit.yaml", "e: 1\n")
	request := BuildRequest{RenderCacheOptions: testRenderCacheOptions(t)}
	prepared, _ := ensurePersistentRenderCache(request)

	identity, mode, dirtyPaths := rootIdentityForRequest(context.Background(), repoRoot, prepared)
	if mode != rootInputModeDirty || identity.Revision != revision {
		t.Fatalf("identity/mode = %+v/%s, want dirty with revision", identity, mode)
	}
	if len(dirtyPaths) != 1 || dirtyPaths[0] != "apps/alpha/edit.yaml" {
		t.Fatalf("dirtyPaths = %#v, want the edit", dirtyPaths)
	}

	cleanRoot := t.TempDir()
	writeTestFile(t, cleanRoot+"/apps/alpha/cm.yaml", "a: 1\n")
	gitCommitAll(t, cleanRoot, "initial")
	_, cleanMode, cleanDirty := rootIdentityForRequest(context.Background(), cleanRoot, prepared)
	if cleanMode != rootInputModeClean || cleanDirty != nil {
		t.Fatalf("clean root = %s/%v, want clean with nil dirty paths", cleanMode, cleanDirty)
	}
}

func TestDirtyModeUntouchedSourceKeepsCommittedIdentity(t *testing.T) {
	repoRoot := t.TempDir()
	writeTestFile(t, repoRoot+"/apps/alpha/cm.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: alpha\n")
	writeTestFile(t, repoRoot+"/apps/beta/cm.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: beta\n")
	revision := gitCommitAll(t, repoRoot, "initial")
	application := func(name string) argoappv1.Application {
		return argoappv1.Application{Spec: argoappv1.ApplicationSpec{Source: &argoappv1.ApplicationSource{
			RepoURL: "https://git.example.test/org/repo.git", Path: "apps/" + name, TargetRevision: "main",
		}}}
	}
	provider := func(mode rootInputMode, dirtyPaths []string) localProvider {
		return localProvider{
			repoRoot:       repoRoot,
			sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{}),
			rootIdentity:   SourceIdentity{Kind: sourceIdentityKindRoot, Revision: revision},
			rootInputMode:  mode,
			rootDirtyPaths: dirtyPaths,
		}
	}

	cleanAlpha := mustCollectSingleSourceIdentity(t, provider(rootInputModeClean, nil), mustPlan(t, application("alpha")))
	if cleanAlpha.Kind != sourceIdentityKindLocalInputs {
		t.Fatalf("clean identity = %+v, want local-inputs", cleanAlpha)
	}

	// Dirty worktree: beta touched, alpha untouched.
	writeTestFile(t, repoRoot+"/apps/beta/cm.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: beta2\n")
	dirty := provider(rootInputModeDirty, []string{"apps/beta/cm.yaml"})

	dirtyAlpha := mustCollectSingleSourceIdentity(t, dirty, mustPlan(t, application("alpha")))
	if dirtyAlpha != cleanAlpha {
		t.Fatalf("untouched app identity changed across modes:\nclean = %+v\ndirty = %+v", cleanAlpha, dirtyAlpha)
	}

	dirtyBeta := mustCollectSingleSourceIdentity(t, dirty, mustPlan(t, application("beta")))
	if dirtyBeta.Kind != sourceIdentityKindWorktreeInputs {
		t.Fatalf("touched app identity = %+v, want worktree-inputs", dirtyBeta)
	}
}

func TestDirtyModeEmptyDirtySetDisablesCommittedShortcut(t *testing.T) {
	repoRoot := t.TempDir()
	writeTestFile(t, repoRoot+"/apps/alpha/cm.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: alpha\n")
	revision := gitCommitAll(t, repoRoot, "initial")
	// Dirty mode with NO dirty-path enumeration (hand-built provider):
	// fail-safe to filesystem keys, preserving pre-feature behavior.
	provider := localProvider{
		repoRoot:       repoRoot,
		sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{}),
		rootIdentity:   SourceIdentity{Kind: sourceIdentityKindRoot, Revision: revision},
		rootInputMode:  rootInputModeDirty,
	}
	application := argoappv1.Application{Spec: argoappv1.ApplicationSpec{Source: &argoappv1.ApplicationSource{
		RepoURL: "https://git.example.test/org/repo.git", Path: "apps/alpha", TargetRevision: "main",
	}}}
	identity := mustCollectSingleSourceIdentity(t, provider, mustPlan(t, application))
	if identity.Kind != sourceIdentityKindWorktreeInputs {
		t.Fatalf("identity = %+v, want worktree-inputs when the dirty set is empty", identity)
	}
}

func TestDirtyModeUntrackedFileUnderAppForcesWorktreeKey(t *testing.T) {
	repoRoot := t.TempDir()
	writeTestFile(t, repoRoot+"/apps/alpha/cm.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: alpha\n")
	revision := gitCommitAll(t, repoRoot, "initial")
	writeTestFile(t, repoRoot+"/apps/alpha/untracked.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: extra\n")
	provider := localProvider{
		repoRoot:       repoRoot,
		sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{}),
		rootIdentity:   SourceIdentity{Kind: sourceIdentityKindRoot, Revision: revision},
		rootInputMode:  rootInputModeDirty,
		rootDirtyPaths: []string{"apps/alpha/untracked.yaml"},
	}
	application := argoappv1.Application{Spec: argoappv1.ApplicationSpec{Source: &argoappv1.ApplicationSource{
		RepoURL: "https://git.example.test/org/repo.git", Path: "apps/alpha", TargetRevision: "main",
	}}}
	identity := mustCollectSingleSourceIdentity(t, provider, mustPlan(t, application))
	if identity.Kind != sourceIdentityKindWorktreeInputs {
		t.Fatalf("identity = %+v, want worktree-inputs (untracked file is a render input)", identity)
	}
}

func TestDirtyModeUntouchedHelmGlobSourceStaysIneligible(t *testing.T) {
	repoRoot := t.TempDir()
	writeTestFile(t, repoRoot+"/charts/demo/Chart.yaml", "apiVersion: v2\nname: demo\nversion: 0.1.0\n")
	writeTestFile(t, repoRoot+"/charts/demo/values.yaml", "name: demo\n")
	writeTestFile(t, repoRoot+"/charts/demo/templates/cm.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\n")
	writeTestFile(t, repoRoot+"/unrelated.txt", "dirt\n")
	revision := gitCommitAll(t, repoRoot, "initial")
	writeTestFile(t, repoRoot+"/unrelated.txt", "dirt2\n")
	provider := localProvider{
		repoRoot:       repoRoot,
		sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{}),
		rootIdentity:   SourceIdentity{Kind: sourceIdentityKindRoot, Revision: revision},
		rootInputMode:  rootInputModeDirty,
		rootDirtyPaths: []string{"unrelated.txt"},
	}
	application := argoappv1.Application{Spec: argoappv1.ApplicationSpec{Source: &argoappv1.ApplicationSource{
		RepoURL: "https://git.example.test/org/repo.git", Path: "charts/demo", TargetRevision: "main",
		Helm: &argoappv1.ApplicationSourceHelm{ValueFiles: []string{"values*.yaml"}},
	}}}
	plan := mustPlan(t, application)
	handle := &persistentRenderCache{
		digests:           map[string]gitref.PathDigestResult{},
		filesystemDigests: map[string]filedigest.PathDigestResult{},
	}
	_, reason, ok := collectSourceIdentities(context.Background(), handle, provider, plan)
	if ok || reason != renderCacheReasonHelmGlobInputs {
		t.Fatalf("ok/reason = %v/%s, want glob-bearing dirty source ineligible even when untouched (a new file matching the glob is invisible to the path-set intersection)", ok, reason)
	}
}

func TestDirtyModeKustomizeUntrackedVariantForcesWorktreeKey(t *testing.T) {
	repoRoot := t.TempDir()
	writeTestFile(t, repoRoot+"/apps/alpha/kustomization.yaml", "resources:\n  - cm.yaml\n")
	writeTestFile(t, repoRoot+"/apps/alpha/cm.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: alpha\n")
	revision := gitCommitAll(t, repoRoot, "initial")
	provider := func(dirtyPaths []string) localProvider {
		return localProvider{
			repoRoot:       repoRoot,
			sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{}),
			rootIdentity:   SourceIdentity{Kind: sourceIdentityKindRoot, Revision: revision},
			rootInputMode:  rootInputModeDirty,
			rootDirtyPaths: dirtyPaths,
		}
	}
	application := argoappv1.Application{Spec: argoappv1.ApplicationSpec{Source: &argoappv1.ApplicationSource{
		RepoURL: "https://git.example.test/org/repo.git", Path: "apps/alpha", TargetRevision: "main",
	}}}
	plan := mustPlan(t, application)

	// Untouched kustomize source takes the committed shortcut.
	untouched := mustCollectSingleSourceIdentity(t, provider([]string{"unrelated.txt"}), plan)
	if untouched.Kind != sourceIdentityKindLocalInputs {
		t.Fatalf("untouched kustomize identity = %+v, want local-inputs", untouched)
	}

	// An untracked lower-precedence variant beside the committed file is a
	// render input (kustomize errors on multiple variants) and must force
	// the filesystem key via the Task 2 optional-variant digest paths.
	writeTestFile(t, repoRoot+"/apps/alpha/kustomization.yml", "resources:\n  - cm.yaml\n")
	variant := mustCollectSingleSourceIdentity(t, provider([]string{"apps/alpha/kustomization.yml"}), plan)
	if variant.Kind != sourceIdentityKindWorktreeInputs {
		t.Fatalf("identity with untracked variant = %+v, want worktree-inputs (variant must intersect the digest path set)", variant)
	}
}
