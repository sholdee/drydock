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
	"slices"
	"strings"

	argocache "github.com/argoproj/argo-cd/v3/util/cache"
	utilio "github.com/argoproj/argo-cd/v3/util/io"
	"github.com/argoproj/argo-cd/v3/util/oci"
	"github.com/argoproj/argo-cd/v3/util/versions"
	"github.com/argoproj/pkg/sync"
	"github.com/sholdee/drydock/internal/cache"
	"github.com/sholdee/drydock/internal/pathsafety"
)

// EntryKind is the cache metadata kind recorded for OCI artifact image
// entries (source "oci", kind "image").
const EntryKind = "image"

// imageTarName is the committed per-entry tar file. The argo client
// os.Create()s whatever path the TempPaths seam hands it (util/oci/client.go
// :521-530 createTarFile via saveCompressedImageToPath), so fresh fetches are
// staged at stagingImageTarPath and renamed here only after a fully
// successful Extract — a committed image.tar is always a complete tar.
const imageTarName = "image.tar"

// Options carries per-call acquisition inputs, mirroring chart.Options.
type Options struct {
	CacheDir       string
	Offline        bool
	ForbiddenRoots []string
	Credentials    Credentials
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
// 373,410), so no-op handlers are mandatory. Resolve injects a drydock-owned
// prefetch-populated tags memo via WithIndexCache (see prefetchedTagsCache) —
// the upstream SetOCITags write path stores an empty read buffer
// (util/oci/client.go:450-452) and is deliberately ignored.
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

// RedactURL returns the URL form safe for errors and cache events: URL
// userinfo is dropped (mirroring remote.RedactURL), so credentials embedded
// in a repoURL spelling never reach error text or cache metadata.
func RedactURL(repoURL string) string {
	normalized := NormalizeURL(repoURL)
	parsed, err := url.Parse("oci://" + normalized)
	if err != nil {
		return "[invalid-url]"
	}
	// Fail closed when an "@" (or a percent escape that could encode one)
	// survives parsing without being classified as userinfo: slash-shifted
	// credentials (oci://us/er:secret@host/...) parse with a nil User, so
	// stripping User alone would return the secret verbatim.
	if parsed.User == nil && strings.ContainsAny(normalized, "@%") {
		return "[invalid-url]"
	}
	parsed.User = nil
	return parsed.String()
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

// stagingImageTarPath is where the argo client writes a fresh fetch before
// extractAttempt commits it. The name is deterministic, so the client's
// per-path lock still serializes concurrent fetches of one entry.
func stagingImageTarPath(entryPath string) string {
	return filepath.Join(entryPath, "."+imageTarName+".partial")
}

// entryTempPaths adapts the drydock OCI cache layout to the argo client's
// TempPaths seam. The client keys paths on the JSON document
// {"url":…,"version":…} (util/oci/client.go:339-345 getCachedPath); the
// adapter maps that key deterministically to the entry's committed
// <entry>/image.tar when one exists, and to the entry's staging path
// otherwise, with the entry directory pre-created so the client's os.Create
// succeeds. Handing out the staging path is what makes the committed tar
// atomic: the client fetches into it and extractAttempt renames it into place
// only after a fully successful Extract.
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
	final := ImageTarPath(entry)
	if _, err := os.Stat(final); err == nil {
		return final, nil
	}
	// No committed tar: hand out the staging path, clearing any partial an
	// interrupted earlier run left behind so the client's existence check
	// never mistakes it for a complete tar.
	staging := stagingImageTarPath(entry)
	_ = os.Remove(staging)
	return staging, nil
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

// rejectURLUserinfo rejects URL-carried credentials for OCI sources before
// any client construction. The check is a raw whole-URL "@" scan (total —
// no url.Parse dependency): an authority-only scan is bypassable because an
// unencoded "/" inside embedded credentials shifts the "@" past the first
// path cut (oci://user:pa/ss@host/... or oci://us/er:secret@host/...), and
// the same shapes defeat url.Parse-based redaction. OCI repository names
// cannot contain "@" and drydock carries digests in targetRevision, so no
// legitimate repoURL contains one. Nothing functional is lost: oras rejects
// userinfo URLs anyway (oras-go registry/reference.go:196) — but its error
// echoes the RAW registry component, and the vendored client logs c.repoURL
// through logrus on tag fetches (client.go:446-451), so the secret must
// never reach either sink. The rejection error carries only the redacted
// URL and names the remediation for both mistake shapes.
func rejectURLUserinfo(repoURL string) error {
	// "%" is rejected alongside "@": NormalizeURL does not percent-decode,
	// so a %40-encoded "@" would slip past an @-only scan and the secret
	// would flow readably into the oras invalid-registry echo. OCI registry
	// hosts and repository names can never legitimately contain either
	// character.
	normalized := NormalizeURL(repoURL)
	if !strings.ContainsAny(normalized, "@%") {
		return nil
	}
	return fmt.Errorf("OCI artifact %s: %q and %q are not allowed in an OCI repository URL; put digests in targetRevision and credentials in --oci-username/--oci-password", RedactURL(repoURL), "@", "%")
}

func (a DefaultAcquirer) newClient(repoURL string, cacheDir string, credentials Credentials, extraOpts ...oci.ClientOpts) (oci.Client, error) {
	creds, err := clientCreds(repoURL, credentials)
	if err != nil {
		return nil, err
	}
	clientOpts := append([]oci.ClientOpts{
		oci.WithEventHandlers(noopEventHandlers()),
		oci.WithImagePaths(entryTempPaths{root: cacheDir}),
		oci.WithManifestMaxExtractedSize(defaultManifestMaxExtractedSize),
	}, extraOpts...)
	return oci.NewClientWithLock("oci://"+NormalizeURL(repoURL), creds, ociKeyLock, "", "", defaultLayerMediaTypes(), clientOpts...)
}

// prefetchedTagsCache is the drydock-owned per-resolve tags memo injected
// via WithIndexCache so one constraint resolve fetches /tags/list exactly
// once: resolveOnline populates it from drydock's OWN GetTags result before
// ResolveRevision runs, and ResolveRevision's constraint fallback then reads
// it back through the client's tags cache. Two hard rules: upstream
// SetOCITags hands over an empty read buffer (util/oci/client.go:450-452) so
// writes are ignored, and the memo must NEVER lazy-fill — getTags holds the
// non-reentrant argoproj KeyLock indexLock across cache callbacks
// (client.go:421-423), so a miss that called back into the client would
// self-deadlock.
type prefetchedTagsCache struct {
	data []byte
}

func (c *prefetchedTagsCache) populate(tags []string) {
	data, err := json.Marshal(tags)
	if err != nil {
		return
	}
	c.data = data
}

// SetOCITags ignores upstream writes (empty buffer; see type comment).
func (c *prefetchedTagsCache) SetOCITags(string, []byte) error { return nil }

func (c *prefetchedTagsCache) GetOCITags(_ string, indexData *[]byte) error {
	if len(c.data) == 0 {
		return argocache.ErrCacheMiss
	}
	*indexData = append([]byte(nil), c.data...)
	return nil
}

// Resolve resolves revision to a digest. Digest-pinned revisions pass through
// without network. Online, resolution mirrors argo-cd v3.4.5
// util/oci/client.go:384-407 exactly: the literal revision resolves first via
// a registry HEAD, and only when that fails does a constraint-shaped revision
// fall back to MaxVersion over the tag list. Offline, resolution is
// seam-level from records captured on earlier online runs, in the same
// exact-first order; the argo client is never constructed.
func (a DefaultAcquirer) Resolve(ctx context.Context, repoURL, revision string, opts Options) (string, error) {
	if err := rejectURLUserinfo(repoURL); err != nil {
		return "", err
	}
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
	memo := &prefetchedTagsCache{}
	client, err := a.newClient(repoURL, cacheDir, opts.Credentials, oci.WithIndexCache(memo))
	if err != nil {
		// Defense-in-depth: construction errors surface with the redacted URL
		// even though userinfo was already rejected above.
		return "", fmt.Errorf("initialize OCI client for %s: %w", RedactURL(repoURL), err)
	}
	return resolveOnline(ctx, client, memo, cacheDir, repoURL, revision)
}

func resolveOnline(ctx context.Context, client oci.Client, memo *prefetchedTagsCache, cacheDir, repoURL, revision string) (string, error) {
	// Constraint-shaped revisions prefetch the tag list ONCE and seed the
	// memo before ResolveRevision runs: the record refresh below and
	// ResolveRevision's constraint fallback (reading the memo through the
	// client's tags cache, noCache=false) then share a single /tags/list
	// fetch. The memo is populated from this GetTags result only — never
	// lazily (see prefetchedTagsCache). A failed prefetch is not fatal:
	// ResolveRevision's own fallback re-fetches (a legitimate double fetch
	// on this failure path) and resolution errors surface there.
	var tags []string
	tagsFetched := false
	if versions.IsConstraint(revision) {
		if fetched, tagsErr := client.GetTags(ctx, true); tagsErr == nil {
			tags = fetched
			tagsFetched = true
			memo.populate(fetched)
		}
	}
	// Match the argo-cd v3.4.5 resolution order exactly: ResolveRevision
	// resolves the literal revision first and only falls back to MaxVersion
	// over the tag list when the exact lookup fails and the revision parses
	// as a semver constraint (util/oci/client.go:384-407 resolveRevision).
	// Dispatching on IsConstraint before the exact lookup would resolve a
	// literal constraint-shaped tag like "1.x" to the constraint winner
	// instead of the tag's own digest.
	digest, err := client.ResolveRevision(ctx, revision, false)
	if err != nil {
		return "", fmt.Errorf("resolve OCI artifact %s revision %q: %w", RedactURL(repoURL), revision, err)
	}
	if !versions.IsConstraint(revision) {
		updateTagRecord(cacheDir, repoURL, nil, false, map[string]string{revision: digest})
		return digest, nil
	}
	// Constraint-shaped revisions also refresh the recorded tag list so other
	// constraints keep resolving offline through MaxVersion. When the digest
	// came from the constraint fallback (the literal revision is not a tag),
	// it is recorded under the winning version too, so offline MaxVersion
	// lookups land on it.
	if tagsFetched {
		digests := map[string]string{revision: digest}
		if !slices.Contains(tags, revision) {
			if version, versionErr := versions.MaxVersion(revision, tags); versionErr == nil {
				digests[version] = digest
			}
		}
		updateTagRecord(cacheDir, repoURL, tags, true, digests)
		return digest, nil
	}
	// A failed tag-list prefetch after a successful resolve records the
	// digest alone: resolution itself already succeeded.
	updateTagRecord(cacheDir, repoURL, nil, false, map[string]string{revision: digest})
	return digest, nil
}

func resolveOffline(cacheDir, repoURL, revision string) (string, error) {
	record, ok := readTagRecord(cacheDir, repoURL)
	if !ok {
		return "", offlineResolveMiss(repoURL, revision)
	}
	// Exact-first mirrors the online resolution order: a digest recorded for
	// the literal revision wins even when the revision parses as a constraint.
	if digest, ok := record.Digests[revision]; ok {
		return digest, nil
	}
	if !versions.IsConstraint(revision) {
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

// Extract materializes the artifact content for digest. With a committed
// image tar cached, the argo client extracts locally without network; offline
// without a cached tar it fails with the offline cache miss contract error
// instead of letting the client reach the registry. Online, a cached tar that
// fails to extract (truncated by an interrupted earlier run or corrupted
// externally) is deleted and re-fetched once, so a bad tar never poisons
// every future run.
func (a DefaultAcquirer) Extract(ctx context.Context, repoURL, digest string, opts Options) (string, func(), error) {
	if err := rejectURLUserinfo(repoURL); err != nil {
		return "", nil, err
	}
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
	client, err := a.newClient(repoURL, cacheDir, opts.Credentials)
	if err != nil {
		// Defense-in-depth: construction errors surface with the redacted URL
		// even though userinfo was already rejected above.
		return "", nil, fmt.Errorf("initialize OCI client for %s: %w", RedactURL(repoURL), err)
	}
	dir, closer, err := extractAttempt(ctx, client, entry, digest)
	if err != nil && fromImageCache && !opts.Offline {
		// Self-heal: drop the unusable cached tar and re-fetch once.
		_ = os.Remove(ImageTarPath(entry))
		fromImageCache = false
		dir, closer, err = extractAttempt(ctx, client, entry, digest)
	}
	if err != nil {
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

// extractAttempt runs one client extraction and commits the staged tar on
// success: fresh fetches land at the staging path (entryTempPaths.GetPath)
// and only the rename after a fully successful Extract publishes image.tar,
// so a crash or failure can never leave a partial committed tar. Failed
// attempts discard their partial along with the error-path extraction dir.
func extractAttempt(ctx context.Context, client oci.Client, entry, digest string) (string, utilio.Closer, error) {
	dir, closer, err := client.Extract(ctx, digest)
	staging := stagingImageTarPath(entry)
	if err != nil {
		if dir != "" {
			_ = os.RemoveAll(dir)
		}
		_ = os.Remove(staging)
		return "", nil, err
	}
	if _, statErr := os.Stat(staging); statErr == nil {
		// Best-effort commit: a failed rename only costs a re-fetch next run.
		_ = os.Rename(staging, ImageTarPath(entry))
	}
	return dir, closer, nil
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
