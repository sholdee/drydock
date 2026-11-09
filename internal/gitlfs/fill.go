package gitlfs

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
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
	// BatchTimeout bounds one batch request, DownloadTimeout one object.
	BatchTimeout    time.Duration
	DownloadTimeout time.Duration
	// MaxBatchResponseBytes is the largest batch response that Fill reads.
	MaxBatchResponseBytes int64
}

func (l Limits) withDefaults() Limits {
	if l.MaxObjectBytes <= 0 {
		l.MaxObjectBytes = DefaultMaxObjectBytes
	}
	if l.MaxTotalBytes <= 0 {
		l.MaxTotalBytes = DefaultMaxTotalBytes
	}
	if l.BatchSize <= 0 {
		l.BatchSize = DefaultBatchSize
	}
	if l.Concurrency <= 0 {
		l.Concurrency = DefaultConcurrency
	}
	if l.BatchTimeout <= 0 {
		l.BatchTimeout = DefaultBatchTimeout
	}
	if l.DownloadTimeout <= 0 {
		l.DownloadTimeout = DefaultDownloadTimeout
	}
	if l.MaxBatchResponseBytes <= 0 {
		l.MaxBatchResponseBytes = DefaultMaxBatchResponseBytes
	}
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
	// Endpoint is the Git LFS API endpoint (Endpoint), or empty with
	// EndpointErr set when the repository has none that drydock can use.
	Endpoint    string
	EndpointErr error
	// Credentials are the --git-* credentials. Fill sends the HTTP
	// credentials to the batch endpoint only, and only when a Hosts entry
	// names its scheme, host, and port.
	Credentials gitauth.Credentials
	// Transport replaces the default HTTP transport. Only tests set it.
	Transport http.RoundTripper
	Limits    Limits
}

// Result is what Fill did.
type Result struct {
	// Missing are the paths of the pointers that stay pointers, sorted.
	Missing []string
	// Reasons say why, at most a few distinct ones, for a warning. They
	// never hold a credential, a header, or a URL path or query.
	Reasons []string
	// Downloaded counts the objects that Fill downloaded, or that another
	// Fill of the process downloaded while this one waited for it.
	Downloaded int
}

// StoreDir is the Git LFS store of the drydock Git cache at gitCacheDir.
func StoreDir(gitCacheDir string) string {
	return filepath.Join(gitCacheDir, "lfs")
}

// maxReasons bounds Result.Reasons.
const maxReasons = 3

// Fill puts, at the path of each pointer below root, the content of its
// object, as `git lfs checkout` does. It reads the object from
// opts.LocalStores, then from the drydock store, then, with the network,
// downloads it into the drydock store. Every object must match the size
// and SHA-256 of its pointer. A pointer that Fill cannot fill stays as it
// is and goes to Result.Missing with a reason. Fill fails only for a local
// problem, such as an unsafe pointer path or a write error.
func Fill(ctx context.Context, root string, pointers []gittree.LFSPointer, opts Options) (Result, error) {
	var result Result
	sources := slices.Clone(opts.LocalStores)
	if opts.CacheDir != "" {
		sources = append(sources, opts.CacheDir)
	}
	pending, err := fillFromStores(root, pointers, sources, &result)
	if err != nil || len(pending) == 0 {
		return result.finish(), err
	}
	if err := downloadPending(ctx, pending, opts, &result); err != nil {
		return result.finish(), err
	}
	if result.Downloaded > 0 {
		pending, err = fillFromStores(root, pending, []string{opts.CacheDir}, nil)
	}
	for _, pointer := range pending {
		result.Missing = append(result.Missing, pointer.Path)
	}
	return result.finish(), err
}

// fillFromStores fills each pointer from the first store that holds its
// object and returns the pointers that no store holds. A pointer that uses
// Git LFS extensions goes to result as missing.
func fillFromStores(root string, pointers []gittree.LFSPointer, stores []string, result *Result) ([]gittree.LFSPointer, error) {
	var pending []gittree.LFSPointer
	for _, pointer := range pointers {
		if pointer.Extensions && result != nil {
			result.Missing = append(result.Missing, pointer.Path)
			result.addReason("the pointer uses Git LFS extensions, whose content drydock does not rebuild")
			continue
		}
		err := gittree.ReplaceLFSPointer(root, pointer, stores)
		switch {
		case errors.Is(err, gittree.ErrLFSObjectMissing):
			pending = append(pending, pointer)
		case err != nil:
			return nil, err
		}
	}
	return pending, nil
}

// downloadPending downloads the objects of pending into the drydock store.
// It records a reason for each object it does not download.
func downloadPending(ctx context.Context, pending []gittree.LFSPointer, opts Options, result *Result) error {
	endpoint, reason := networkEndpoint(opts)
	if reason != "" {
		result.addReason(reason)
		return nil
	}
	c := newClient(opts)
	wanted := c.selectObjects(pending, result)
	for start := 0; start < len(wanted); start += c.limits.BatchSize {
		if err := ctx.Err(); err != nil {
			return err
		}
		chunk := wanted[start:min(start+c.limits.BatchSize, len(wanted))]
		c.fetchChunk(ctx, endpoint, chunk, result)
	}
	return ctx.Err()
}

// networkEndpoint returns the batch endpoint of opts, or the reason why
// Fill does not use the network.
func networkEndpoint(opts Options) (*url.URL, string) {
	switch {
	case opts.CacheDir == "":
		return nil, "drydock has no Git LFS cache directory"
	case !opts.AllowNetwork:
		return nil, "--offline is set, and neither the local Git LFS store nor the drydock Git LFS cache holds the objects"
	case opts.EndpointErr != nil:
		return nil, opts.EndpointErr.Error()
	}
	endpoint, err := url.Parse(opts.Endpoint)
	if err != nil || endpoint.Host == "" {
		return nil, "the Git LFS endpoint is not a valid URL"
	}
	return endpoint, ""
}

// client is the network side of one Fill.
type client struct {
	transport   http.RoundTripper
	credentials gitauth.Credentials
	limits      Limits
	store       store
}

func newClient(opts Options) *client {
	transport := opts.Transport
	if transport == nil {
		transport = defaultTransport
	}
	return &client{
		transport:   transport,
		credentials: opts.Credentials,
		limits:      opts.Limits.withDefaults(),
		store:       store{dir: opts.CacheDir},
	}
}

// defaultTransport bounds the connection, the TLS handshake, and the wait
// for response headers. It reads the proxy from the environment, as git
// and go-git do.
var defaultTransport http.RoundTripper = &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
	ForceAttemptHTTP2:     true,
	MaxIdleConns:          100,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   10 * time.Second,
	ResponseHeaderTimeout: time.Minute,
	ExpectContinueTimeout: time.Second,
}

// selectObjects lists the objects of pending once each, in path order, and
// keeps those within the size limits. The others get a reason.
func (c *client) selectObjects(pending []gittree.LFSPointer, result *Result) []objectRef {
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
		if pointer.Size > c.limits.MaxObjectBytes {
			result.addReason(fmt.Sprintf("an object is larger than the limit of %d bytes for one Git LFS object", c.limits.MaxObjectBytes))
			continue
		}
		if total+pointer.Size > c.limits.MaxTotalBytes {
			result.addReason(fmt.Sprintf("the objects are larger than the limit of %d bytes of Git LFS downloads for one tree", c.limits.MaxTotalBytes))
			continue
		}
		total += pointer.Size
		out = append(out, objectRef{OID: pointer.OID, Size: pointer.Size})
	}
	return out
}

// fetchChunk runs one batch request and downloads its objects, at most
// limits.Concurrency at a time.
func (c *client) fetchChunk(ctx context.Context, endpoint *url.URL, chunk []objectRef, result *Result) {
	objects, err := c.batch(ctx, endpoint.String(), chunk)
	if err != nil {
		result.addReason(c.redact(err.Error()))
		return
	}
	var mu sync.Mutex
	record := func(err error) {
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			result.addReason(c.redact(err.Error()))
			return
		}
		result.Downloaded++
	}
	limit := make(chan struct{}, c.limits.Concurrency)
	var wg sync.WaitGroup
	for _, want := range chunk {
		object, ok := objects[want.OID]
		if !ok {
			record(errors.New("the batch response does not list an object, or lists it with another size"))
			continue
		}
		wg.Go(func() {
			limit <- struct{}{}
			defer func() { <-limit }()
			record(c.fetchObject(ctx, endpoint, want, object))
		})
	}
	wg.Wait()
}

// fetchObject downloads one object of a batch response, once per process
// for an object ID: a second Fill that needs the same object waits, then
// finds it in the store.
func (c *client) fetchObject(ctx context.Context, endpoint *url.URL, want objectRef, object batchObject) error {
	if object.Error != nil {
		return fmt.Errorf("the Git LFS server reports error %d for an object: %s", object.Error.Code, sanitize(object.Error.Message))
	}
	action, ok := object.Actions["download"]
	if !ok {
		return errors.New("the batch response gives no download action for an object")
	}
	unlock := objectLocks.lock(c.store.dir, want.OID)
	defer unlock()
	if c.store.has(want) {
		return nil
	}
	return c.download(ctx, endpoint, want, action)
}

// redact removes the --git-* secret values from a message, should a server
// echo one.
func (c *client) redact(message string) string {
	for _, secret := range []string{c.credentials.Password, c.credentials.BearerToken} {
		if secret = strings.TrimSpace(secret); secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	return message
}

func (r *Result) addReason(reason string) {
	if slices.Contains(r.Reasons, reason) || len(r.Reasons) >= maxReasons {
		return
	}
	r.Reasons = append(r.Reasons, reason)
}

func (r Result) finish() Result {
	sort.Strings(r.Missing)
	switch {
	case len(r.Missing) == 0:
		r.Reasons = nil
	case len(r.Reasons) == 0:
		r.Reasons = []string{"no Git LFS store holds an object that matches the pointer"}
	}
	return r
}
