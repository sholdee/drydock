// Package ociartifact acquires first-class Argo CD OCI artifact sources
// (repoURL "oci://…" without chart:) by wrapping the vendored Argo CD OCI
// client. Revision resolution and extraction guards follow argo-cd v3.4.5
// util/oci/client.go; the image cache is a drydock-owned layout under the OCI
// cache root so `drydock cache` can list and prune it.
package ociartifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/argoproj/argo-cd/v3/util/oci"
	"github.com/argoproj/argo-cd/v3/util/versions"
	"github.com/argoproj/pkg/sync"
	"github.com/sholdee/drydock/internal/cache"
	"github.com/sholdee/drydock/internal/pathsafety"
)

// EntryKind is the cache metadata kind recorded for OCI artifact image
// entries (source "oci", kind "image").
const EntryKind = "image"

// imageTarName is the per-entry tar file the argo client os.Create()s
// (util/oci/client.go:521-530 createTarFile via saveCompressedImageToPath).
const imageTarName = "image.tar"

// Options carries per-call acquisition inputs, mirroring chart.Options.
type Options struct {
	CacheDir       string
	Offline        bool
	ForbiddenRoots []string
	// OnAcquired is invoked by the production acquirer exactly once per real
	// (non-memoized) successful Extract, with fromImageCache reporting whether
	// the image tar was already cached before the call. Session memo hits never
	// invoke it, which is what makes single-acquisition event assertions
	// memo-sensitive. Fakes should call it when simulating an extraction.
	OnAcquired func(fromImageCache bool)
}

// Acquirer is the OCI artifact acquisition seam. Resolve turns a tag, semver
// constraint, or digest into a concrete digest; Extract materializes the
// artifact content for that digest into a directory and returns a release
// callback for it.
type Acquirer interface {
	Resolve(ctx context.Context, repoURL, revision string, opts Options) (string, error)
	Extract(ctx context.Context, repoURL, digest string, opts Options) (string, func(), error)
}

// DefaultAcquirer wraps util/oci.NewClientWithLock. Every exported method of
// the argo client calls its embedded EventHandlers unconditionally and
// NewClientWithLock installs no defaults (util/oci/client.go:232,244,357,
// 373,410), so no-op handlers are mandatory. WithIndexCache is deliberately
// not used: upstream SetOCITags stores the empty read buffer
// (util/oci/client.go:450-452), so the tags cache never functions.
type DefaultAcquirer struct{}

// ociKeyLock is the drydock-supplied KeyLock serializing per-path image cache
// writes inside the argo client (extract locks on the cached tar path).
var ociKeyLock = sync.NewKeyLock()

// defaultLayerMediaTypes mirrors the Argo CD repo-server default for
// --oci-layer-media-types (vendored argo-cd v3.4.5
// cmd/argocd-repo-server/commands/argocd_repo_server.go:267).
func defaultLayerMediaTypes() []string {
	return []string{
		"application/vnd.oci.image.layer.v1.tar",
		"application/vnd.oci.image.layer.v1.tar+gzip",
		"application/vnd.cncf.helm.chart.content.v1.tar+gzip",
	}
}

// defaultManifestMaxExtractedSize mirrors the Argo CD repo-server default for
// --oci-manifest-max-extracted-size, "1G" (vendored argo-cd v3.4.5
// cmd/argocd-repo-server/commands/argocd_repo_server.go:262);
// resource.ParseQuantity("1G").ToDec().Value() is 10^9 bytes.
const defaultManifestMaxExtractedSize = int64(1_000_000_000)

// IsOCIURL reports whether repoURL names an OCI registry source. It is total
// over oci:// spellings: surrounding whitespace and scheme casing do not
// change classification, so no spelling can fall through to the local
// path-exists or git-acquisition branches. Argo CD matches the exact
// lowercase prefix (v1alpha1 types.go:317-319); accepting case variants only
// widens classification, never re-routes a URL argo would treat as OCI.
func IsOCIURL(repoURL string) bool {
	trimmed := strings.TrimSpace(repoURL)
	if len(trimmed) < len("oci://") {
		return false
	}
	return strings.EqualFold(trimmed[:len("oci://")], "oci://")
}

// NormalizeURL canonicalizes an OCI repository URL for cache keys and
// records: whitespace and trailing slashes trimmed, scheme dropped (the argo
// client keys its cache on the schemeless repo path, util/oci/client.go:125).
func NormalizeURL(repoURL string) string {
	trimmed := strings.TrimSpace(repoURL)
	if IsOCIURL(trimmed) {
		trimmed = trimmed[len("oci://"):]
	}
	return strings.TrimRight(trimmed, "/")
}

// digestPattern matches OCI digest references (algorithm:hex, lowercase hex
// per the OCI image spec digest grammar).
var digestPattern = regexp.MustCompile(`^[a-z0-9]+(?:[.+_-][a-z0-9]+)*:[0-9a-f]{32,}$`)

// IsDigest reports whether revision is a digest-pinned reference.
func IsDigest(revision string) bool {
	return digestPattern.MatchString(strings.TrimSpace(revision))
}

// RedactURL returns the URL form safe for errors and cache events.
func RedactURL(repoURL string) string {
	return "oci://" + NormalizeURL(repoURL)
}

func DefaultCacheDir() (string, error) {
	root, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "drydock", "oci"), nil
}

// ResolveCacheDir follows the chart cache-dir validation pattern
// (chart.ResolveCacheDir): default under the user cache dir, absolute, and
// never inside a protected root.
func ResolveCacheDir(cacheDir string, forbiddenRoots []string) (string, error) {
	if cacheDir == "" {
		defaultDir, err := DefaultCacheDir()
		if err != nil {
			return "", err
		}
		cacheDir = defaultDir
	}
	absCacheDir, err := filepath.Abs(cacheDir)
	if err != nil {
		return "", err
	}
	absCacheDir = filepath.Clean(absCacheDir)
	inside, matchedRoot, err := pathsafety.IsInsideAny(absCacheDir, forbiddenRoots)
	if err != nil {
		return "", err
	}
	if inside {
		return "", fmt.Errorf("oci cache dir %q must not be inside repository root %q", absCacheDir, matchedRoot)
	}
	return absCacheDir, nil
}

// EntryKey derives the 64-hex cache entry key from (url, version).
func EntryKey(repoURL, version string) string {
	sum := sha256.Sum256([]byte(NormalizeURL(repoURL) + "\x00" + strings.TrimSpace(version)))
	return hex.EncodeToString(sum[:])
}

// EntryPath returns the cache entry directory for (url, version).
func EntryPath(cacheDir, repoURL, version string) string {
	return cache.OCIEntryPath(cacheDir, EntryKey(repoURL, version))
}

// ImageTarPath returns the cached image tar location inside an entry.
func ImageTarPath(entryPath string) string {
	return filepath.Join(entryPath, imageTarName)
}

// entryTempPaths adapts the drydock OCI cache layout to the argo client's
// TempPaths seam. The client keys paths on the JSON document
// {"url":…,"version":…} (util/oci/client.go:339-345 getCachedPath); the
// adapter maps that key deterministically to <entry>/image.tar with the entry
// directory pre-created so the client's os.Create succeeds.
type entryTempPaths struct {
	root string
}

func (p entryTempPaths) Add(string, string) {}

func (p entryTempPaths) GetPath(key string) (string, error) {
	var parsed struct {
		URL     string `json:"url"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal([]byte(key), &parsed); err != nil {
		return "", fmt.Errorf("unexpected oci cache path key: %w", err)
	}
	entry := EntryPath(p.root, parsed.URL, parsed.Version)
	if err := os.MkdirAll(entry, 0o755); err != nil {
		return "", err
	}
	return ImageTarPath(entry), nil
}

func (p entryTempPaths) GetPathIfExists(key string) string {
	path, err := p.GetPath(key)
	if err != nil {
		return ""
	}
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return path
}

func (p entryTempPaths) GetPaths() map[string]string {
	return map[string]string{}
}

// noopEventHandlers fills every EventHandlers field: the argo client invokes
// them unconditionally on each exported method, so leaving any nil panics
// (util/oci/client.go:232,244,357,373,410).
func noopEventHandlers() oci.EventHandlers {
	handler := func(string) func() { return func() {} }
	failHandler := func(string) func(string) { return func(string) {} }
	return oci.EventHandlers{
		OnExtract:             handler,
		OnResolveRevision:     handler,
		OnDigestMetadata:      handler,
		OnTestRepo:            handler,
		OnGetTags:             handler,
		OnExtractFail:         failHandler,
		OnResolveRevisionFail: failHandler,
		OnDigestMetadataFail:  failHandler,
		OnTestRepoFail:        handler,
		OnGetTagsFail:         handler,
	}
}

// isLoopbackURL reports whether the registry host is loopback. Loopback
// registries use plain HTTP (the Docker localhost convention), which keeps
// hermetic httptest registries and local development registries working
// without a global insecure switch; non-loopback hosts always use TLS.
func isLoopbackURL(repoURL string) bool {
	parsed, err := url.Parse("oci://" + NormalizeURL(repoURL))
	if err != nil {
		return false
	}
	host := parsed.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (a DefaultAcquirer) newClient(repoURL string, cacheDir string) (oci.Client, error) {
	creds := oci.Creds{InsecureHTTPOnly: isLoopbackURL(repoURL)}
	return oci.NewClientWithLock("oci://"+NormalizeURL(repoURL), creds, ociKeyLock, "", "", defaultLayerMediaTypes(),
		oci.WithEventHandlers(noopEventHandlers()),
		oci.WithImagePaths(entryTempPaths{root: cacheDir}),
		oci.WithManifestMaxExtractedSize(defaultManifestMaxExtractedSize),
	)
}

// Resolve resolves revision to a digest. Digest-pinned revisions pass through
// without network. Online, resolution mirrors argo-cd v3.4.5
// util/oci/client.go:384-407: an exact revision resolves via a registry HEAD;
// a semver constraint resolves MaxVersion over the tag list and then the
// winning tag via HEAD. Offline, resolution is seam-level from records
// captured on earlier online runs; the argo client is never constructed.
func (a DefaultAcquirer) Resolve(ctx context.Context, repoURL, revision string, opts Options) (string, error) {
	revision = strings.TrimSpace(revision)
	if IsDigest(revision) {
		return revision, nil
	}
	cacheDir, err := ResolveCacheDir(opts.CacheDir, opts.ForbiddenRoots)
	if err != nil {
		return "", err
	}
	if opts.Offline {
		return resolveOffline(cacheDir, repoURL, revision)
	}
	client, err := a.newClient(repoURL, cacheDir)
	if err != nil {
		return "", err
	}
	return resolveOnline(ctx, client, cacheDir, repoURL, revision)
}

func resolveOnline(ctx context.Context, client oci.Client, cacheDir, repoURL, revision string) (string, error) {
	if !versions.IsConstraint(revision) {
		digest, err := client.ResolveRevision(ctx, revision, true)
		if err != nil {
			return "", fmt.Errorf("resolve OCI artifact %s revision %q: %w", RedactURL(repoURL), revision, err)
		}
		// Overwritten on every online resolve: tags never go stale, at the
		// accepted cost of dropping older recorded tags.
		writeTagRecord(cacheDir, repoURL, tagRecord{Digests: map[string]string{revision: digest}})
		return digest, nil
	}
	tags, err := client.GetTags(ctx, true)
	if err != nil {
		return "", fmt.Errorf("resolve OCI artifact %s constraint %q: %w", RedactURL(repoURL), revision, err)
	}
	version, err := versions.MaxVersion(revision, tags)
	if err != nil {
		return "", fmt.Errorf("resolve OCI artifact %s constraint %q: %w", RedactURL(repoURL), revision, err)
	}
	digest, err := client.ResolveRevision(ctx, version, true)
	if err != nil {
		return "", fmt.Errorf("resolve OCI artifact %s revision %q: %w", RedactURL(repoURL), version, err)
	}
	writeTagRecord(cacheDir, repoURL, tagRecord{Tags: tags, Digests: map[string]string{version: digest}})
	return digest, nil
}

func resolveOffline(cacheDir, repoURL, revision string) (string, error) {
	record, ok := readTagRecord(cacheDir, repoURL)
	if !ok {
		return "", offlineResolveMiss(repoURL, revision)
	}
	if !versions.IsConstraint(revision) {
		if digest, ok := record.Digests[revision]; ok {
			return digest, nil
		}
		return "", offlineResolveMiss(repoURL, revision)
	}
	version, err := versions.MaxVersion(revision, record.Tags)
	if err != nil {
		return "", offlineResolveMiss(repoURL, revision)
	}
	if digest, ok := record.Digests[version]; ok {
		return digest, nil
	}
	return "", offlineResolveMiss(repoURL, revision)
}

// offlineResolveMiss carries the literal "offline cache miss" contract string
// that cacheevent.ActionForError keys on for ActionMiss
// (internal/cacheevent/cacheevent.go:127-132).
func offlineResolveMiss(repoURL, revision string) error {
	return fmt.Errorf("offline cache miss for OCI artifact %s revision %q", RedactURL(repoURL), revision)
}

// Extract materializes the artifact content for digest. With the image tar
// cached, the argo client extracts locally without network; offline without a
// cached tar it fails with the offline cache miss contract error instead of
// letting the client reach the registry.
func (a DefaultAcquirer) Extract(ctx context.Context, repoURL, digest string, opts Options) (string, func(), error) {
	cacheDir, err := ResolveCacheDir(opts.CacheDir, opts.ForbiddenRoots)
	if err != nil {
		return "", nil, err
	}
	entry := EntryPath(cacheDir, repoURL, digest)
	fromImageCache := false
	if info, err := os.Stat(ImageTarPath(entry)); err == nil && info.Mode().IsRegular() {
		fromImageCache = true
	}
	if opts.Offline && !fromImageCache {
		return "", nil, fmt.Errorf("offline cache miss for OCI artifact %s digest %s", RedactURL(repoURL), digest)
	}
	client, err := a.newClient(repoURL, cacheDir)
	if err != nil {
		return "", nil, err
	}
	dir, closer, err := client.Extract(ctx, digest)
	if err != nil {
		if dir != "" {
			_ = os.RemoveAll(dir)
		}
		return "", nil, fmt.Errorf("extract OCI artifact %s digest %s: %w", RedactURL(repoURL), digest, err)
	}
	writeEntryMetadata(entry, repoURL, digest)
	if opts.OnAcquired != nil {
		opts.OnAcquired(fromImageCache)
	}
	release := func() {
		if closer != nil {
			_ = closer.Close()
		}
	}
	return dir, release, nil
}

// writeEntryMetadata satisfies the cache lister contract (64-hex entry dir
// plus metadata.json, internal/cache/cache.go:384,411) and refreshes
// UpdatedAt so LRU pruning tracks last use.
func writeEntryMetadata(entryPath, repoURL, digest string) {
	_ = cache.WriteMetadata(entryPath, cache.Metadata{
		Source:   cache.SourceOCI,
		Kind:     EntryKind,
		Key:      filepath.Base(entryPath),
		Target:   RedactURL(repoURL),
		Revision: digest,
	})
}
