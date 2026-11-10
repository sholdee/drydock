package gitlfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sholdee/drydock/internal/gitauth"
	"github.com/sholdee/drydock/internal/gitlfs/lfstest"
)

// TestFillDropsCredentialsOnBatchRedirectToAnotherPort pins that a batch
// server on a listed host that redirects the request with 307 to another
// port of the same IP does not pass the --git-* token on. net/http keeps
// Authorization on a redirect to the same host name on another port, so
// credhost.ScopeRedirects is what removes it.
func TestFillDropsCredentialsOnBatchRedirectToAnotherPort(t *testing.T) {
	target := lfstest.New(t, false, false)
	target.Add(testContent)
	redirector := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.Batch.URL+request.URL.Path, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirector.Close)
	c := newFillCase(t, testContent)
	opts := c.options(target, gitauth.Credentials{BearerToken: gitSecret, Hosts: []string{hostEntry(t, redirector.URL)}})
	opts.Endpoint = Endpoint{URL: redirector.URL + "/org/repo.git/info/lfs"}
	if result := c.fill(t, opts); len(result.Pointers()) != 0 {
		t.Fatalf("Fill() = %+v, want the object through the redirect", result)
	}
	second := target.RequestsTo("batch")
	if len(second) != 1 || second[0].Header.Get("Authorization") != "" {
		t.Fatalf("second hop requests = %+v, want one without Authorization", second)
	}
}

// TestFillNeverShowsHrefPathOrQuery pins that a failed download names only
// the scheme and host of the href: its path and query can hold a signed
// token.
func TestFillNeverShowsHrefPathOrQuery(t *testing.T) {
	server := lfstest.New(t, false, false)
	server.Add(testContent)
	server.Body = func(string, []byte) []byte { return nil }
	server.Mutate = func(objects []lfstest.Object) []lfstest.Object {
		for i := range objects {
			action := objects[i].Actions["download"]
			action.Href = server.Objects.URL + "/missing/secret-path?X-Signature=signed-token"
			objects[i].Actions["download"] = action
		}
		return objects
	}
	c := newFillCase(t, testContent)
	message := c.fail(t, c.options(server, gitauth.Credentials{}))
	if strings.Contains(message, "signed-token") || strings.Contains(message, "secret-path") || !strings.Contains(message, "HTTP 404") {
		t.Fatalf("error %q, want the status without the path or query", message)
	}
}

// TestFillLimitsActionHeaders pins that a download action with more than
// 32 headers is unsupported and never requested.
func TestFillLimitsActionHeaders(t *testing.T) {
	server := lfstest.New(t, false, false)
	server.Add(testContent)
	server.ActionHeader = map[string]string{}
	for i := range maxActionHeaders + 1 {
		server.ActionHeader[fmt.Sprintf("X-Header-%d", i)] = "value"
	}
	c := newFillCase(t, testContent)
	result := c.fill(t, c.options(server, gitauth.Credentials{}))
	if len(result.Unsupported) != 1 || len(server.RequestsTo("objects")) != 0 {
		t.Fatalf("Fill() = %+v, object requests = %d; want the action refused", result, len(server.RequestsTo("objects")))
	}
}

// endlessReader never ends.
type endlessReader struct{}

func (endlessReader) Read(data []byte) (int, error) {
	for i := range data {
		data[i] = 'x'
	}
	return len(data), nil
}

// TestStorePutReadsAtMostTheObjectSize pins that the store reads at most
// one byte more than the pointer names, so a server cannot fill the disk.
func TestStorePutReadsAtMostTheObjectSize(t *testing.T) {
	done := make(chan error, 1)
	go func() {
		done <- store{dir: t.TempDir()}.put(objectRef{OID: strings.Repeat("ab", 32), Size: 10}, endlessReader{})
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "size") {
			t.Fatalf("put() error = %v, want the size mismatch", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("put() read an endless body")
	}
}

// TestStorePutRefusesInvalidOID pins that only a lower-case SHA-256 names
// a path in the store.
func TestStorePutRefusesInvalidOID(t *testing.T) {
	dir := t.TempDir()
	for _, oid := range []string{"../../escape", strings.Repeat("AB", 32), strings.Repeat("ab", 31)} {
		err := store{dir: dir}.put(objectRef{OID: oid, Size: 1}, strings.NewReader("x"))
		if err == nil || !strings.Contains(err.Error(), "invalid Git LFS object ID") {
			t.Fatalf("put(%q) error = %v, want the invalid object ID", oid, err)
		}
	}
}

// TestFillLimitsTheBatchResponse pins that a batch response over the limit
// fails the fill unread.
func TestFillLimitsTheBatchResponse(t *testing.T) {
	server := lfstest.New(t, false, false)
	server.Add(testContent)
	c := newFillCase(t, testContent)
	opts := c.options(server, gitauth.Credentials{})
	opts.Limits.MaxBatchResponseBytes = 64
	if message := c.fail(t, opts); !strings.Contains(message, "larger than 64 bytes") {
		t.Fatalf("error %q, want the batch response limit", message)
	}
}

// TestFillStopsAStalledDownload pins that a download whose body sends
// nothing for the idle timeout fails, and that one Fill has one deadline
// for all its network work.
func TestFillStopsAStalledDownload(t *testing.T) {
	release := make(chan struct{})
	server := lfstest.New(t, false, false)
	server.Add(testContent)
	stall := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Length", fmt.Sprint(len(testContent)))
		_, _ = io.WriteString(writer, testContent[:3])
		if flusher, ok := writer.(http.Flusher); ok {
			flusher.Flush()
		}
		<-release
	}))
	t.Cleanup(stall.Close)
	// Cleanups run in reverse order: release the handlers before Close
	// waits for them.
	t.Cleanup(func() { close(release) })
	server.Mutate = func(objects []lfstest.Object) []lfstest.Object {
		for i := range objects {
			action := objects[i].Actions["download"]
			action.Href = stall.URL + "/object"
			objects[i].Actions["download"] = action
		}
		return objects
	}
	c := newFillCase(t, testContent)
	opts := c.options(server, gitauth.Credentials{})
	opts.Limits.IdleTimeout = 200 * time.Millisecond
	if message := c.fail(t, opts); !strings.Contains(message, "sent nothing") {
		t.Fatalf("error %q, want the idle timeout", message)
	}
	opts.Limits.IdleTimeout = time.Hour
	opts.Limits.FillTimeout = 300 * time.Millisecond
	start := time.Now()
	c.fail(t, opts)
	if time.Since(start) > 5*time.Second {
		t.Fatalf("Fill() took %s, want the Fill deadline", time.Since(start))
	}
}

// TestFillCountsOnlyItsOwnDownloads pins that two Fills of one object in
// one process download it once, and that the Fill that finds it in the
// store does not count it as a download, so the source reports no network
// use.
func TestFillCountsOnlyItsOwnDownloads(t *testing.T) {
	server := lfstest.New(t, false, false)
	server.Add(testContent)
	server.ObjectDelay = 300 * time.Millisecond
	cacheDir := filepath.Join(t.TempDir(), "lfs")
	var wg sync.WaitGroup
	results := make([]Result, 2)
	for i := range results {
		c := newFillCase(t, testContent)
		c.store = cacheDir
		wg.Go(func() { results[i] = c.fill(t, c.options(server, gitauth.Credentials{})) })
	}
	wg.Wait()
	if total := results[0].Downloaded + results[1].Downloaded; total != 1 || len(server.RequestsTo("objects")) != 1 {
		t.Fatalf("Downloaded = %d + %d, object requests = %d; want one download", results[0].Downloaded, results[1].Downloaded, len(server.RequestsTo("objects")))
	}
}

// TestFillKeepsAFileThatAlreadyMatches pins that a file that already holds
// the content of its pointer is not written again, and that a store hit
// marks the object as used for the cache commands.
func TestFillKeepsAFileThatAlreadyMatches(t *testing.T) {
	c := newFillCase(t, testContent)
	target := filepath.Join(c.root, "dir", "cm.yaml")
	writeFile(t, target, testContent)
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(target, old, old); err != nil {
		t.Fatal(err)
	}
	if result := c.fill(t, Options{CacheDir: c.store}); len(result.Pointers()) != 0 {
		t.Fatalf("Fill() = %+v, want the file kept", result)
	}
	if info, _ := os.Stat(target); !info.ModTime().Equal(old) {
		t.Fatalf("file modified at %s, want it untouched", info.ModTime())
	}

	c = newFillCase(t, testContent)
	object := (store{dir: c.store}).objectPath(c.pointer.OID)
	writeFile(t, object, testContent)
	if err := os.Chtimes(object, old, old); err != nil {
		t.Fatal(err)
	}
	c.fill(t, Options{CacheDir: c.store})
	if info, _ := os.Stat(object); !info.ModTime().After(old) {
		t.Fatal("the store object keeps its old time after a hit, want it marked as used")
	}
}

// TestDialGuardRefusesLinkLocalAndForeignLoopback pins the addresses that
// a Fill may connect to: never link-local, and loopback only for a batch
// host on loopback that the repository content did not choose.
func TestDialGuardRefusesLinkLocalAndForeignLoopback(t *testing.T) {
	loopback := newDialGuard(Endpoint{URL: "http://127.0.0.1:8080/repo.git/info/lfs", RepoHost: "127.0.0.1"})
	foreign := newDialGuard(Endpoint{URL: "https://lfs.example.test/x", RepoHost: "git.example.test"})
	chosen := newDialGuard(Endpoint{URL: "http://127.0.0.1:8080/x", FromConfig: true, RepoHost: "git.example.test"})
	for _, tc := range []struct {
		guard   *dialGuard
		address string
		allowed bool
	}{
		{loopback, "127.0.0.1:80", true},
		{loopback, "169.254.169.254:80", false},
		{loopback, "[fe80::1]:80", false},
		{foreign, "127.0.0.1:80", false},
		{foreign, "[::1]:80", false},
		{foreign, "0.0.0.0:80", false},
		{foreign, "192.0.2.10:443", true},
		{chosen, "127.0.0.1:8080", false},
		{loopback, "[fd00:ec2::254]:80", false},
		{loopback, "100.100.100.200:80", false},
		{loopback, "[64:ff9b::a9fe:a9fe]:80", false},
		{foreign, "[64:ff9b::7f00:1]:80", false},
		{foreign, "[64:ff9b::c000:20a]:443", true},
		{foreign, "100.100.100.201:443", true},
	} {
		err := tc.guard.control("tcp", tc.address, nil)
		if (err == nil) != tc.allowed {
			t.Errorf("control(%s) with loopback allowed %t = %v, want allowed %t", tc.address, tc.guard.allowLoopback, err, tc.allowed)
		}
	}
}

// TestFillRefusesAddressesThatTheGuardRefuses pins that an href to a
// link-local address and a .lfsconfig batch URL on loopback for a
// repository elsewhere leave the pointer unsupported without a connection.
func TestFillRefusesAddressesThatTheGuardRefuses(t *testing.T) {
	server := lfstest.New(t, false, false)
	server.Add(testContent)
	server.Mutate = func(objects []lfstest.Object) []lfstest.Object {
		for i := range objects {
			action := objects[i].Actions["download"]
			action.Href = "http://169.254.169.254/latest/meta-data"
			objects[i].Actions["download"] = action
		}
		return objects
	}
	c := newFillCase(t, testContent)
	result := c.fill(t, c.options(server, gitauth.Credentials{}))
	if len(result.Unsupported) != 1 || !strings.Contains(strings.Join(result.UnsupportedReasons, ";"), "link-local") {
		t.Fatalf("Fill(link-local href) = %+v, want it unsupported", result)
	}

	other := lfstest.New(t, false, false)
	other.Add(testContent)
	c = newFillCase(t, testContent)
	opts := c.options(other, gitauth.Credentials{})
	opts.Endpoint = Endpoint{URL: other.Batch.URL + "/lfs", FromConfig: true, RepoHost: "git.example.test"}
	result = c.fill(t, opts)
	if len(result.Unsupported) != 1 || len(other.Requests()) != 0 || !strings.Contains(strings.Join(result.UnsupportedReasons, ";"), "loopback") {
		t.Fatalf("Fill(.lfsconfig on loopback) = %+v, requests %d; want it refused", result, len(other.Requests()))
	}
}

// TestProxyAddressUsesTheDefaultPorts pins the proxy address that the
// guard lets through unchecked.
func TestProxyAddressUsesTheDefaultPorts(t *testing.T) {
	for raw, want := range map[string]string{
		"http://proxy.example.test":       net.JoinHostPort("proxy.example.test", "80"),
		"https://proxy.example.test":      net.JoinHostPort("proxy.example.test", "443"),
		"socks5://proxy.example.test":     net.JoinHostPort("proxy.example.test", "1080"),
		"http://proxy.example.test:3128/": net.JoinHostPort("proxy.example.test", "3128"),
	} {
		parsed, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := proxyAddress(parsed); got != want {
			t.Errorf("proxyAddress(%s) = %s, want %s", raw, got, want)
		}
	}
}

// TestDialGuardChecksProxiedTargets pins the check of a proxied request:
// an IP address or localhost that the guard refuses fails, and a name that
// does not resolve here goes to the proxy, which is then the policy point.
func TestDialGuardChecksProxiedTargets(t *testing.T) {
	guard := newDialGuard(Endpoint{URL: "https://lfs.example.test/x", RepoHost: "git.example.test"})
	guard.resolver = &net.Resolver{PreferGo: true, Dial: func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("no DNS here")
	}}
	proxyURL, _ := url.Parse("http://proxy.example.test:3128")
	guard.fromEnvironment = func(*http.Request) (*url.URL, error) { return proxyURL, nil }
	for target, allowed := range map[string]bool{
		"https://only-the-proxy-knows.example.test/x": true,
		"https://127.0.0.1/x":                         false,
		"https://localhost/x":                         false,
		"https://api.localhost/x":                     false,
		"https://[fd00:ec2::254]/x":                   false,
		"https://192.0.2.10/x":                        true,
	} {
		request, _ := http.NewRequest(http.MethodGet, target, nil)
		got, err := guard.proxy(request)
		if (err == nil) != allowed || (allowed && got != proxyURL) {
			t.Errorf("proxy(%s) = %v, %v; want allowed %t", target, got, err, allowed)
		}
	}
	if _, ok := guard.proxies.Load("proxy.example.test:3128"); !ok {
		t.Fatal("the proxy address was not recorded, so its dial would be checked")
	}
}
