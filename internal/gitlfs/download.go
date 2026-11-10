package gitlfs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sholdee/drydock/internal/credhost"
)

// Limits on the headers that a batch response may give for one action.
const (
	maxActionHeaders     = 32
	maxActionHeaderBytes = 16 << 10
)

// download fetches the object of want from the download action of the
// batch response into the store, after it makes sure that its size and
// SHA-256 match. The request carries only the headers of the action:
// never the --git-* credentials. It refuses a plain-http href when the
// batch endpoint is https. A body that sends nothing for
// limits.IdleTimeout stops the download.
func (c *client) download(ctx context.Context, batchURL *url.URL, want objectRef, action batchAction) error {
	href, err := url.Parse(strings.TrimSpace(action.Href))
	if err != nil || href.Host == "" || (href.Scheme != "http" && href.Scheme != "https") {
		return errors.New("the batch response gives no valid http or https download URL")
	}
	if batchURL.Scheme == "https" && href.Scheme == "http" {
		return unsupportedf("the batch response points the download at plain http (%s), which drydock refuses for an https Git LFS server", hostOf(href))
	}
	if err := checkActionHeaders(action.Header); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, c.limits.DownloadTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, href.String(), nil)
	if err != nil {
		return errors.New("the batch response gives no valid download URL")
	}
	for name, value := range action.Header {
		request.Header.Set(name, value)
	}
	httpClient := &http.Client{Transport: c.transport, CheckRedirect: actionRedirectPolicy}
	response, err := httpClient.Do(request)
	if err != nil {
		return classifyTransport(fmt.Errorf("the download from %s failed: %s", hostOf(href), transportError(err)), err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("the download from %s answered HTTP %d", hostOf(href), response.StatusCode)
	}
	body := newIdleReader(response.Body, c.limits.IdleTimeout, cancel)
	defer body.stop()
	if err := c.store.put(want, body); err != nil {
		if body.stalled() {
			return fmt.Errorf("the download from %s sent nothing for %s", hostOf(href), c.limits.IdleTimeout)
		}
		return err
	}
	return nil
}

// classifyTransport returns err as an unsupported reason when cause is a
// connection that drydock refused, else err.
func classifyTransport(err, cause error) error {
	if errors.Is(cause, errRefusedAddress) || IsUnsupported(cause) {
		return &unsupportedError{message: err.Error()}
	}
	return err
}

// idleReader cancels a download when its body sends nothing for idle.
type idleReader struct {
	reader io.Reader
	idle   time.Duration
	timer  *time.Timer
	fired  atomic.Bool
}

func newIdleReader(reader io.Reader, idle time.Duration, cancel context.CancelFunc) *idleReader {
	r := &idleReader{reader: reader, idle: idle}
	r.timer = time.AfterFunc(idle, func() {
		r.fired.Store(true)
		cancel()
	})
	return r
}

func (r *idleReader) Read(data []byte) (int, error) {
	n, err := r.reader.Read(data)
	if n > 0 {
		r.timer.Reset(r.idle)
	}
	return n, err
}

func (r *idleReader) stop() { r.timer.Stop() }

func (r *idleReader) stalled() bool { return r.fired.Load() }

// actionRedirectPolicy follows a redirect of an object download. When the
// redirect leaves the scheme, host, or port of the requests before it, the
// request loses every header of the batch action, not only Authorization,
// because the server gave them for the href alone. A redirect from https
// to http fails. At most 10 redirects are followed.
func actionRedirectPolicy(request *http.Request, via []*http.Request) error {
	if len(via) >= credhost.MaxRedirects {
		return fmt.Errorf("stopped after %d redirects", credhost.MaxRedirects)
	}
	for _, hop := range via {
		if hop.URL.Scheme == "https" && request.URL.Scheme != "https" {
			return unsupportedf("a redirect leads from https to plain http, which drydock refuses")
		}
	}
	if !credhost.SameOrigin(request, via) {
		request.Header = http.Header{}
	}
	return nil
}

// checkActionHeaders bounds the headers of an action. net/http refuses an
// invalid header name or value itself.
func checkActionHeaders(header map[string]string) error {
	if len(header) > maxActionHeaders {
		return unsupportedf("the batch response gives more than %d headers for a download", maxActionHeaders)
	}
	total := 0
	for name, value := range header {
		total += len(name) + len(value)
	}
	if total > maxActionHeaderBytes {
		return unsupportedf("the batch response gives more than %d bytes of headers for a download", maxActionHeaderBytes)
	}
	return nil
}

// hostOf returns the scheme and host of target for a message. It never
// returns the path or the query, which can hold a signed token.
func hostOf(target *url.URL) string {
	if target == nil {
		return "[invalid-url]"
	}
	return target.Scheme + "://" + target.Host
}

// transportError returns the text of err without the request URL, which
// can hold a signed token in its query.
func transportError(err error) string {
	if urlErr, ok := errors.AsType[*url.Error](err); ok {
		err = urlErr.Err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timed out"
	}
	return sanitize(err.Error())
}

// store is the content-addressed Git LFS object store of the drydock Git
// cache: objects/<oid[0:2]>/<oid[2:4]>/<oid>, as in a .git/lfs directory.
type store struct {
	dir string
}

// put writes the object of want from body into the store: into a
// temporary file of the store first, which moves into place only when its
// size and SHA-256 match want. It reads at most want.Size+1 bytes.
func (s store) put(want objectRef, body io.Reader) error {
	if !validOID(want.OID) {
		return errors.New("invalid Git LFS object ID")
	}
	tmpDir := filepath.Join(s.dir, "tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return fmt.Errorf("create the Git LFS cache: %w", err)
	}
	tmp, err := os.CreateTemp(tmpDir, want.OID+".*.tmp")
	if err != nil {
		return fmt.Errorf("create a temporary Git LFS cache file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	sum := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(tmp, sum), io.LimitReader(body, want.Size+1))
	closeErr := tmp.Close()
	switch {
	case copyErr != nil:
		return fmt.Errorf("the download stopped: %s", transportError(copyErr))
	case closeErr != nil:
		return closeErr
	case written != want.Size:
		return errors.New("the size of the download does not match the pointer")
	case hex.EncodeToString(sum.Sum(nil)) != want.OID:
		return errors.New("the SHA-256 of the download does not match the pointer")
	}
	target := s.objectPath(want.OID)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create the Git LFS cache: %w", err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		return fmt.Errorf("publish the Git LFS cache object: %w", err)
	}
	return nil
}

func (s store) objectPath(oid string) string {
	return filepath.Join(s.dir, "objects", oid[0:2], oid[2:4], oid)
}

// validOID reports whether oid is a SHA-256 in lower-case hex, the only
// form that names a path in the store.
func validOID(oid string) bool {
	if len(oid) != sha256.Size*2 {
		return false
	}
	for _, r := range oid {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
