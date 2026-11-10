package gitlfs

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sholdee/drydock/internal/gitauth"
	"github.com/sholdee/drydock/internal/gittree"
)

// Defaults of Limits. git-lfs sends at most 100 objects in one batch
// request (lfs.transfer.batchSize) and runs 8 transfers at once
// (lfs.concurrenttransfers).
const (
	DefaultMaxObjectBytes        int64 = 100 << 20
	DefaultMaxTotalBytes         int64 = 512 << 20
	DefaultBatchSize                   = 100
	DefaultConcurrency                 = 8
	DefaultBatchTimeout                = 2 * time.Minute
	DefaultDownloadTimeout             = 10 * time.Minute
	DefaultFillTimeout                 = 15 * time.Minute
	DefaultIdleTimeout                 = time.Minute
	DefaultMaxBatchResponseBytes int64 = 10 << 20
)

// Limits bound the network work of one Fill. A zero field takes its
// default.
type Limits struct {
	// MaxObjectBytes is the largest object that Fill downloads.
	MaxObjectBytes int64
	// MaxTotalBytes is the most that Fill downloads for one tree.
	MaxTotalBytes int64
	// BatchSize is the most objects of one batch request.
	BatchSize int
	// Concurrency is the most downloads at the same time.
	Concurrency int
	// BatchTimeout bounds one batch request, DownloadTimeout one object,
	// and FillTimeout all the network work of one Fill.
	BatchTimeout    time.Duration
	DownloadTimeout time.Duration
	FillTimeout     time.Duration
	// IdleTimeout stops a download whose body sends nothing for so long.
	IdleTimeout time.Duration
	// MaxBatchResponseBytes is the largest batch response that Fill reads.
	MaxBatchResponseBytes int64
}

func (l Limits) withDefaults() Limits {
	positive := func(value, fallback int64) int64 {
		if value <= 0 {
			return fallback
		}
		return value
	}
	duration := func(value, fallback time.Duration) time.Duration {
		if value <= 0 {
			return fallback
		}
		return value
	}
	l.MaxObjectBytes = positive(l.MaxObjectBytes, DefaultMaxObjectBytes)
	l.MaxTotalBytes = positive(l.MaxTotalBytes, DefaultMaxTotalBytes)
	l.MaxBatchResponseBytes = positive(l.MaxBatchResponseBytes, DefaultMaxBatchResponseBytes)
	l.BatchSize = int(positive(int64(l.BatchSize), DefaultBatchSize))
	l.Concurrency = int(positive(int64(l.Concurrency), DefaultConcurrency))
	l.BatchTimeout = duration(l.BatchTimeout, DefaultBatchTimeout)
	l.DownloadTimeout = duration(l.DownloadTimeout, DefaultDownloadTimeout)
	l.FillTimeout = duration(l.FillTimeout, DefaultFillTimeout)
	l.IdleTimeout = duration(l.IdleTimeout, DefaultIdleTimeout)
	return l
}

// Options say where Fill finds the objects of a tree.
type Options struct {
	// LocalStores are .git/lfs directories that Fill reads first, such as
	// the stores of the local repository of a --ref snapshot. Fill never
	// writes them.
	LocalStores []string
	// CacheDir is the drydock Git LFS store (StoreDir), which Fill reads
	// next and downloads into. Empty turns the store and the network off.
	CacheDir string
	// AllowNetwork lets Fill download what the stores lack. --offline
	// turns it off.
	AllowNetwork bool
	// Endpoint is the Git LFS endpoint (FindEndpoint), unless EndpointErr
	// says why the repository has none.
	Endpoint    Endpoint
	EndpointErr error
	// Credentials are the --git-* credentials. Fill sends the HTTP
	// credentials to the batch endpoint only, and only when a Hosts entry
	// names its scheme, host, and port.
	Credentials gitauth.Credentials
	// SSH is the SSH side of an SSH remote (git-lfs-authenticate).
	SSH SSHOptions
	// TLSConfig replaces the default TLS configuration. Only tests set it.
	TLSConfig *tls.Config
	Limits    Limits
}

// ErrFetch reports that the Git LFS server did not give drydock an object,
// as `git lfs fetch` fails in the Argo CD repo-server.
var ErrFetch = errors.New("git LFS fetch failed")

// Result is what Fill did.
type Result struct {
	// Missing are the paths of the pointers that stay pointers because
	// drydock had no network, sorted, and Reasons say why.
	Missing []string
	Reasons []string
	// Unsupported are the paths of the pointers that stay pointers because
	// drydock does not support them, sorted, and UnsupportedReasons say
	// why. No reason holds a credential, a header, or a URL path or query.
	Unsupported        []string
	UnsupportedReasons []string
	// Downloaded counts the objects that Fill downloaded itself.
	Downloaded int
}

// Pointers are Missing and Unsupported, sorted: every pointer that stays.
func (r Result) Pointers() []string {
	out := append(slices.Clone(r.Missing), r.Unsupported...)
	sort.Strings(out)
	return out
}

// StoreDir is the Git LFS store of the drydock Git cache at gitCacheDir.
func StoreDir(gitCacheDir string) string {
	return filepath.Join(gitCacheDir, "lfs")
}

// maxReasons bounds the reasons of a Result and of an ErrFetch error.
const maxReasons = 3

// Fill puts, at the path of each pointer below root, the content of its
// object, as `git lfs checkout` does. It reads the object from
// opts.LocalStores, then from the drydock store, then, with the network,
// downloads it into the drydock store. Every object must match the size
// and SHA-256 of its pointer.
//
// A pointer stays as it is in Result.Missing when the network is off, and
// in Result.Unsupported when drydock does not support it, such as a size
// over a limit or an endpoint that drydock refuses. When the server fails
// to give an object, Fill fails with ErrFetch, as `git lfs fetch` fails
// the manifest request of the repo-server. It also fails for a local
// problem, such as an unsafe pointer path or a write error.
func Fill(ctx context.Context, root string, pointers []gittree.LFSPointer, opts Options) (Result, error) {
	f := &filler{root: root, opts: opts, outcomes: map[string]outcome{}}
	pending, err := f.fillFromStores(pointers, f.stores())
	if err != nil || len(pending) == 0 {
		return f.result.finish(), err
	}
	if reason, unsupported, ok := networkOff(opts); !ok {
		f.leave(pending, reason, unsupported)
		return f.result.finish(), nil
	}
	if err := f.download(ctx, pending); err != nil {
		return f.result.finish(), err
	}
	pending, err = f.fillFromStores(pending, []string{opts.CacheDir})
	if err != nil {
		return f.result.finish(), err
	}
	return f.settle(pending)
}

// filler is the state of one Fill.
type filler struct {
	root     string
	opts     Options
	result   Result
	mu       sync.Mutex
	outcomes map[string]outcome
	failures []string
}

// outcome is what the network gave for one object: nothing, an
// unsupported reason, or a failure.
type outcome struct {
	unsupported string
	failure     string
}

func (f *filler) stores() []string {
	stores := slices.Clone(f.opts.LocalStores)
	if f.opts.CacheDir != "" {
		stores = append(stores, f.opts.CacheDir)
	}
	return stores
}

// fillFromStores fills each pointer from the first store that holds its
// object and returns the pointers that no store holds. A pointer that uses
// Git LFS extensions is unsupported. An object of the drydock store gets a
// new modification time, which the cache commands read as its last use.
func (f *filler) fillFromStores(pointers []gittree.LFSPointer, stores []string) ([]gittree.LFSPointer, error) {
	var pending []gittree.LFSPointer
	for _, pointer := range pointers {
		if pointer.Extensions {
			f.result.Unsupported = append(f.result.Unsupported, pointer.Path)
			f.result.UnsupportedReasons = addReason(f.result.UnsupportedReasons, "the pointer uses Git LFS extensions, whose content drydock does not rebuild")
			continue
		}
		used, err := gittree.ReplaceLFSPointerFrom(f.root, pointer, stores)
		switch {
		case errors.Is(err, gittree.ErrLFSObjectMissing):
			pending = append(pending, pointer)
		case err != nil:
			return nil, err
		case used != "" && used == f.opts.CacheDir:
			now := time.Now()
			_ = os.Chtimes(gittree.LFSObjectPath(used, pointer.OID), now, now)
		}
	}
	return pending, nil
}

// networkOff returns why Fill does not use the network, and ok when it
// does. unsupported reports a reason where drydock is the limit.
func networkOff(opts Options) (string, bool, bool) {
	switch {
	case !opts.AllowNetwork:
		return "--offline is set, and neither the local Git LFS store nor the drydock Git LFS cache holds the objects", false, false
	case opts.CacheDir == "":
		return "drydock has no Git LFS cache directory", true, false
	}
	return "", false, true
}

// leave keeps every pending pointer with reason.
func (f *filler) leave(pending []gittree.LFSPointer, reason string, unsupported bool) {
	for _, pointer := range pending {
		if unsupported {
			f.result.Unsupported = append(f.result.Unsupported, pointer.Path)
		} else {
			f.result.Missing = append(f.result.Missing, pointer.Path)
		}
	}
	if unsupported {
		f.result.UnsupportedReasons = addReason(f.result.UnsupportedReasons, reason)
	} else {
		f.result.Reasons = addReason(f.result.Reasons, reason)
	}
}

// download downloads the objects of pending into the drydock store within
// limits.FillTimeout, and records the outcome of each object.
func (f *filler) download(ctx context.Context, pending []gittree.LFSPointer) error {
	if f.opts.EndpointErr != nil {
		f.recordAll(pending, f.opts.EndpointErr)
		return nil
	}
	c := newClient(f.opts)
	ctx, cancel := context.WithTimeout(ctx, c.limits.FillTimeout)
	defer cancel()
	wanted := f.selectObjects(c.limits, pending)
	for start := 0; start < len(wanted); start += c.limits.BatchSize {
		if ctx.Err() != nil {
			break
		}
		f.fetchChunk(ctx, c, wanted[start:min(start+c.limits.BatchSize, len(wanted))])
	}
	if ctx.Err() != nil && parentDone(ctx) {
		return ctx.Err()
	}
	if ctx.Err() != nil {
		f.failures = addReason(f.failures, fmt.Sprintf("the Git LFS downloads took longer than %s", c.limits.FillTimeout))
	}
	return nil
}

// parentDone reports whether the caller's context ended, not only the
// deadline of the Fill.
func parentDone(ctx context.Context) bool {
	return errors.Is(context.Cause(ctx), context.Canceled)
}

// recordAll records err for the objects of every pending pointer.
func (f *filler) recordAll(pending []gittree.LFSPointer, err error) {
	for _, pointer := range pending {
		f.record(pointer.OID, err)
	}
}

// record records the outcome of an object: err nil is no outcome.
func (f *filler) record(oid string, err error) {
	if err == nil {
		return
	}
	message := redact(err.Error(), f.opts.Credentials)
	f.mu.Lock()
	defer f.mu.Unlock()
	if IsUnsupported(err) {
		f.outcomes[oid] = outcome{unsupported: message}
		return
	}
	f.outcomes[oid] = outcome{failure: message}
	f.failures = addReason(f.failures, message)
}

// selectObjects lists the objects of pending once each, in path order, and
// keeps those within the size limits. The others are unsupported.
func (f *filler) selectObjects(limits Limits, pending []gittree.LFSPointer) []objectRef {
	sorted := slices.Clone(pending)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	seen := map[string]bool{}
	var out []objectRef
	var total int64
	for _, pointer := range sorted {
		if seen[pointer.OID] {
			continue
		}
		seen[pointer.OID] = true
		if pointer.Size > limits.MaxObjectBytes {
			f.record(pointer.OID, unsupportedf("an object is larger than the limit of %d bytes for one Git LFS object", limits.MaxObjectBytes))
			continue
		}
		if total+pointer.Size > limits.MaxTotalBytes {
			f.record(pointer.OID, unsupportedf("the objects are larger than the limit of %d bytes of Git LFS downloads for one tree", limits.MaxTotalBytes))
			continue
		}
		total += pointer.Size
		out = append(out, objectRef{OID: pointer.OID, Size: pointer.Size})
	}
	return out
}

// fetchChunk runs one batch request and downloads its objects, at most
// limits.Concurrency at a time.
func (f *filler) fetchChunk(ctx context.Context, c *client, chunk []objectRef) {
	objects, batchURL, err := c.batch(ctx, chunk)
	if err != nil {
		for _, want := range chunk {
			f.record(want.OID, err)
		}
		return
	}
	limit := make(chan struct{}, c.limits.Concurrency)
	var wg sync.WaitGroup
	for _, want := range chunk {
		object, ok := objects[want.OID]
		if !ok {
			f.record(want.OID, errors.New("the batch response does not list an object, or lists it with another size"))
			continue
		}
		wg.Go(func() {
			limit <- struct{}{}
			defer func() { <-limit }()
			downloaded, err := c.fetchObject(ctx, batchURL, want, object)
			f.record(want.OID, err)
			if downloaded {
				f.mu.Lock()
				f.result.Downloaded++
				f.mu.Unlock()
			}
		})
	}
	wg.Wait()
}

// settle sorts the pointers that the stores still lack after the
// downloads: unsupported, or failed. A failure fails the Fill.
func (f *filler) settle(pending []gittree.LFSPointer) (Result, error) {
	for _, pointer := range pending {
		result := f.outcomes[pointer.OID]
		switch {
		case result.unsupported != "":
			f.result.Unsupported = append(f.result.Unsupported, pointer.Path)
			f.result.UnsupportedReasons = addReason(f.result.UnsupportedReasons, result.unsupported)
		case result.failure == "":
			f.failures = addReason(f.failures, "no Git LFS store holds an object that matches the pointer")
		}
	}
	if len(f.failures) > 0 {
		return f.result.finish(), fmt.Errorf("%w: %s", ErrFetch, strings.Join(f.failures, "; "))
	}
	return f.result.finish(), nil
}

// client is the network side of one Fill.
type client struct {
	transport   http.RoundTripper
	credentials gitauth.Credentials
	endpoint    Endpoint
	ssh         SSHOptions
	guard       *dialGuard
	limits      Limits
	store       store
}

func newClient(opts Options) *client {
	guard := newDialGuard(opts.Endpoint)
	return &client{
		transport:   newTransport(guard, opts.TLSConfig),
		credentials: opts.Credentials,
		endpoint:    opts.Endpoint,
		ssh:         opts.SSH,
		guard:       guard,
		limits:      opts.Limits.withDefaults(),
		store:       store{dir: opts.CacheDir},
	}
}

// newTransport bounds the connection, the TLS handshake, and the wait for
// response headers, and dials through guard. It reads the proxy from the
// environment, as git and go-git do. Each Fill has its own transport, so
// no connection outlives the guard of its Fill.
func newTransport(guard *dialGuard, tlsConfig *tls.Config) *http.Transport {
	return &http.Transport{
		Proxy:                 guard.proxy,
		DialContext:           guard.dialContext,
		TLSClientConfig:       tlsConfig,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: time.Minute,
		ExpectContinueTimeout: time.Second,
	}
}

// fetchObject downloads one object of a batch response, once per process
// for an object ID: a second Fill that needs the same object waits, then
// finds it in the store. downloaded reports a download of its own.
func (c *client) fetchObject(ctx context.Context, batchURL *url.URL, want objectRef, object batchObject) (bool, error) {
	if object.Error != nil {
		return false, fmt.Errorf("the Git LFS server reports error %d for an object: %s", object.Error.Code, sanitize(object.Error.Message))
	}
	action, ok := object.Actions["download"]
	if !ok {
		return false, errors.New("the batch response gives no download action for an object")
	}
	unlock := objectLocks.lock(c.store.dir, want.OID)
	defer unlock()
	if c.store.has(want) {
		return false, nil
	}
	err := c.download(ctx, batchURL, want, action)
	return err == nil, err
}

// redact removes the --git-* secret values from a message, should a server
// echo one.
func redact(message string, credentials gitauth.Credentials) string {
	for _, secret := range []string{credentials.Password, credentials.BearerToken} {
		if secret = strings.TrimSpace(secret); secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	return message
}

// addReason adds reason to reasons once, up to maxReasons.
func addReason(reasons []string, reason string) []string {
	if slices.Contains(reasons, reason) || len(reasons) >= maxReasons {
		return reasons
	}
	return append(reasons, reason)
}

func (r Result) finish() Result {
	sort.Strings(r.Missing)
	sort.Strings(r.Unsupported)
	if len(r.Missing) == 0 {
		r.Reasons = nil
	}
	if len(r.Unsupported) == 0 {
		r.UnsupportedReasons = nil
	}
	return r
}
