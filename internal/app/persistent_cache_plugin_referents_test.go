package app

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/cacheevent"
)

// These tests pin the stale-cache regression fix for builtin Kustomize
// plugin configs (PatchTransformer, ConfigMapGenerator, and friends): they
// read referents through the LISTING kustomization's loader, so those
// referents sit outside the config file itself. Before the fix, the
// persistent render cache's input digest walked the plugin config file
// itself (and KSOPS generator files) but never the files a builtin config
// reads through the loader, so a referent-only edit left the digest (and
// therefore the cache key) unchanged: a warm build silently served the
// stale pre-edit render. Each case here commits an initial referent value,
// populates the cache, edits ONLY the referent, and asserts a warm build
// (same cache dir) re-renders and shows the new value, matching a
// fresh-cache-dir build.

// persistentConfigMapDataValueOK is the non-fatal counterpart to
// persistentConfigMapDataValue: it reports the data key's value plus two
// independent flags — whether the ConfigMap itself was found in the result,
// and whether the key was set on it. Callers that assert a field's ABSENCE
// (a ValueAddTransformer target that no longer matches a given ConfigMap)
// must check cmFound too: without it, a ConfigMap that vanished from the
// manifests entirely reads identically to one that is present but missing
// the key.
func persistentConfigMapDataValueOK(result BuildResult, appName, cmName, key string) (value string, keyFound, cmFound bool) {
	for _, item := range result.ApplicationManifests {
		if item.Application.Name != appName || item.Manifest.Object == nil || item.Manifest.Object.GetName() != cmName {
			continue
		}
		cmFound = true
		data, ok := item.Manifest.Object.Object["data"].(map[string]any)
		if !ok {
			return "", false, cmFound
		}
		value, keyFound = data[key].(string)
		return value, keyFound, cmFound
	}
	return "", false, false
}

// assertPersistentConfigMapMarkerAbsent asserts that a ConfigMap is present
// in the result but its marker data key is not set — distinguishing "field
// absent" from "ConfigMap missing entirely" (which reads identically as far
// as the marker value is concerned, but is a much worse regression).
func assertPersistentConfigMapMarkerAbsent(t *testing.T, result BuildResult, appName, cmName string) {
	t.Helper()
	_, keyFound, cmFound := persistentConfigMapDataValueOK(result, appName, cmName, "marker")
	if !cmFound {
		t.Fatalf("%s ConfigMap missing entirely", cmName)
	}
	if keyFound {
		t.Fatalf("%s unexpectedly has a marker field", cmName)
	}
}

// applicationManifestObjects returns the raw manifest objects rendered for
// one Application, in build order — enough to reflect.DeepEqual an entire
// warm build's render against a fresh-cache-dir build's, rather than
// comparing a single field of a single object.
func applicationManifestObjects(result BuildResult, appName string) []map[string]any {
	var objects []map[string]any
	for _, item := range result.ApplicationManifests {
		if item.Application.Name != appName || item.Manifest.Object == nil {
			continue
		}
		objects = append(objects, item.Manifest.Object.Object)
	}
	return objects
}

func writePersistentCachePatchTransformerReferent(t *testing.T, root, name, value string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "manifests", name, "patch.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
data:
  value: `+value+`
`)
}

// writePersistentCacheDemoConfigMap writes the demo ConfigMap resource
// (cm.yaml) that the PatchTransformer, PatchStrategicMergeTransformer,
// directory-entry, and inline-transformer cases all patch: a fixed base
// value the plugin config's referent overwrites.
func writePersistentCacheDemoConfigMap(t *testing.T, root, name string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "manifests", name, "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
data:
  value: base
`)
}

// writePersistentCachePatchTransformerApp: a PatchTransformer config file
// (cfg/transformer.yaml) whose "path: patch.yaml" resolves against the
// LISTING kustomization directory (manifests/<name>), not cfg/.
func writePersistentCachePatchTransformerApp(t *testing.T, root, name, value string) {
	t.Helper()
	writePersistentCacheKustomizeApp(t, root, name, `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cm.yaml
transformers:
  - cfg/transformer.yaml
`)
	writePersistentCacheDemoConfigMap(t, root, name)
	writeTestFile(t, filepath.Join(root, "manifests", name, "cfg", "transformer.yaml"), `apiVersion: builtin
kind: PatchTransformer
metadata:
  name: label-demo
path: patch.yaml
target:
  kind: ConfigMap
  name: demo
`)
	writePersistentCachePatchTransformerReferent(t, root, name, value)
}

// writePersistentCacheConfigMapGeneratorApp: a ConfigMapGenerator config file
// (cfg/generator.yaml) whose "files: [value.txt]" reads
// manifests/<name>/value.txt (the listing directory), not
// manifests/<name>/cfg/value.txt.
func writePersistentCacheConfigMapGeneratorApp(t *testing.T, root, name, value string) {
	t.Helper()
	writePersistentCacheKustomizeApp(t, root, name, `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
generators:
  - cfg/generator.yaml
`)
	writeTestFile(t, filepath.Join(root, "manifests", name, "cfg", "generator.yaml"), `apiVersion: builtin
kind: ConfigMapGenerator
metadata:
  name: generated
options:
  disableNameSuffixHash: true
files:
  - value.txt
`)
	writeTestFile(t, filepath.Join(root, "manifests", name, "value.txt"), value)
}

// writePersistentCacheStrategicMergePatchApp: a PatchStrategicMergeTransformer
// config file (cfg/transformer.yaml) whose "paths: [patch.yaml]" resolves
// against the listing directory.
func writePersistentCacheStrategicMergePatchApp(t *testing.T, root, name, value string) {
	t.Helper()
	writePersistentCacheKustomizeApp(t, root, name, `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cm.yaml
transformers:
  - cfg/transformer.yaml
`)
	writePersistentCacheDemoConfigMap(t, root, name)
	writeTestFile(t, filepath.Join(root, "manifests", name, "cfg", "transformer.yaml"), `apiVersion: builtin
kind: PatchStrategicMergeTransformer
metadata:
  name: label-demo
paths:
  - patch.yaml
`)
	writePersistentCachePatchTransformerReferent(t, root, name, value)
}

// writePersistentCacheDirectoryEntryPluginApp: a "transformers: [./cfg]"
// directory entry whose nested PatchTransformer still resolves its
// referent (patch.yaml) against the OUTER listing directory
// (manifests/<name>), not the entry directory (manifests/<name>/cfg).
func writePersistentCacheDirectoryEntryPluginApp(t *testing.T, root, name, value string) {
	t.Helper()
	writePersistentCacheKustomizeApp(t, root, name, `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cm.yaml
transformers:
  - ./cfg
`)
	writePersistentCacheDemoConfigMap(t, root, name)
	writeTestFile(t, filepath.Join(root, "manifests", name, "cfg", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - transformer.yaml
`)
	writeTestFile(t, filepath.Join(root, "manifests", name, "cfg", "transformer.yaml"), `apiVersion: builtin
kind: PatchTransformer
metadata:
  name: label-demo
path: patch.yaml
target:
  kind: ConfigMap
  name: demo
`)
	writePersistentCachePatchTransformerReferent(t, root, name, value)
}

// TestPersistentCacheBuiltinPluginConfigReferentInvalidatesCache covers
// PatchTransformer path, ConfigMapGenerator files, PatchStrategicMergeTransformer
// paths, and a transformers: directory entry: for each, editing ONLY the
// referent (never the plugin config file) must rotate the persistent cache
// key so a warm build re-renders and reflects the new value, matching a
// fresh-cache-dir build object for object.
func TestPersistentCacheBuiltinPluginConfigReferentInvalidatesCache(t *testing.T) {
	cases := []struct {
		name    string
		appName string
		cmName  string
		dataKey string
		write   func(t *testing.T, root, appName, value string)
	}{
		{name: "PatchTransformer path", appName: "patch-app", cmName: "demo", dataKey: "value", write: writePersistentCachePatchTransformerApp},
		{name: "ConfigMapGenerator files", appName: "cmgen-app", cmName: "generated", dataKey: "value.txt", write: writePersistentCacheConfigMapGeneratorApp},
		{name: "PatchStrategicMergeTransformer paths", appName: "smp-app", cmName: "demo", dataKey: "value", write: writePersistentCacheStrategicMergePatchApp},
		{name: "directory entry", appName: "dir-app", cmName: "demo", dataKey: "value", write: writePersistentCacheDirectoryEntryPluginApp},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			testCase.write(t, root, testCase.appName, "initial")
			gitCommitAll(t, root, "initial")
			cacheDir := t.TempDir()
			request := persistentBuildRequest(root, cacheDir)
			request.RecordCacheEvents = true

			coldResult, err := (Orchestrator{}).Build(context.Background(), request)
			if err != nil {
				t.Fatalf("cold Build() error = %v", err)
			}
			coldValue, ok, _ := persistentConfigMapDataValueOK(coldResult, testCase.appName, testCase.cmName, testCase.dataKey)
			if !ok || !strings.Contains(coldValue, "initial") {
				t.Fatalf("cold value = %q, ok = %v, want containing %q", coldValue, ok, "initial")
			}
			if !hasRenderCacheEvent(coldResult.CacheEvents, cacheevent.ActionStore, "argocd/"+testCase.appName, "") {
				t.Fatalf("CacheEvents = %#v, want %s persistent store", coldResult.CacheEvents, testCase.appName)
			}

			testCase.write(t, root, testCase.appName, "changed")
			gitCommitAll(t, root, "change referent only")

			warmOrchestrator := Orchestrator{}
			warmRendered := renderedSourceCounts(&warmOrchestrator)
			warmResult, err := warmOrchestrator.Build(context.Background(), request)
			if err != nil {
				t.Fatalf("warm Build() error = %v", err)
			}
			if got := warmRendered["manifests/"+testCase.appName]; got == 0 {
				t.Fatalf("warm build after referent-only change performed 0 renders for %s; stale cache would silently reuse the pre-edit value", testCase.appName)
			}
			warmValue, ok, _ := persistentConfigMapDataValueOK(warmResult, testCase.appName, testCase.cmName, testCase.dataKey)
			if !ok || !strings.Contains(warmValue, "changed") {
				t.Fatalf("warm value = %q, ok = %v, want containing %q", warmValue, ok, "changed")
			}

			freshCacheDir := t.TempDir()
			freshResult, err := (Orchestrator{}).Build(context.Background(), persistentBuildRequest(root, freshCacheDir))
			if err != nil {
				t.Fatalf("fresh-cache Build() error = %v", err)
			}
			if _, ok, _ := persistentConfigMapDataValueOK(freshResult, testCase.appName, testCase.cmName, testCase.dataKey); !ok {
				t.Fatalf("fresh-cache value missing for %s/%s[%s]", testCase.appName, testCase.cmName, testCase.dataKey)
			}
			if !reflect.DeepEqual(applicationManifestObjects(warmResult, testCase.appName), applicationManifestObjects(freshResult, testCase.appName)) {
				t.Fatalf("warm %s manifests != fresh-cache manifests; warm build served a stale render", testCase.appName)
			}
		})
	}
}

// writePersistentCacheValueAddTransformerApp: a ValueAddTransformer whose
// "targetFilePath: targets.yaml" referent selects which of two ConfigMaps
// (demoA, demoB) receives the added field. The transformer's own config
// file never changes across the two writes below — only targets.yaml,
// the referent, does.
func writePersistentCacheValueAddTransformerApp(t *testing.T, root, name, target string) {
	t.Helper()
	writePersistentCacheKustomizeApp(t, root, name, `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cm-a.yaml
  - cm-b.yaml
transformers:
  - cfg/transformer.yaml
`)
	writeTestFile(t, filepath.Join(root, "manifests", name, "cm-a.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: demoA
`)
	writeTestFile(t, filepath.Join(root, "manifests", name, "cm-b.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: demoB
`)
	writeTestFile(t, filepath.Join(root, "manifests", name, "cfg", "transformer.yaml"), `apiVersion: builtin
kind: ValueAddTransformer
metadata:
  name: add-marker
value: added
targetFilePath: targets.yaml
`)
	writeTestFile(t, filepath.Join(root, "manifests", name, "targets.yaml"), `targets:
  - selector:
      kind: ConfigMap
      name: `+target+`
    fieldPath: data/marker
`)
}

// TestPersistentCacheValueAddTransformerTargetFilePathReferentInvalidatesCache
// covers ValueAddTransformer's targetFilePath referent specifically, since
// its referent holds a target selector rather than a value string: editing
// ONLY targets.yaml to select demoB instead of demoA must rotate the cache
// key so a warm build moves the added field, matching a fresh-cache-dir
// build.
func TestPersistentCacheValueAddTransformerTargetFilePathReferentInvalidatesCache(t *testing.T) {
	const appName = "value-add-app"
	root := t.TempDir()
	writePersistentCacheValueAddTransformerApp(t, root, appName, "demoA")
	gitCommitAll(t, root, "initial")
	cacheDir := t.TempDir()
	request := persistentBuildRequest(root, cacheDir)
	request.RecordCacheEvents = true

	coldResult, err := (Orchestrator{}).Build(context.Background(), request)
	if err != nil {
		t.Fatalf("cold Build() error = %v", err)
	}
	if value, ok, _ := persistentConfigMapDataValueOK(coldResult, appName, "demoA", "marker"); !ok || value != "added" {
		t.Fatalf("cold demoA marker = %q, ok = %v, want \"added\"", value, ok)
	}
	assertPersistentConfigMapMarkerAbsent(t, coldResult, appName, "demoB")
	if !hasRenderCacheEvent(coldResult.CacheEvents, cacheevent.ActionStore, "argocd/"+appName, "") {
		t.Fatalf("CacheEvents = %#v, want %s persistent store", coldResult.CacheEvents, appName)
	}

	writePersistentCacheValueAddTransformerApp(t, root, appName, "demoB")
	gitCommitAll(t, root, "change targetFilePath referent only")

	warmOrchestrator := Orchestrator{}
	warmRendered := renderedSourceCounts(&warmOrchestrator)
	warmResult, err := warmOrchestrator.Build(context.Background(), request)
	if err != nil {
		t.Fatalf("warm Build() error = %v", err)
	}
	if got := warmRendered["manifests/"+appName]; got == 0 {
		t.Fatalf("warm build after targetFilePath-only change performed 0 renders; stale cache would silently keep targeting demoA")
	}
	assertPersistentConfigMapMarkerAbsent(t, warmResult, appName, "demoA")
	if value, ok, _ := persistentConfigMapDataValueOK(warmResult, appName, "demoB", "marker"); !ok || value != "added" {
		t.Fatalf("warm demoB marker = %q, ok = %v, want \"added\"", value, ok)
	}

	freshCacheDir := t.TempDir()
	freshResult, err := (Orchestrator{}).Build(context.Background(), persistentBuildRequest(root, freshCacheDir))
	if err != nil {
		t.Fatalf("fresh-cache Build() error = %v", err)
	}
	if _, ok, _ := persistentConfigMapDataValueOK(freshResult, appName, "demoB", "marker"); !ok {
		t.Fatalf("fresh-cache demoB marker missing")
	}
	if !reflect.DeepEqual(applicationManifestObjects(warmResult, appName), applicationManifestObjects(freshResult, appName)) {
		t.Fatalf("warm %s manifests != fresh-cache manifests; warm build served a stale render", appName)
	}
}

// TestPersistentCacheBuiltinPluginConfigReferentDirtyEditForcesRerender is
// the dirty-tree variant: an UNCOMMITTED edit to the referent (never
// committed at all) must still intersect the app's digested input path set
// and force the worktree-inputs re-render flow, rather than falling through
// to the committed-shortcut path that serves a stale committed identity for
// dirt outside an app's known inputs.
func TestPersistentCacheBuiltinPluginConfigReferentDirtyEditForcesRerender(t *testing.T) {
	const appName = "patch-app"
	root := t.TempDir()
	writePersistentCachePatchTransformerApp(t, root, appName, "initial")
	gitCommitAll(t, root, "initial")
	cacheDir := t.TempDir()
	request := persistentBuildRequest(root, cacheDir)
	request.RecordCacheEvents = true

	coldResult, err := (Orchestrator{}).Build(context.Background(), request)
	if err != nil {
		t.Fatalf("cold Build() error = %v", err)
	}
	if value, ok, _ := persistentConfigMapDataValueOK(coldResult, appName, "demo", "value"); !ok || value != "initial" {
		t.Fatalf("cold value = %q, ok = %v, want \"initial\"", value, ok)
	}
	if !hasRenderCacheEvent(coldResult.CacheEvents, cacheevent.ActionStore, "argocd/"+appName, "") {
		t.Fatalf("CacheEvents = %#v, want %s persistent store", coldResult.CacheEvents, appName)
	}

	// Edit ONLY the referent, and leave it uncommitted (dirty worktree).
	writePersistentCachePatchTransformerReferent(t, root, appName, "changed")

	warmOrchestrator := Orchestrator{}
	warmRendered := renderedSourceCounts(&warmOrchestrator)
	warmResult, err := warmOrchestrator.Build(context.Background(), request)
	if err != nil {
		t.Fatalf("dirty warm Build() error = %v", err)
	}
	if got := warmRendered["manifests/"+appName]; got == 0 {
		t.Fatalf("dirty referent edit performed 0 renders; stale cache would silently reuse the committed value")
	}
	warmValue, ok, _ := persistentConfigMapDataValueOK(warmResult, appName, "demo", "value")
	if !ok || warmValue != "changed" {
		t.Fatalf("dirty warm value = %q, ok = %v, want \"changed\"", warmValue, ok)
	}

	freshCacheDir := t.TempDir()
	freshResult, err := (Orchestrator{}).Build(context.Background(), persistentBuildRequest(root, freshCacheDir))
	if err != nil {
		t.Fatalf("dirty fresh-cache Build() error = %v", err)
	}
	if _, ok, _ := persistentConfigMapDataValueOK(freshResult, appName, "demo", "value"); !ok {
		t.Fatalf("dirty fresh-cache value missing")
	}
	if !reflect.DeepEqual(applicationManifestObjects(warmResult, appName), applicationManifestObjects(freshResult, appName)) {
		t.Fatalf("dirty warm %s manifests != dirty fresh-cache manifests; warm build served a stale render", appName)
	}
}

// TestPersistentCacheInlineBuiltinTransformerReferentBecomesEligible covers
// an inline transformers: entry (a literal builtin plugin YAML document
// embedded in the kustomization). Misreading it as a path would mark the
// source persistence-INELIGIBLE (skipped, input-graph-unsupported) rather
// than stale, so it cannot fail the "must FAIL" pattern the other cases
// use. Instead this asserts the intended behavior directly: a cold build
// stores a persistent cache entry (not a skip), an unchanged warm build is
// a cache HIT (0 renders), and a change to the inline entry's own external
// referent is NOT served stale.
func TestPersistentCacheInlineBuiltinTransformerReferentBecomesEligible(t *testing.T) {
	const appName = "inline-app"
	writeApp := func(t *testing.T, root, value string) {
		t.Helper()
		writePersistentCacheKustomizeApp(t, root, appName, `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cm.yaml
transformers:
  - |
    apiVersion: builtin
    kind: PatchStrategicMergeTransformer
    metadata:
      name: inline
    paths:
      - patch.yaml
`)
		writePersistentCacheDemoConfigMap(t, root, appName)
		writePersistentCachePatchTransformerReferent(t, root, appName, value)
	}

	root := t.TempDir()
	writeApp(t, root, "initial")
	gitCommitAll(t, root, "initial")
	cacheDir := t.TempDir()
	request := persistentBuildRequest(root, cacheDir)
	request.RecordCacheEvents = true

	coldResult, err := (Orchestrator{}).Build(context.Background(), request)
	if err != nil {
		t.Fatalf("cold Build() error = %v", err)
	}
	if value, ok, _ := persistentConfigMapDataValueOK(coldResult, appName, "demo", "value"); !ok || value != "initial" {
		t.Fatalf("cold value = %q, ok = %v, want \"initial\"", value, ok)
	}
	if !hasRenderCacheEvent(coldResult.CacheEvents, cacheevent.ActionStore, "argocd/"+appName, "") {
		t.Fatalf("CacheEvents = %#v, want %s persistent store", coldResult.CacheEvents, appName)
	}

	// Unchanged warm build: must be a cache hit.
	unchangedOrchestrator := Orchestrator{}
	unchangedRendered := renderedSourceCounts(&unchangedOrchestrator)
	unchangedResult, err := unchangedOrchestrator.Build(context.Background(), request)
	if err != nil {
		t.Fatalf("unchanged warm Build() error = %v", err)
	}
	if got := unchangedRendered["manifests/"+appName]; got != 0 {
		t.Fatalf("unchanged warm build renders = %d, want 0 (cache hit)", got)
	}
	if !hasRenderCacheEvent(unchangedResult.CacheEvents, cacheevent.ActionHit, "argocd/"+appName, "") {
		t.Fatalf("CacheEvents = %#v, want %s persistent hit", unchangedResult.CacheEvents, appName)
	}

	// Change ONLY the inline entry's external referent (patch.yaml).
	writeApp(t, root, "changed")
	gitCommitAll(t, root, "change inline transformer referent only")

	changedOrchestrator := Orchestrator{}
	changedRendered := renderedSourceCounts(&changedOrchestrator)
	changedResult, err := changedOrchestrator.Build(context.Background(), request)
	if err != nil {
		t.Fatalf("referent-change warm Build() error = %v", err)
	}
	if got := changedRendered["manifests/"+appName]; got == 0 {
		t.Fatalf("referent-only change performed 0 renders; stale cache would silently reuse the pre-edit value")
	}
	if value, ok, _ := persistentConfigMapDataValueOK(changedResult, appName, "demo", "value"); !ok || value != "changed" {
		t.Fatalf("post-change value = %q, ok = %v, want \"changed\"", value, ok)
	}
}
