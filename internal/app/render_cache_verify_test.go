package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/filedigest"
	"github.com/sholdee/drydock/internal/render"
	sourcepkg "github.com/sholdee/drydock/internal/source"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func testRenderCacheStoreKey(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

func mustFilesystemDigest(t *testing.T, repoRoot string, paths []filedigest.PathDigestPath) string {
	t.Helper()
	result, err := filedigest.PathDigest(context.Background(), filedigest.PathDigestInput{RepoRoot: repoRoot, Paths: paths})
	if err != nil {
		t.Fatalf("PathDigest() error = %v", err)
	}
	return result.Digest
}

func TestPersistentRenderStateStoreSkipsWhenInputsChangedAfterDigest(t *testing.T) {
	repoRoot := t.TempDir()
	writeTestFile(t, repoRoot+"/manifests/demo/cm.yaml", "a: 1\n")
	handle := newPersistentRenderCache(testRenderCacheOptions(t), true)
	paths := []filedigest.PathDigestPath{{Path: "manifests/demo"}}
	state := persistentRenderState{
		enabled:      true,
		key:          testRenderCacheStoreKey("store-verify"),
		handle:       handle,
		recorder:     cacheevent.NewRecorder(true),
		acquisitions: cacheevent.NewAcquisitionCollector(),
		verifications: []renderInputVerification{{
			repoRoot: repoRoot,
			paths:    paths,
			digest:   mustFilesystemDigest(t, repoRoot, paths),
		}},
	}

	// The render "happened"; an editor save lands before store().
	writeTestFile(t, repoRoot+"/manifests/demo/cm.yaml", "a: 2\n")
	state.store(context.Background(), RenderResult{})
	if got := handle.store.Writes(); got != 0 {
		t.Fatalf("store wrote %d entries under a stale input digest, want 0", got)
	}

	// With a current baseline the store proceeds.
	state.verifications[0].digest = mustFilesystemDigest(t, repoRoot, paths)
	state.store(context.Background(), RenderResult{})
	if got := handle.store.Writes(); got != 1 {
		t.Fatalf("store writes = %d after verified store, want 1", got)
	}
}

// TestRenderInputsUnchangedBypassesRunScopedMemo pins that renderInputsUnchanged
// calls filedigest.PathDigest directly and NOT the run-scoped memo on
// state.handle.filesystemPathDigest.
//
// Setup: the memo is deliberately warmed with the pre-edit digest (exactly what
// the production path does), then the file is edited.  If renderInputsUnchanged
// were using the memo it would see the stale cached value == baseline → store
// would proceed → Writes() == 1 → test fails.  With the direct call the
// recomputed digest diverges → store is skipped → Writes() == 0.
func TestRenderInputsUnchangedBypassesRunScopedMemo(t *testing.T) {
	repoRoot := t.TempDir()
	writeTestFile(t, repoRoot+"/manifests/demo/cm.yaml", "a: 1\n")

	handle := newPersistentRenderCache(testRenderCacheOptions(t), true)
	if !handle.active() {
		t.Fatal("persistent cache did not open")
	}

	paths := []filedigest.PathDigestPath{{Path: "manifests/demo"}}

	// Warm the run-scoped memo through handle.filesystemPathDigest — exactly
	// what worktreeInputSourceIdentity / localInputSourceIdentity does during
	// preparePersistentRender.  This records the digest of the CURRENT file
	// content in the memo map keyed on (repoRoot, paths, forbiddenRoots).
	memoResult, err := handle.filesystemPathDigest(context.Background(), repoRoot, paths, nil)
	if err != nil {
		t.Fatalf("filesystemPathDigest() error = %v", err)
	}
	baseline := memoResult.Digest

	state := persistentRenderState{
		enabled:      true,
		key:          testRenderCacheStoreKey("memo-bypass"),
		handle:       handle,
		recorder:     cacheevent.NewRecorder(true),
		acquisitions: cacheevent.NewAcquisitionCollector(),
		verifications: []renderInputVerification{{
			repoRoot: repoRoot,
			paths:    paths,
			digest:   baseline,
		}},
	}

	// Edit the file AFTER the memo was warmed.  The memo still holds the
	// pre-edit digest; a direct PathDigest call will see the new content.
	writeTestFile(t, repoRoot+"/manifests/demo/cm.yaml", "a: 2\n")

	state.store(context.Background(), RenderResult{})

	if got := handle.store.Writes(); got != 0 {
		t.Fatalf("store wrote %d entries; renderInputsUnchanged used the memoized digest instead of re-reading disk (want 0 — guard must bypass the memo)", got)
	}

	// Sanity: confirm the skip event carried the expected reason so a
	// records-inactive false-positive cannot silently pass this test.
	events := state.recorder.Events()
	var found bool
	for _, ev := range events {
		if ev.Action == cacheevent.ActionSkipped && ev.Reason == renderCacheReasonInputsChanged {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected skipped/inputs-changed event, got %#v", events)
	}
}

// TestRenderApplicationStoreSkipsOnMidRenderEdit pins the end-to-end wiring of
// preparePersistentRender: specifically that local.inputVerifier is set and
// state.verifications is populated, so that a mid-render file mutation causes
// store() to skip.
//
// It runs two phases in sequence on the same cache handle:
//  1. Control — no mutation → store must write exactly one entry.
//  2. Dirty — renderObserver mutates the source file mid-render → store must
//     skip with reason "inputs-changed".
//
// The control phase proves the wiring produces stores at all, making the skip
// assertion in the dirty phase meaningful.
func TestRenderApplicationStoreSkipsOnMidRenderEdit(t *testing.T) {
	repoRoot := t.TempDir()

	// Plain YAML directory source — renders with zero external tooling.
	writeTestFile(t, filepath.Join(repoRoot, "manifests", "demo", "cm.yaml"),
		"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\ndata:\n  value: initial\n")
	rev := gitCommitAll(t, repoRoot, "initial")

	application := argoappv1.Application{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "argocd"},
		Spec: argoappv1.ApplicationSpec{
			Source: &argoappv1.ApplicationSource{
				RepoURL:        "https://git.example.test/org/repo.git",
				Path:           "manifests/demo",
				TargetRevision: "main",
				Directory:      &argoappv1.ApplicationSourceDirectory{},
			},
			Destination: argoappv1.ApplicationDestination{Namespace: "default"},
		},
	}

	cacheOptions := testRenderCacheOptions(t)

	// ── Phase 1: control — no mid-render mutation ────────────────────────────
	controlHandle := newPersistentRenderCache(cacheOptions, true)
	if !controlHandle.active() {
		t.Fatal("control persistent cache did not open")
	}

	controlProvider := localProvider{
		repoRoot:       repoRoot,
		sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{}),
		rootIdentity:   SourceIdentity{Kind: sourceIdentityKindRoot, Revision: rev},
		rootInputMode:  rootInputModeDirty,
		cacheEvents:    cacheevent.NewRecorder(true),
		acquisitions:   cacheevent.NewAcquisitionCollector(),
		// renderObserver intentionally nil — no mutation
	}

	_, err := RenderApplicationWithOptions(context.Background(), application, controlProvider, ApplicationRenderOptions{
		persistent: persistentRenderOptions{cache: controlHandle},
	})
	if err != nil {
		t.Fatalf("control RenderApplicationWithOptions() error = %v", err)
	}
	if got := controlHandle.store.Writes(); got != 1 {
		t.Fatalf("control phase: store.Writes() = %d, want 1 (wiring must produce a store when inputs are unchanged)", got)
	}

	// ── Phase 2: dirty — mutate a source file mid-render ────────────────────
	// Use a fresh cache handle (different cacheDir) so the miss→render→store
	// path is exercised rather than returning a hit from phase 1.
	dirtyHandle := newPersistentRenderCache(testRenderCacheOptions(t), true)
	if !dirtyHandle.active() {
		t.Fatal("dirty persistent cache did not open")
	}

	mutatedOnce := false
	dirtyProvider := localProvider{
		repoRoot:       repoRoot,
		sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{}),
		rootIdentity:   SourceIdentity{Kind: sourceIdentityKindRoot, Revision: rev},
		rootInputMode:  rootInputModeDirty,
		cacheEvents:    cacheevent.NewRecorder(true),
		acquisitions:   cacheevent.NewAcquisitionCollector(),
		renderObserver: func(_ render.ResolvedSource) {
			// Guard: mutate only on first call so concurrency does not
			// interfere.  A single overwrite is sufficient to change
			// the directory digest.
			if mutatedOnce {
				return
			}
			mutatedOnce = true
			writeTestFile(t, filepath.Join(repoRoot, "manifests", "demo", "cm.yaml"),
				"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\ndata:\n  value: mutated\n")
		},
	}

	_, err = RenderApplicationWithOptions(context.Background(), application, dirtyProvider, ApplicationRenderOptions{
		persistent: persistentRenderOptions{cache: dirtyHandle},
	})
	if err != nil {
		t.Fatalf("dirty RenderApplicationWithOptions() error = %v", err)
	}

	if got := dirtyHandle.store.Writes(); got != 0 {
		t.Fatalf("dirty phase: store.Writes() = %d, want 0 — mid-render mutation must prevent the store", got)
	}

	// Confirm the skip reason is inputs-changed, not records-inactive or
	// any other false-positive reason.
	events := dirtyProvider.cacheEvents.Events()
	var foundSkip bool
	for _, ev := range events {
		if ev.Action == cacheevent.ActionSkipped && ev.Reason == renderCacheReasonInputsChanged {
			foundSkip = true
		}
	}
	if !foundSkip {
		t.Fatalf("expected skipped/inputs-changed event in dirty phase, got %#v", events)
	}
}

func TestRenderApplicationCleanModeStoreSkipsOnMidRenderEdit(t *testing.T) {
	repoRoot := t.TempDir()
	sourceFile := filepath.Join(repoRoot, "manifests", "demo", "cm.yaml")
	writeTestFile(t, sourceFile,
		"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\ndata:\n  value: initial\n")
	rev := gitCommitAll(t, repoRoot, "initial")

	application := argoappv1.Application{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "argocd"},
		Spec: argoappv1.ApplicationSpec{
			Source: &argoappv1.ApplicationSource{
				RepoURL:        "https://git.example.test/org/repo.git",
				Path:           "manifests/demo",
				TargetRevision: "main",
				Directory:      &argoappv1.ApplicationSourceDirectory{},
			},
			Destination: argoappv1.ApplicationDestination{Namespace: "default"},
		},
	}

	cleanProvider := func(observer func(render.ResolvedSource)) localProvider {
		return localProvider{
			repoRoot:       repoRoot,
			sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{}),
			rootIdentity:   SourceIdentity{Kind: sourceIdentityKindRoot, Revision: rev},
			rootInputMode:  rootInputModeClean,
			cacheEvents:    cacheevent.NewRecorder(true),
			acquisitions:   cacheevent.NewAcquisitionCollector(),
			renderObserver: observer,
		}
	}

	controlHandle := newPersistentRenderCache(testRenderCacheOptions(t), true)
	_, err := RenderApplicationWithOptions(context.Background(), application, cleanProvider(nil), ApplicationRenderOptions{
		persistent: persistentRenderOptions{cache: controlHandle},
	})
	if err != nil {
		t.Fatalf("control render error = %v", err)
	}
	if got := controlHandle.store.Writes(); got != 1 {
		t.Fatalf("control phase: store.Writes() = %d, want 1", got)
	}

	dirtyHandle := newPersistentRenderCache(testRenderCacheOptions(t), true)
	mutated := false
	provider := cleanProvider(func(_ render.ResolvedSource) {
		if mutated {
			return
		}
		mutated = true
		writeTestFile(t, sourceFile,
			"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\ndata:\n  value: mutated\n")
	})
	_, err = RenderApplicationWithOptions(context.Background(), application, provider, ApplicationRenderOptions{
		persistent: persistentRenderOptions{cache: dirtyHandle},
	})
	if err != nil {
		t.Fatalf("mutating render error = %v", err)
	}
	if got := dirtyHandle.store.Writes(); got != 0 {
		t.Fatalf("clean-mode mid-render edit: store.Writes() = %d, want 0", got)
	}
	events := provider.cacheEvents.Events()
	found := false
	for _, ev := range events {
		if ev.Action == cacheevent.ActionSkipped && ev.Reason == renderCacheReasonInputsChanged {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected skipped/inputs-changed event, got %#v", events)
	}
}

func TestStoreInputsChangedEventCarriesErrorDetail(t *testing.T) {
	state := persistentRenderState{
		enabled:      true,
		key:          testRenderCacheStoreKey("inputs-changed-detail"),
		handle:       newPersistentRenderCache(testRenderCacheOptions(t), true),
		recorder:     cacheevent.NewRecorder(true),
		acquisitions: cacheevent.NewAcquisitionCollector(),
		verifications: []renderInputVerification{{
			repoRoot: t.TempDir(),
			paths:    []filedigest.PathDigestPath{{Path: "missing-required.yaml"}},
			digest:   "stale",
		}},
	}
	state.store(context.Background(), RenderResult{})

	events := state.recorder.Events()
	for _, ev := range events {
		if ev.Action == cacheevent.ActionSkipped && ev.Reason == renderCacheReasonInputsChanged {
			if ev.Error == "" {
				t.Fatalf("inputs-changed skip from a digest ERROR must carry the error text, got %#v", ev)
			}
			return
		}
	}
	t.Fatalf("no skipped/inputs-changed event, got %#v", events)
}

func TestDirtyWorktreeServesCommittedHitsForUntouchedApps(t *testing.T) {
	repoRoot := t.TempDir()
	writeTestFile(t, filepath.Join(repoRoot, "apps", "alpha", "cm.yaml"),
		"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: alpha\ndata:\n  value: one\n")
	writeTestFile(t, filepath.Join(repoRoot, "apps", "beta", "cm.yaml"),
		"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: beta\ndata:\n  value: one\n")
	rev := gitCommitAll(t, repoRoot, "initial")
	cacheOptions := testRenderCacheOptions(t) // ONE options value: both runs share the store

	application := func(name string) argoappv1.Application {
		return argoappv1.Application{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "argocd"},
			Spec: argoappv1.ApplicationSpec{
				Source: &argoappv1.ApplicationSource{
					RepoURL:        "https://git.example.test/org/repo.git",
					Path:           "apps/" + name,
					TargetRevision: "main",
					Directory:      &argoappv1.ApplicationSourceDirectory{},
				},
				Destination: argoappv1.ApplicationDestination{Namespace: "default"},
			},
		}
	}
	renderApp := func(handle *persistentRenderCache, mode rootInputMode, dirtyPaths []string, name string) []cacheevent.Event {
		provider := localProvider{
			repoRoot:       repoRoot,
			sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{}),
			rootIdentity:   SourceIdentity{Kind: sourceIdentityKindRoot, Revision: rev},
			rootInputMode:  mode,
			rootDirtyPaths: dirtyPaths,
			cacheEvents:    cacheevent.NewRecorder(true),
			acquisitions:   cacheevent.NewAcquisitionCollector(),
		}
		_, err := RenderApplicationWithOptions(context.Background(), application(name), provider, ApplicationRenderOptions{
			persistent: persistentRenderOptions{cache: handle},
		})
		if err != nil {
			t.Fatalf("render %s: %v", name, err)
		}
		return provider.cacheEvents.Events()
	}
	hasAction := func(events []cacheevent.Event, action cacheevent.Action) bool {
		for _, ev := range events {
			if ev.Action == action {
				return true
			}
		}
		return false
	}

	// Run 1: clean worktree — both apps render and store under committed keys.
	cleanHandle := newPersistentRenderCache(cacheOptions, true)
	renderApp(cleanHandle, rootInputModeClean, nil, "alpha")
	renderApp(cleanHandle, rootInputModeClean, nil, "beta")
	if got := cleanHandle.store.Writes(); got != 2 {
		t.Fatalf("clean run stores = %d, want 2", got)
	}

	// Edit beta only; run 2 in dirty mode with the enumerated dirty set.
	writeTestFile(t, filepath.Join(repoRoot, "apps", "beta", "cm.yaml"),
		"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: beta\ndata:\n  value: two\n")
	dirtyHandle := newPersistentRenderCache(cacheOptions, true)
	dirtyPaths := []string{"apps/beta/cm.yaml"}

	alphaEvents := renderApp(dirtyHandle, rootInputModeDirty, dirtyPaths, "alpha")
	if !hasAction(alphaEvents, cacheevent.ActionHit) {
		t.Fatalf("untouched app must HIT its committed-key entry across the mode transition, got %#v", alphaEvents)
	}
	betaEvents := renderApp(dirtyHandle, rootInputModeDirty, dirtyPaths, "beta")
	if !hasAction(betaEvents, cacheevent.ActionStore) || hasAction(betaEvents, cacheevent.ActionHit) {
		t.Fatalf("touched app must miss and store under a worktree key, got %#v", betaEvents)
	}
	if got := dirtyHandle.store.Writes(); got != 1 {
		t.Fatalf("dirty run stores = %d, want exactly 1 (the touched app)", got)
	}
}

func TestRenderApplicationDirtyShortcutStoreSkipsOnMidRenderEdit(t *testing.T) {
	repoRoot := t.TempDir()
	sourceFile := filepath.Join(repoRoot, "apps", "alpha", "cm.yaml")
	writeTestFile(t, sourceFile,
		"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: alpha\ndata:\n  value: initial\n")
	writeTestFile(t, filepath.Join(repoRoot, "unrelated.txt"), "dirt\n")
	rev := gitCommitAll(t, repoRoot, "initial")
	writeTestFile(t, filepath.Join(repoRoot, "unrelated.txt"), "dirt2\n")

	application := argoappv1.Application{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha", Namespace: "argocd"},
		Spec: argoappv1.ApplicationSpec{
			Source: &argoappv1.ApplicationSource{
				RepoURL:        "https://git.example.test/org/repo.git",
				Path:           "apps/alpha",
				TargetRevision: "main",
				Directory:      &argoappv1.ApplicationSourceDirectory{},
			},
			Destination: argoappv1.ApplicationDestination{Namespace: "default"},
		},
	}

	handle := newPersistentRenderCache(testRenderCacheOptions(t), true)
	mutated := false
	provider := localProvider{
		repoRoot:       repoRoot,
		sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{}),
		rootIdentity:   SourceIdentity{Kind: sourceIdentityKindRoot, Revision: rev},
		rootInputMode:  rootInputModeDirty,
		rootDirtyPaths: []string{"unrelated.txt"}, // alpha untouched → committed shortcut
		cacheEvents:    cacheevent.NewRecorder(true),
		acquisitions:   cacheevent.NewAcquisitionCollector(),
		renderObserver: func(_ render.ResolvedSource) {
			if mutated {
				return
			}
			mutated = true
			writeTestFile(t, sourceFile,
				"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: alpha\ndata:\n  value: mutated\n")
		},
	}
	_, err := RenderApplicationWithOptions(context.Background(), application, provider, ApplicationRenderOptions{
		persistent: persistentRenderOptions{cache: handle},
	})
	if err != nil {
		t.Fatalf("render error = %v", err)
	}
	if got := handle.store.Writes(); got != 0 {
		t.Fatalf("store.Writes() = %d, want 0 — a mid-render edit under a committed-shortcut key must skip the store", got)
	}
	events := provider.cacheEvents.Events()
	found := false
	for _, ev := range events {
		if ev.Action == cacheevent.ActionSkipped && ev.Reason == renderCacheReasonInputsChanged {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected skipped/inputs-changed event, got %#v", events)
	}
}
