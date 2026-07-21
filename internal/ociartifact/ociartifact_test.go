package ociartifact

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/cache"
	"github.com/sholdee/drydock/internal/ociartifact/ocitest"
)

func TestIsOCIURLTotalOverSpellings(t *testing.T) {
	for _, url := range []string{
		"oci://ghcr.io/org/app",
		"OCI://ghcr.io/org/app",
		"Oci://ghcr.io/org/app",
		"  oci://ghcr.io/org/app  ",
	} {
		if !IsOCIURL(url) {
			t.Fatalf("IsOCIURL(%q) = false, want true", url)
		}
	}
	for _, url := range []string{
		"https://ghcr.io/org/app",
		"ghcr.io/org/app",
		"git@github.com:org/app.git",
		"",
		"oci:/ghcr.io/org/app",
	} {
		if IsOCIURL(url) {
			t.Fatalf("IsOCIURL(%q) = true, want false", url)
		}
	}
}

func TestNormalizeURL(t *testing.T) {
	for _, testCase := range []struct{ input, want string }{
		{"oci://ghcr.io/org/app", "ghcr.io/org/app"},
		{"OCI://ghcr.io/org/app/", "ghcr.io/org/app"},
		{"  oci://ghcr.io/org/app ", "ghcr.io/org/app"},
		{"ghcr.io/org/app", "ghcr.io/org/app"},
	} {
		if got := NormalizeURL(testCase.input); got != testCase.want {
			t.Fatalf("NormalizeURL(%q) = %q, want %q", testCase.input, got, testCase.want)
		}
	}
}

func TestIsDigest(t *testing.T) {
	if !IsDigest("sha256:" + strings.Repeat("ab", 32)) {
		t.Fatal("sha256 digest not recognized")
	}
	if !IsDigest("sha512:" + strings.Repeat("cd", 64)) {
		t.Fatal("sha512 digest not recognized")
	}
	for _, revision := range []string{"1.2.3", "latest", "sha256:XYZ", "", "~1.2"} {
		if IsDigest(revision) {
			t.Fatalf("IsDigest(%q) = true, want false", revision)
		}
	}
}

// TestDefaultsMatchArgoRepoServer pins the adopted Argo CD repo-server
// defaults: --oci-layer-media-types (argocd_repo_server.go:267) and
// --oci-manifest-max-extracted-size "1G" (argocd_repo_server.go:262).
func TestDefaultsMatchArgoRepoServer(t *testing.T) {
	want := []string{
		"application/vnd.oci.image.layer.v1.tar",
		"application/vnd.oci.image.layer.v1.tar+gzip",
		"application/vnd.cncf.helm.chart.content.v1.tar+gzip",
	}
	got := defaultLayerMediaTypes()
	if len(got) != len(want) {
		t.Fatalf("defaultLayerMediaTypes() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("defaultLayerMediaTypes()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if defaultManifestMaxExtractedSize != 1_000_000_000 {
		t.Fatalf("defaultManifestMaxExtractedSize = %d, want 1G (10^9)", defaultManifestMaxExtractedSize)
	}
}

func TestEntryTempPathsAdapterMapsClientKeys(t *testing.T) {
	root := t.TempDir()
	adapter := entryTempPaths{root: root}
	// The argo client keys cache paths on this JSON document
	// (util/oci/client.go:339-345 getCachedPath).
	key, err := json.Marshal(map[string]string{"url": "127.0.0.1:5000/org/app", "version": "sha256:" + strings.Repeat("ab", 32)})
	if err != nil {
		t.Fatal(err)
	}
	entry := EntryPath(root, "oci://127.0.0.1:5000/org/app", "sha256:"+strings.Repeat("ab", 32))
	got, err := adapter.GetPath(string(key))
	if err != nil {
		t.Fatalf("GetPath() error = %v", err)
	}
	// No committed tar yet: the client must be handed the staging path (and
	// never a leftover partial), so a fetch write cannot land at image.tar
	// directly.
	if want := stagingImageTarPath(entry); got != want {
		t.Fatalf("GetPath() = %q, want staging path %q before a committed tar", got, want)
	}
	if info, err := os.Stat(entry); err != nil || !info.IsDir() {
		t.Fatalf("entry dir not pre-created: %v", err)
	}
	if adapter.GetPathIfExists(string(key)) != "" {
		t.Fatal("GetPathIfExists() should be empty before the tar is committed")
	}
	if err := os.WriteFile(ImageTarPath(entry), []byte("tar"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = adapter.GetPath(string(key))
	if err != nil {
		t.Fatalf("GetPath() error = %v", err)
	}
	if want := ImageTarPath(entry); got != want {
		t.Fatalf("GetPath() = %q, want committed tar %q once present", got, want)
	}
	if adapter.GetPathIfExists(string(key)) != ImageTarPath(entry) {
		t.Fatal("GetPathIfExists() should return the tar path once committed")
	}
}

func TestEntryKeyIsCacheKeyShaped(t *testing.T) {
	key := EntryKey("oci://ghcr.io/org/app", "sha256:"+strings.Repeat("ab", 32))
	if len(key) != 64 {
		t.Fatalf("EntryKey length = %d, want 64", len(key))
	}
	if key != EntryKey("OCI://ghcr.io/org/app/", "sha256:"+strings.Repeat("ab", 32)) {
		t.Fatal("EntryKey must be spelling-stable across oci:// URL variants")
	}
}

// TestResolveAndExtractRealWrapper exercises the production argo-client
// wrapper end-to-end against the hermetic registry. The nil-EventHandlers
// panic class is invisible to fakes, so this coverage is mandatory.
func TestResolveAndExtractRealWrapper(t *testing.T) {
	reg := ocitest.StartRegistry(t)
	pushedDigest := ocitest.PushHelmChartArtifact(t, reg, "charts/demo", "1.2.3", ocitest.HelmChartSpec{Name: "demo", Version: "1.2.3"})
	repoURL := reg.RepoURL("charts/demo")
	opts := Options{CacheDir: t.TempDir()}
	acquirer := DefaultAcquirer{}

	digest, err := acquirer.Resolve(t.Context(), repoURL, "1.2.3", opts)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if digest != pushedDigest {
		t.Fatalf("Resolve() = %q, want %q", digest, pushedDigest)
	}

	acquiredCalls := 0
	fromCache := false
	opts.OnAcquired = func(fromImageCache bool) {
		acquiredCalls++
		fromCache = fromImageCache
	}
	dir, release, err := acquirer.Extract(t.Context(), repoURL, digest, opts)
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	defer release()
	if acquiredCalls != 1 || fromCache {
		t.Fatalf("OnAcquired calls = %d fromCache = %v, want 1 false", acquiredCalls, fromCache)
	}
	// The helm chart tgz holds exactly one top-level chart dir whose contents
	// land at the extraction root, so `path: .` lands on the chart root.
	if _, err := os.Stat(filepath.Join(dir, "Chart.yaml")); err != nil {
		t.Fatalf("Chart.yaml not at extraction root: %v", err)
	}

	entry := EntryPath(opts.CacheDir, repoURL, digest)
	if _, err := os.Stat(ImageTarPath(entry)); err != nil {
		t.Fatalf("image tar not cached: %v", err)
	}
	metadata, err := cache.ReadMetadata(entry, cache.SourceOCI, EntryKind, filepath.Base(entry))
	if err != nil || metadata == nil {
		t.Fatalf("entry metadata = %v, %v; want valid metadata", metadata, err)
	}
	if metadata.Revision != digest {
		t.Fatalf("metadata revision = %q, want %q", metadata.Revision, digest)
	}

	// Second extract: image-cache hit.
	dir2, release2, err := acquirer.Extract(t.Context(), repoURL, digest, opts)
	if err != nil {
		t.Fatalf("second Extract() error = %v", err)
	}
	defer release2()
	if acquiredCalls != 2 || !fromCache {
		t.Fatalf("second Extract OnAcquired calls = %d fromCache = %v, want 2 true", acquiredCalls, fromCache)
	}
	if _, err := os.Stat(filepath.Join(dir2, "Chart.yaml")); err != nil {
		t.Fatalf("cached-extract Chart.yaml missing: %v", err)
	}
}

func TestExtractReleaseRemovesExtractionDir(t *testing.T) {
	reg := ocitest.StartRegistry(t)
	digest := ocitest.PushPlainManifestsArtifact(t, reg, "manifests/app", "v1", map[string]string{
		"configmap.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: plain\n",
	})
	repoURL := reg.RepoURL("manifests/app")
	opts := Options{CacheDir: t.TempDir()}
	dir, release, err := DefaultAcquirer{}.Extract(t.Context(), repoURL, digest, opts)
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "configmap.yaml")); err != nil {
		t.Fatalf("plain manifest missing at extraction root: %v", err)
	}
	release()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("release did not remove extraction dir: %v", err)
	}
}

func TestResolveSemverConstraint(t *testing.T) {
	reg := ocitest.StartRegistry(t)
	ocitest.PushHelmChartArtifact(t, reg, "charts/semver", "1.2.3", ocitest.HelmChartSpec{Version: "1.2.3"})
	want := ocitest.PushHelmChartArtifact(t, reg, "charts/semver", "1.2.4", ocitest.HelmChartSpec{Version: "1.2.4"})
	ocitest.PushHelmChartArtifact(t, reg, "charts/semver", "2.0.0", ocitest.HelmChartSpec{Version: "2.0.0"})
	// Build-metadata tags are pushed with "_" and converted back to "+"
	// during tag listing (util/oci/client.go:440-444); MaxVersion must parse
	// them without failing the whole constraint.
	ocitest.PushHelmChartArtifact(t, reg, "charts/semver", "1.1.0_build.7", ocitest.HelmChartSpec{Version: "1.1.0"})
	repoURL := reg.RepoURL("charts/semver")
	opts := Options{CacheDir: t.TempDir()}

	digest, err := DefaultAcquirer{}.Resolve(t.Context(), repoURL, "~1.2", opts)
	if err != nil {
		t.Fatalf("Resolve(~1.2) error = %v", err)
	}
	if digest != want {
		t.Fatalf("Resolve(~1.2) = %q, want %q (1.2.4)", digest, want)
	}
}

func TestResolveMissingTagFails(t *testing.T) {
	reg := ocitest.StartRegistry(t)
	ocitest.PushHelmChartArtifact(t, reg, "charts/missing", "1.0.0", ocitest.HelmChartSpec{})
	repoURL := reg.RepoURL("charts/missing")
	opts := Options{CacheDir: t.TempDir()}

	if _, err := (DefaultAcquirer{}).Resolve(t.Context(), repoURL, "9.9.9", opts); err == nil {
		t.Fatal("Resolve(missing tag) should fail")
	} else if !strings.Contains(err.Error(), "9.9.9") {
		t.Fatalf("Resolve(missing tag) error %q should name the revision", err)
	}
}

func TestExtractRejectsDisallowedMediaType(t *testing.T) {
	reg := ocitest.StartRegistry(t)
	digest := ocitest.PushDisallowedMediaTypeArtifact(t, reg, "blocked/app", "v1")
	repoURL := reg.RepoURL("blocked/app")
	opts := Options{CacheDir: t.TempDir()}

	_, _, err := DefaultAcquirer{}.Extract(t.Context(), repoURL, digest, opts)
	if err == nil {
		t.Fatal("Extract() should reject disallowed media types")
	}
	if !strings.Contains(err.Error(), "not in the list of allowed media types") {
		t.Fatalf("Extract() error = %q, want allowlist rejection", err)
	}
}

func TestExtractRejectsMultipleContentLayers(t *testing.T) {
	reg := ocitest.StartRegistry(t)
	digest := ocitest.PushMultiContentLayerArtifact(t, reg, "multi/app", "v1")
	repoURL := reg.RepoURL("multi/app")
	opts := Options{CacheDir: t.TempDir()}

	_, _, err := DefaultAcquirer{}.Extract(t.Context(), repoURL, digest, opts)
	if err == nil {
		t.Fatal("Extract() should reject multiple content layers")
	}
	if !strings.Contains(err.Error(), "expected only a single oci content layer") {
		t.Fatalf("Extract() error = %q, want single-content-layer rejection", err)
	}
}

// TestOfflineResolution phase-splits online and offline: the online phase
// populates records and the image cache, then the registry is closed (network
// refused) and a fresh acquirer value must serve digests, recorded tags, and
// recorded constraints from disk.
func TestOfflineResolution(t *testing.T) {
	reg := ocitest.StartRegistry(t)
	tagDigest := ocitest.PushHelmChartArtifact(t, reg, "charts/offline", "1.2.3", ocitest.HelmChartSpec{Version: "1.2.3"})
	repoURL := reg.RepoURL("charts/offline")
	cacheDir := t.TempDir()
	online := Options{CacheDir: cacheDir}

	acquirer := DefaultAcquirer{}
	if _, err := acquirer.Resolve(t.Context(), repoURL, "1.2.3", online); err != nil {
		t.Fatalf("online Resolve(tag) error = %v", err)
	}
	if _, err := acquirer.Resolve(t.Context(), repoURL, "~1.2", online); err != nil {
		t.Fatalf("online Resolve(constraint) error = %v", err)
	}
	if _, release, err := acquirer.Extract(t.Context(), repoURL, tagDigest, online); err != nil {
		t.Fatalf("online Extract() error = %v", err)
	} else {
		release()
	}

	// Network refused from here on.
	reg.Server.Close()
	offline := Options{CacheDir: cacheDir, Offline: true}
	offlineAcquirer := DefaultAcquirer{}

	assertOfflineResolves(t, offlineAcquirer, repoURL, tagDigest, offline)

	dir, release, err := offlineAcquirer.Extract(t.Context(), repoURL, tagDigest, offline)
	if err != nil {
		t.Fatalf("offline Extract(cached digest) error = %v", err)
	}
	defer release()
	if _, err := os.Stat(filepath.Join(dir, "Chart.yaml")); err != nil {
		t.Fatalf("offline extract content missing: %v", err)
	}

	missingDigest := "sha256:" + strings.Repeat("12", 32)
	if _, _, err := offlineAcquirer.Extract(t.Context(), repoURL, missingDigest, offline); err == nil {
		t.Fatal("offline Extract(uncached digest) should fail")
	} else if !strings.Contains(err.Error(), "offline cache miss") {
		t.Fatalf("offline Extract(uncached digest) error = %q, want offline cache miss contract text", err)
	}
}

func assertOfflineResolves(t *testing.T, acquirer DefaultAcquirer, repoURL, tagDigest string, offline Options) {
	t.Helper()
	if digest, err := acquirer.Resolve(t.Context(), repoURL, tagDigest, offline); err != nil || digest != tagDigest {
		t.Fatalf("offline Resolve(digest) = %q, %v; want passthrough", digest, err)
	}
	if digest, err := acquirer.Resolve(t.Context(), repoURL, "1.2.3", offline); err != nil || digest != tagDigest {
		t.Fatalf("offline Resolve(recorded tag) = %q, %v; want %q", digest, err, tagDigest)
	}
	if digest, err := acquirer.Resolve(t.Context(), repoURL, "~1.2", offline); err != nil || digest != tagDigest {
		t.Fatalf("offline Resolve(recorded constraint) = %q, %v; want %q", digest, err, tagDigest)
	}
	if _, err := acquirer.Resolve(t.Context(), repoURL, "4.5.6", offline); err == nil {
		t.Fatal("offline Resolve(unseen tag) should fail")
	} else if !strings.Contains(err.Error(), "offline cache miss") {
		t.Fatalf("offline Resolve(unseen tag) error = %q, want offline cache miss contract text", err)
	}
}

func TestOfflineFirstRunFails(t *testing.T) {
	opts := Options{CacheDir: t.TempDir(), Offline: true}
	if _, err := (DefaultAcquirer{}).Resolve(t.Context(), "oci://127.0.0.1:1/none/app", "1.0.0", opts); err == nil {
		t.Fatal("first-run offline Resolve should fail")
	} else if !strings.Contains(err.Error(), "offline cache miss") {
		t.Fatalf("first-run offline Resolve error = %q, want offline cache miss contract text", err)
	}
}

// TestExtractSelfHealsCorruptCachedImageTar pins the self-heal contract: a
// pre-existing truncated/corrupt image.tar must not poison online runs — the
// tar is dropped and re-fetched once, and the healed tar serves offline runs.
func TestExtractSelfHealsCorruptCachedImageTar(t *testing.T) {
	reg := ocitest.StartRegistry(t)
	digest := ocitest.PushHelmChartArtifact(t, reg, "charts/heal", "1.2.3", ocitest.HelmChartSpec{Name: "heal", Version: "1.2.3"})
	repoURL := reg.RepoURL("charts/heal")
	opts := Options{CacheDir: t.TempDir()}
	entry := EntryPath(opts.CacheDir, repoURL, digest)
	if err := os.MkdirAll(entry, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ImageTarPath(entry), []byte("truncated-tar-write"), 0o600); err != nil {
		t.Fatal(err)
	}

	fromCache := true
	opts.OnAcquired = func(fromImageCache bool) { fromCache = fromImageCache }
	dir, release, err := DefaultAcquirer{}.Extract(t.Context(), repoURL, digest, opts)
	if err != nil {
		t.Fatalf("online Extract() with corrupt cached tar error = %v, want self-heal re-fetch", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "Chart.yaml")); err != nil {
		t.Fatalf("self-healed extract content missing: %v", err)
	}
	release()
	if fromCache {
		t.Fatal("OnAcquired reported fromImageCache = true for a self-heal re-fetch")
	}

	// The healed tar is committed: offline extraction now works.
	reg.Server.Close()
	opts.OnAcquired = nil
	opts.Offline = true
	dir, release, err = DefaultAcquirer{}.Extract(t.Context(), repoURL, digest, opts)
	if err != nil {
		t.Fatalf("offline Extract() after self-heal error = %v", err)
	}
	defer release()
	if _, err := os.Stat(filepath.Join(dir, "Chart.yaml")); err != nil {
		t.Fatalf("offline extract content missing after self-heal: %v", err)
	}
}

// Offline, a corrupt cached tar cannot re-fetch: the error must be clear and
// the tar must be left in place for a later online run to heal.
func TestOfflineExtractCorruptCachedImageTarFails(t *testing.T) {
	repoURL := "oci://127.0.0.1:1/none/app"
	digest := "sha256:" + strings.Repeat("ab", 32)
	opts := Options{CacheDir: t.TempDir(), Offline: true}
	entry := EntryPath(opts.CacheDir, repoURL, digest)
	if err := os.MkdirAll(entry, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ImageTarPath(entry), []byte("truncated-tar-write"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, _, err := DefaultAcquirer{}.Extract(t.Context(), repoURL, digest, opts)
	if err == nil {
		t.Fatal("offline Extract() with corrupt cached tar should fail")
	}
	if !strings.Contains(err.Error(), "extract OCI artifact "+RedactURL(repoURL)) {
		t.Fatalf("offline Extract() error = %q, want clear extract error naming the artifact", err)
	}
	if _, statErr := os.Stat(ImageTarPath(entry)); statErr != nil {
		t.Fatalf("offline failure must leave the cached tar for a later online heal: %v", statErr)
	}
}

// TestExtractFailureDoesNotCommitImageTar pins tar-write atomicity: an
// extraction that fails after the client fetched and saved the image (corrupt
// content layer) must leave neither a committed image.tar nor a staged
// partial behind.
func TestExtractFailureDoesNotCommitImageTar(t *testing.T) {
	reg := ocitest.StartRegistry(t)
	digest := ocitest.PushCorruptContentLayerArtifact(t, reg, "corrupt/app", "v1")
	repoURL := reg.RepoURL("corrupt/app")
	opts := Options{CacheDir: t.TempDir()}
	entry := EntryPath(opts.CacheDir, repoURL, digest)

	if _, _, err := (DefaultAcquirer{}).Extract(t.Context(), repoURL, digest, opts); err == nil {
		t.Fatal("Extract() of a corrupt content layer should fail")
	}
	if _, err := os.Stat(ImageTarPath(entry)); !os.IsNotExist(err) {
		t.Fatalf("failed extraction committed image.tar: %v", err)
	}
	if _, err := os.Stat(stagingImageTarPath(entry)); !os.IsNotExist(err) {
		t.Fatalf("failed extraction left a staged partial behind: %v", err)
	}
}

// TestOfflineRecordsAccumulateAcrossTags pins record merging: different tags
// of one repository resolved online in one run (two apps pinning different
// tags) must all resolve offline, and the recorded tag list must survive
// later exact-tag resolves so other constraints still resolve via MaxVersion.
func TestOfflineRecordsAccumulateAcrossTags(t *testing.T) {
	reg := ocitest.StartRegistry(t)
	firstDigest := ocitest.PushHelmChartArtifact(t, reg, "charts/multi", "1.0.0", ocitest.HelmChartSpec{Version: "1.0.0"})
	secondDigest := ocitest.PushHelmChartArtifact(t, reg, "charts/multi", "2.0.0", ocitest.HelmChartSpec{Version: "2.0.0"})
	repoURL := reg.RepoURL("charts/multi")
	opts := Options{CacheDir: t.TempDir()}
	acquirer := DefaultAcquirer{}

	// Order matters: the constraint resolve records the tag list, then two
	// exact resolves follow — whole-record overwrites would drop both the
	// first tag's digest and the tag list.
	if _, err := acquirer.Resolve(t.Context(), repoURL, "~1.0", opts); err != nil {
		t.Fatalf("online Resolve(~1.0) error = %v", err)
	}
	if _, err := acquirer.Resolve(t.Context(), repoURL, "1.0.0", opts); err != nil {
		t.Fatalf("online Resolve(1.0.0) error = %v", err)
	}
	if _, err := acquirer.Resolve(t.Context(), repoURL, "2.0.0", opts); err != nil {
		t.Fatalf("online Resolve(2.0.0) error = %v", err)
	}

	reg.Server.Close()
	opts.Offline = true
	if digest, err := acquirer.Resolve(t.Context(), repoURL, "1.0.0", opts); err != nil || digest != firstDigest {
		t.Fatalf("offline Resolve(1.0.0) = %q, %v; want %q (sibling tag record dropped)", digest, err, firstDigest)
	}
	if digest, err := acquirer.Resolve(t.Context(), repoURL, "2.0.0", opts); err != nil || digest != secondDigest {
		t.Fatalf("offline Resolve(2.0.0) = %q, %v; want %q", digest, err, secondDigest)
	}
	// "^2.0" was never resolved online: it needs the preserved tag list plus
	// the merged winner digest.
	if digest, err := acquirer.Resolve(t.Context(), repoURL, "^2.0", opts); err != nil || digest != secondDigest {
		t.Fatalf("offline Resolve(^2.0) = %q, %v; want %q (tag list dropped by a later exact resolve)", digest, err, secondDigest)
	}
}

// TestResolveLiteralConstraintShapedTag pins argo-cd v3.4.5 resolution order:
// a literal tag that parses as a semver constraint ("1.x") resolves to its
// own digest via the exact lookup, never to the constraint's MaxVersion
// winner — online and offline.
func TestResolveLiteralConstraintShapedTag(t *testing.T) {
	reg := ocitest.StartRegistry(t)
	literalDigest := ocitest.PushHelmChartArtifact(t, reg, "charts/literal", "1.x", ocitest.HelmChartSpec{Version: "1.0.0"})
	winnerDigest := ocitest.PushHelmChartArtifact(t, reg, "charts/literal", "1.9.9", ocitest.HelmChartSpec{Version: "1.9.9"})
	if literalDigest == winnerDigest {
		t.Fatal("fixture digests must differ")
	}
	repoURL := reg.RepoURL("charts/literal")
	opts := Options{CacheDir: t.TempDir()}
	acquirer := DefaultAcquirer{}

	digest, err := acquirer.Resolve(t.Context(), repoURL, "1.x", opts)
	if err != nil {
		t.Fatalf("Resolve(1.x) error = %v", err)
	}
	if digest != literalDigest {
		t.Fatalf("Resolve(1.x) = %q, want the literal tag's digest %q, not MaxVersion winner %q", digest, literalDigest, winnerDigest)
	}

	reg.Server.Close()
	opts.Offline = true
	if digest, err := acquirer.Resolve(t.Context(), repoURL, "1.x", opts); err != nil || digest != literalDigest {
		t.Fatalf("offline Resolve(1.x) = %q, %v; want exact-first record hit %q", digest, err, literalDigest)
	}
}

// TestRedactURLStripsUserinfo pins credential redaction (remote.RedactURL
// parity): userinfo never survives into error/cache-event URL forms.
func TestRedactURLStripsUserinfo(t *testing.T) {
	for _, testCase := range []struct{ input, want string }{
		{"oci://user:secret@ghcr.io/org/app", "oci://ghcr.io/org/app"},
		{"oci://token@ghcr.io/org/app", "oci://ghcr.io/org/app"},
		{"oci://ghcr.io/org/app", "oci://ghcr.io/org/app"},
	} {
		if got := RedactURL(testCase.input); got != testCase.want {
			t.Fatalf("RedactURL(%q) = %q, want %q", testCase.input, got, testCase.want)
		}
	}
}

// Cache metadata goes through RedactURL: credentials embedded in a repoURL
// spelling never land in metadata.json.
func TestEntryMetadataTargetRedactsUserinfo(t *testing.T) {
	entry := filepath.Join(t.TempDir(), strings.Repeat("ab", 32))
	if err := os.MkdirAll(entry, 0o755); err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("cd", 32)
	writeEntryMetadata(entry, "oci://user:secret@ghcr.io/org/app", digest)
	metadata, err := cache.ReadMetadata(entry, cache.SourceOCI, EntryKind, filepath.Base(entry))
	if err != nil || metadata == nil {
		t.Fatalf("metadata = %v, %v; want valid metadata", metadata, err)
	}
	if strings.Contains(metadata.Target, "secret") || strings.Contains(metadata.Target, "user") {
		t.Fatalf("metadata target %q leaks URL userinfo", metadata.Target)
	}
	if metadata.Target != "oci://ghcr.io/org/app" {
		t.Fatalf("metadata target = %q, want redacted URL", metadata.Target)
	}
}

// TestIsLoopbackURLPlainHTTPBoundary pins the plain-HTTP boundary: loopback
// registries (the Docker localhost convention, hermetic fixtures) use plain
// HTTP; every other host — including private-range IPs — stays TLS-only.
func TestIsLoopbackURLPlainHTTPBoundary(t *testing.T) {
	for _, testCase := range []struct {
		url  string
		want bool
	}{
		{"oci://localhost/org/app", true},
		{"oci://localhost:5000/org/app", true},
		{"oci://127.0.0.1:5000/org/app", true},
		{"oci://[::1]:5000/org/app", true},
		{"oci://ghcr.io/org/app", false},
		{"oci://10.0.0.1:5000/org/app", false},
	} {
		if got := isLoopbackURL(testCase.url); got != testCase.want {
			t.Fatalf("isLoopbackURL(%q) = %v, want %v", testCase.url, got, testCase.want)
		}
	}
}

// TestRecordsPathOutsideEntryContract pins that offline records live outside
// the 64-hex entry namespace, so the cache lister never reports them.
func TestRecordsPathOutsideEntryContract(t *testing.T) {
	cacheDir := t.TempDir()
	writeTagRecord(cacheDir, "oci://ghcr.io/org/app", tagRecord{Digests: map[string]string{"v1": "sha256:" + strings.Repeat("ab", 32)}})
	entries, err := cache.List(cache.Options{OCICacheDir: cacheDir, Sources: []cache.Source{cache.SourceOCI}})
	if err != nil {
		t.Fatalf("cache.List() error = %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("cache.List() = %d entries, want 0 (records are not entries)", len(entries))
	}
	record, ok := readTagRecord(cacheDir, "oci://ghcr.io/org/app")
	if !ok || record.Digests["v1"] == "" {
		t.Fatalf("record round-trip failed: %v %v", record, ok)
	}
}
