package gitlfs

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sholdee/drydock/internal/credhost"
	"github.com/sholdee/drydock/internal/gitauth"
	"github.com/sholdee/drydock/internal/gitlfs/lfstest"
	"github.com/sholdee/drydock/internal/gittree"
)

const (
	testContent = "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: from-lfs\n"
	gitSecret   = "git-secret-token"
)

// fillCase is one tree with one pointer file and a drydock store.
type fillCase struct {
	root    string
	store   string
	pointer gittree.LFSPointer
	text    string
}

func newFillCase(t *testing.T, content string) fillCase {
	t.Helper()
	text := lfstest.Pointer(content)
	pointer, ok := gittree.ParseLFSPointer([]byte(text))
	if !ok {
		t.Fatal("test pointer does not parse")
	}
	pointer.Path = "dir/cm.yaml"
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "dir", "cm.yaml"), text)
	return fillCase{root: root, store: filepath.Join(t.TempDir(), "lfs"), pointer: pointer, text: text}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (c fillCase) file(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(c.root, "dir", "cm.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func (c fillCase) options(server *lfstest.Server, credentials gitauth.Credentials) Options {
	return Options{
		CacheDir:     c.store,
		AllowNetwork: true,
		Endpoint:     Endpoint{URL: server.Batch.URL + "/org/repo.git/info/lfs", RepoHost: "127.0.0.1"},
		Credentials:  credentials,
		TLSConfig:    server.TLSConfig(),
	}
}

// fill runs Fill and fails the test on an error.
func (c fillCase) fill(t *testing.T, opts Options) Result {
	t.Helper()
	result, err := c.try(opts)
	if err != nil {
		t.Fatalf("Fill() error = %v", err)
	}
	return result
}

// fail runs Fill, fails the test unless it fails with ErrFetch, and
// returns the error text.
func (c fillCase) fail(t *testing.T, opts Options) string {
	t.Helper()
	result, err := c.try(opts)
	if !errors.Is(err, ErrFetch) {
		t.Fatalf("Fill() = %+v, %v; want ErrFetch", result, err)
	}
	if c.file(t) != c.text {
		t.Fatalf("file = %q after a failed Fill, want the pointer", c.file(t))
	}
	return err.Error()
}

func (c fillCase) try(opts Options) (Result, error) {
	return Fill(context.Background(), c.root, []gittree.LFSPointer{c.pointer}, opts)
}

func hostEntry(t *testing.T, rawURL string) string {
	t.Helper()
	entry, ok := credhost.Entry(rawURL)
	if !ok {
		t.Fatalf("credhost.Entry(%q) failed", rawURL)
	}
	return entry
}

func headersHold(requests []lfstest.Request, value string) bool {
	for _, request := range requests {
		for _, values := range request.Header {
			for _, item := range values {
				if strings.Contains(item, value) {
					return true
				}
			}
		}
	}
	return false
}

// TestFillDownloadsObjectsWithScopedCredentials pins the network path: the
// batch request to a listed host gets the --git-* bearer token, the object
// host gets only the headers of the download action and never the token,
// and the verified object fills the pointer and lands in the store.
func TestFillDownloadsObjectsWithScopedCredentials(t *testing.T) {
	server := lfstest.New(t, false, false)
	server.Add(testContent)
	server.ActionHeader = map[string]string{"Authorization": "RemoteAuth object-token", "X-Object-Key": "object-key"}
	c := newFillCase(t, testContent)
	result := c.fill(t, c.options(server, gitauth.Credentials{BearerToken: gitSecret, Hosts: []string{hostEntry(t, server.Batch.URL)}}))

	if len(result.Missing) != 0 || result.Downloaded != 1 || c.file(t) != testContent {
		t.Fatalf("Fill() = %+v, file = %q; want the object content", result, c.file(t))
	}
	batch := server.RequestsTo("batch")
	if len(batch) != 1 || batch[0].Header.Get("Authorization") != "Bearer "+gitSecret || batch[0].Path != "/org/repo.git/info/lfs/objects/batch" {
		t.Fatalf("batch requests = %+v, want one POST with the bearer token", batch)
	}
	if headersHold(batch, "object-token") || headersHold(batch, "object-key") {
		t.Fatal("the batch request carries a header of the download action")
	}
	objects := server.RequestsTo("objects")
	if len(objects) != 1 || objects[0].Header.Get("Authorization") != "RemoteAuth object-token" || objects[0].Header.Get("X-Object-Key") != "object-key" {
		t.Fatalf("object requests = %+v, want the action headers", objects)
	}
	if headersHold(objects, gitSecret) {
		t.Fatal("the object host received the --git-* token")
	}
	if !(store{dir: c.store}).has(objectRef{OID: c.pointer.OID, Size: c.pointer.Size}) {
		t.Fatal("the drydock store does not hold the downloaded object")
	}
}

// TestFillSendsCredentialsOnlyToListedBatchHost pins that the --git-*
// credentials go to the batch endpoint only when a --git-auth-host entry
// names its scheme, host, and port, and never to the object host, even
// when an entry names that host.
func TestFillSendsCredentialsOnlyToListedBatchHost(t *testing.T) {
	server := lfstest.New(t, false, false)
	server.Add(testContent)
	c := newFillCase(t, testContent)
	credentials := gitauth.Credentials{Username: "user", Password: gitSecret, Hosts: []string{hostEntry(t, server.Objects.URL)}}
	result := c.fill(t, c.options(server, credentials))
	if len(result.Missing) != 0 {
		t.Fatalf("Fill() = %+v, want the object", result)
	}
	for _, request := range server.Requests() {
		if request.Header.Get("Authorization") != "" {
			t.Fatalf("%s request %s carries Authorization, want none", request.Host, request.Path)
		}
	}
}

// TestFillDropsActionHeadersOnCrossOriginRedirect pins that the headers of
// a download action go to the href alone: a redirect to another host
// loses every one of them, not only Authorization.
func TestFillDropsActionHeadersOnCrossOriginRedirect(t *testing.T) {
	other := lfstest.New(t, false, false)
	other.Add(testContent)
	server := lfstest.New(t, false, false)
	server.Add(testContent)
	server.ActionHeader = map[string]string{"Authorization": "RemoteAuth object-token", "X-Object-Key": "object-key"}
	server.RedirectObjects = other.Objects.URL
	c := newFillCase(t, testContent)
	result := c.fill(t, c.options(server, gitauth.Credentials{BearerToken: gitSecret, Hosts: []string{hostEntry(t, server.Batch.URL)}}))
	if len(result.Missing) != 0 || c.file(t) != testContent {
		t.Fatalf("Fill() = %+v, want the object through the redirect", result)
	}
	redirected := other.RequestsTo("objects")
	if len(redirected) != 1 {
		t.Fatalf("redirect target requests = %+v, want one", redirected)
	}
	if headersHold(redirected, "object-token") || headersHold(redirected, "object-key") || headersHold(redirected, gitSecret) {
		t.Fatalf("redirect target headers = %v, want no action header or credential", redirected[0].Header)
	}
}

// TestFillKeepsActionHeadersOnSameOriginRedirect pins that a redirect on
// the host of the href keeps the action headers, which that host gave.
func TestFillKeepsActionHeadersOnSameOriginRedirect(t *testing.T) {
	server := lfstest.New(t, false, false)
	server.Add(testContent)
	server.ActionHeader = map[string]string{"X-Object-Key": "object-key"}
	var once sync.Once
	mux := http.NewServeMux()
	var seen []string
	var mu sync.Mutex
	redirector := httptest.NewServer(mux)
	t.Cleanup(redirector.Close)
	mux.HandleFunc("/", func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		seen = append(seen, request.Header.Get("X-Object-Key"))
		mu.Unlock()
		redirected := false
		once.Do(func() { redirected = true })
		if redirected {
			http.Redirect(writer, request, "/final", http.StatusFound)
			return
		}
		_, _ = writer.Write([]byte(testContent))
	})
	server.Mutate = func(objects []lfstest.Object) []lfstest.Object {
		for i := range objects {
			action := objects[i].Actions["download"]
			action.Href = redirector.URL + "/start"
			objects[i].Actions["download"] = action
		}
		return objects
	}
	c := newFillCase(t, testContent)
	result := c.fill(t, c.options(server, gitauth.Credentials{}))
	if len(result.Missing) != 0 || len(seen) != 2 || seen[0] != "object-key" || seen[1] != "object-key" {
		t.Fatalf("Fill() = %+v, X-Object-Key per hop = %q; want it on both hops", result, seen)
	}
}

// TestFillFailsForObjectsThatDoNotMatchThePointer pins that a download
// whose SHA-256 or size differs from the pointer never fills it and never
// enters the store, and fails the fill, as `git lfs fetch` fails.
func TestFillFailsForObjectsThatDoNotMatchThePointer(t *testing.T) {
	for name, tc := range map[string]struct {
		body   func(string, []byte) []byte
		reason string
	}{
		"sha":    {func(_ string, content []byte) []byte { return []byte(strings.ToUpper(string(content))) }, "SHA-256"},
		"longer": {func(_ string, content []byte) []byte { return append(content, []byte("extra")...) }, "size"},
		"short":  {func(_ string, content []byte) []byte { return content[:5] }, "size"},
	} {
		server := lfstest.New(t, false, false)
		server.Add(testContent)
		server.Body = tc.body
		c := newFillCase(t, testContent)
		message := c.fail(t, c.options(server, gitauth.Credentials{}))
		if !strings.Contains(message, tc.reason) || strings.Contains(message, "62") {
			t.Fatalf("%s: error %q, want a reason about %s without a byte count", name, message, tc.reason)
		}
		if _, err := os.Stat((store{dir: c.store}).objectPath(c.pointer.OID)); !os.IsNotExist(err) {
			t.Fatalf("%s: the store holds the bad object (stat error %v)", name, err)
		}
	}
}

// TestFillLeavesObjectsOverTheLimitsUnsupported pins that an object larger
// than the limit for one object, or past the limit for the tree, is never
// requested and stays a pointer as unsupported, not as a failure.
func TestFillLeavesObjectsOverTheLimitsUnsupported(t *testing.T) {
	for name, limits := range map[string]Limits{
		"object": {MaxObjectBytes: 10},
		"total":  {MaxTotalBytes: 10},
	} {
		server := lfstest.New(t, false, false)
		server.Add(testContent)
		c := newFillCase(t, testContent)
		opts := c.options(server, gitauth.Credentials{})
		opts.Limits = limits
		result := c.fill(t, opts)
		if len(result.Unsupported) != 1 || len(server.Requests()) != 0 || !strings.Contains(strings.Join(result.UnsupportedReasons, ";"), "limit") {
			t.Fatalf("%s: Fill() = %+v, requests = %d; want the object refused before any request", name, result, len(server.Requests()))
		}
	}
}

// TestFillOfflineReadsOnlyTheStores pins that without the network Fill
// reads the local store and the drydock store, never a server, and keeps
// a pointer whose object neither holds.
func TestFillOfflineReadsOnlyTheStores(t *testing.T) {
	server := lfstest.New(t, false, false)
	server.Add(testContent)
	c := newFillCase(t, testContent)
	opts := c.options(server, gitauth.Credentials{})
	opts.AllowNetwork = false
	result := c.fill(t, opts)
	if len(result.Missing) != 1 || len(server.Requests()) != 0 || !strings.Contains(result.Reasons[0], "--offline") {
		t.Fatalf("Fill(offline, empty store) = %+v, requests = %d", result, len(server.Requests()))
	}
	writeFile(t, (store{dir: c.store}).objectPath(c.pointer.OID), testContent)
	result = c.fill(t, opts)
	if len(result.Missing) != 0 || c.file(t) != testContent || len(server.Requests()) != 0 {
		t.Fatalf("Fill(offline, cached) = %+v, requests = %d; want the cached object", result, len(server.Requests()))
	}
}

// TestFillReadsLocalStoreThenCacheThenNetwork pins the order of the
// sources: an object in the local .git/lfs store or in the drydock store
// is never downloaded.
func TestFillReadsLocalStoreThenCacheThenNetwork(t *testing.T) {
	server := lfstest.New(t, false, false)
	contents := []string{"local: 1\n", "cached: 2\n", "network: 3\n"}
	root := t.TempDir()
	local := t.TempDir()
	cacheDir := filepath.Join(t.TempDir(), "lfs")
	pointers := make([]gittree.LFSPointer, 0, len(contents))
	for i, content := range contents {
		server.Add(content)
		text := lfstest.Pointer(content)
		pointer, _ := gittree.ParseLFSPointer([]byte(text))
		pointer.Path = []string{"a.yaml", "b.yaml", "c.yaml"}[i]
		writeFile(t, filepath.Join(root, pointer.Path), text)
		pointers = append(pointers, pointer)
	}
	writeFile(t, gittree.LFSObjectPath(local, pointers[0].OID), contents[0])
	writeFile(t, (store{dir: cacheDir}).objectPath(pointers[1].OID), contents[1])
	result, err := Fill(context.Background(), root, pointers, Options{
		LocalStores: []string{local}, CacheDir: cacheDir, AllowNetwork: true,
		Endpoint: Endpoint{URL: server.Batch.URL + "/repo.git/info/lfs"},
	})
	if err != nil || len(result.Missing) != 0 || result.Downloaded != 1 {
		t.Fatalf("Fill() = %+v, %v; want one download", result, err)
	}
	objects := server.RequestsTo("objects")
	if len(objects) != 1 || !strings.Contains(objects[0].Path, pointers[2].OID) {
		t.Fatalf("object requests = %+v, want only the network object", objects)
	}
	for i, pointer := range pointers {
		if data, _ := os.ReadFile(filepath.Join(root, pointer.Path)); string(data) != contents[i] {
			t.Fatalf("%s = %q, want %q", pointer.Path, data, contents[i])
		}
	}
}

// TestFillReplacesACorruptCacheObject pins that an object in the drydock
// store that does not match its pointer is downloaded again.
func TestFillReplacesACorruptCacheObject(t *testing.T) {
	server := lfstest.New(t, false, false)
	server.Add(testContent)
	c := newFillCase(t, testContent)
	writeFile(t, (store{dir: c.store}).objectPath(c.pointer.OID), strings.ToUpper(testContent))
	result := c.fill(t, c.options(server, gitauth.Credentials{}))
	if len(result.Missing) != 0 || result.Downloaded != 1 || c.file(t) != testContent {
		t.Fatalf("Fill() = %+v, file = %q; want the object downloaded again", result, c.file(t))
	}
}

// TestFillFailsWhenTheServerLacksTheObject pins that an object error of
// the batch response fails the fill with its code, as `git lfs fetch`
// fails the manifest request of the repo-server.
func TestFillFailsWhenTheServerLacksTheObject(t *testing.T) {
	server := lfstest.New(t, false, false)
	c := newFillCase(t, testContent)
	if message := c.fail(t, c.options(server, gitauth.Credentials{})); !strings.Contains(message, "error 404") {
		t.Fatalf("error %q, want the 404 reason", message)
	}
}

// TestFillReportsARefusedBatchWithoutSecrets pins that a refused batch
// request keeps the pointer, and that the reason holds the status but no
// credential, also when the server echoes one.
func TestFillReportsARefusedBatchWithoutSecrets(t *testing.T) {
	server := lfstest.New(t, false, false)
	server.BatchStatus = http.StatusForbidden
	c := newFillCase(t, testContent)
	message := c.fail(t, c.options(server, gitauth.Credentials{BearerToken: gitSecret, Hosts: []string{hostEntry(t, server.Batch.URL)}}))
	if !strings.Contains(message, "HTTP 403") || strings.Contains(message, gitSecret) {
		t.Fatalf("error %q, want the 403 reason without the token", message)
	}
}

// TestFillFailsForMaliciousBatchObjects pins that Fill uses only the
// objects that it asked for, with the size of the pointer: an oid with a
// path in it, a wrong oid, or a huge size never names a file or a
// download, and the fill fails because the server did not give the
// object.
func TestFillFailsForMaliciousBatchObjects(t *testing.T) {
	cases := map[string]func(lfstest.Object) lfstest.Object{
		"traversal oid": func(object lfstest.Object) lfstest.Object {
			object.OID = "../../../../escape"
			return object
		},
		"wrong oid": func(object lfstest.Object) lfstest.Object {
			object.OID = strings.Repeat("ab", 32)
			return object
		},
		"huge size": func(object lfstest.Object) lfstest.Object {
			object.Size = 1 << 50
			return object
		},
	}
	for name, mutate := range cases {
		server := lfstest.New(t, false, false)
		server.Add(testContent)
		server.Mutate = func(objects []lfstest.Object) []lfstest.Object {
			for i := range objects {
				objects[i] = mutate(objects[i])
			}
			return objects
		}
		c := newFillCase(t, testContent)
		c.fail(t, c.options(server, gitauth.Credentials{}))
		if len(server.RequestsTo("objects")) != 0 {
			t.Fatalf("%s: object requests = %d; want the object ignored", name, len(server.RequestsTo("objects")))
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(c.store), "escape")); !os.IsNotExist(err) {
			t.Fatalf("%s: a file escaped the store", name)
		}
	}
}

// TestFillRefusesPlainHTTPFromHTTPSBatch pins that an https batch endpoint
// cannot point a download, or a redirect of one, at plain http.
func TestFillRefusesPlainHTTPFromHTTPSBatch(t *testing.T) {
	server := lfstest.New(t, true, false)
	server.Add(testContent)
	c := newFillCase(t, testContent)
	result := c.fill(t, c.options(server, gitauth.Credentials{}))
	if len(result.Unsupported) != 1 || len(server.RequestsTo("objects")) != 0 || !strings.Contains(strings.Join(result.UnsupportedReasons, ";"), "plain http") {
		t.Fatalf("Fill(https batch, http href) = %+v, object requests = %d", result, len(server.RequestsTo("objects")))
	}

	plain := lfstest.New(t, false, false)
	plain.Add(testContent)
	secure := lfstest.New(t, true, true)
	secure.Add(testContent)
	secure.RedirectObjects = plain.Objects.URL
	c = newFillCase(t, testContent)
	result = c.fill(t, c.options(secure, gitauth.Credentials{}))
	if len(result.Unsupported) != 1 || len(plain.RequestsTo("objects")) != 0 {
		t.Fatalf("Fill(https href, redirect to http) = %+v, http requests = %d", result, len(plain.RequestsTo("objects")))
	}
}

// TestFillTreatsEndpointErrorsByCause pins that an endpoint that drydock
// refuses, such as a local store, keeps the pointers as unsupported, and
// that an endpoint that git-lfs cannot use either, such as an empty
// lfs.url, fails the fill.
func TestFillTreatsEndpointErrorsByCause(t *testing.T) {
	c := newFillCase(t, testContent)
	_, err := FindEndpoint("https://git.example.test/r.git", []byte("[lfs]\n\turl = file:///srv/lfs\n"))
	result := c.fill(t, Options{CacheDir: c.store, AllowNetwork: true, EndpointErr: err})
	if len(result.Unsupported) != 1 || !strings.Contains(result.UnsupportedReasons[0], "local Git LFS store") {
		t.Fatalf("Fill(file lfs.url) = %+v, want it unsupported", result)
	}
	_, err = FindEndpoint("https://git.example.test/r.git", []byte("[lfs]\n\turl =\n"))
	if message := c.fail(t, Options{CacheDir: c.store, AllowNetwork: true, EndpointErr: err}); !strings.Contains(message, "empty") {
		t.Fatalf("Fill(empty lfs.url) error %q, want the empty lfs.url", message)
	}
}
