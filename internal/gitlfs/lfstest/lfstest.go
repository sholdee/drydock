// Package lfstest serves the Git LFS batch API and an object store on two
// local hosts for tests, and records every request.
package lfstest

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Request is one request that a Server received.
type Request struct {
	// Host is "batch" or "objects".
	Host   string
	Method string
	Path   string
	Header http.Header
}

// Object is one object of a batch response, which Mutate may change.
type Object struct {
	OID           string            `json:"oid"`
	Size          int64             `json:"size"`
	Authenticated bool              `json:"authenticated,omitempty"`
	Actions       map[string]Action `json:"actions,omitempty"`
	Error         *ObjectError      `json:"error,omitempty"`
}

// Action is the download action of an Object.
type Action struct {
	Href   string            `json:"href"`
	Header map[string]string `json:"header,omitempty"`
}

// ObjectError is the error of an Object.
type ObjectError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Server is a Git LFS server: Batch answers batch requests at any path
// that ends in /objects/batch, and Objects serves the objects at
// /objects/<oid>. Its fields change how it answers; set them before the
// requests.
type Server struct {
	Batch   *httptest.Server
	Objects *httptest.Server

	// ActionHeader is the header of every download action.
	ActionHeader map[string]string
	// Mutate changes the objects of each batch response.
	Mutate func([]Object) []Object
	// Body, when set, replaces the bytes that Objects serves for an oid.
	Body func(oid string, content []byte) []byte
	// BatchStatus, when not 0, answers every batch request with it and a
	// message that holds the Authorization header of the request.
	BatchStatus int
	// RedirectObjects, when set, makes Objects answer with a 302 redirect
	// to the same path below this base.
	RedirectObjects string
	// ObjectDelay makes Objects wait before it answers.
	ObjectDelay time.Duration

	mu       sync.Mutex
	objects  map[string][]byte
	requests []Request
}

// New starts a Server. With tlsBatch, the batch host serves https, and
// Client trusts it; the object host serves plain http unless tlsObjects.
func New(t testing.TB, tlsBatch, tlsObjects bool) *Server {
	t.Helper()
	s := &Server{objects: map[string][]byte{}}
	s.Batch = start(t, http.HandlerFunc(s.ServeBatch), tlsBatch)
	s.Objects = start(t, http.HandlerFunc(s.serveObject), tlsObjects)
	return s
}

func start(t testing.TB, handler http.Handler, secure bool) *httptest.Server {
	var server *httptest.Server
	if secure {
		server = httptest.NewTLSServer(handler)
	} else {
		server = httptest.NewServer(handler)
	}
	t.Cleanup(server.Close)
	return server
}

// TLSConfig returns a TLS configuration that trusts the certificates of
// both hosts. httptest gives every server the same certificate.
func (s *Server) TLSConfig() *tls.Config {
	for _, server := range []*httptest.Server{s.Batch, s.Objects} {
		if server.TLS == nil {
			continue
		}
		if trusted, ok := server.Client().Transport.(*http.Transport); ok && trusted.TLSClientConfig != nil {
			return trusted.TLSClientConfig.Clone()
		}
	}
	return nil
}

// Add stores content and returns its oid and the text of its pointer.
func (s *Server) Add(content string) (string, string) {
	sum := sha256.Sum256([]byte(content))
	oid := hex.EncodeToString(sum[:])
	s.mu.Lock()
	s.objects[oid] = []byte(content)
	s.mu.Unlock()
	return oid, Pointer(content)
}

// Pointer returns the text of the Git LFS pointer of content.
func Pointer(content string) string {
	sum := sha256.Sum256([]byte(content))
	return "version https://git-lfs.github.com/spec/v1\noid sha256:" + hex.EncodeToString(sum[:]) + "\nsize " + strconv.Itoa(len(content)) + "\n"
}

// Requests returns the requests of both hosts, in order.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// RequestsTo returns the requests of one host ("batch" or "objects").
func (s *Server) RequestsTo(host string) []Request {
	var out []Request
	for _, request := range s.Requests() {
		if request.Host == host {
			out = append(out, request)
		}
	}
	return out
}

func (s *Server) record(host string, request *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, Request{Host: host, Method: request.Method, Path: request.URL.RequestURI(), Header: request.Header.Clone()})
}

type batchResponse struct {
	Transfer string   `json:"transfer"`
	Objects  []Object `json:"objects"`
	HashAlgo string   `json:"hash_algo"`
}

type batchRequest struct {
	Operation string `json:"operation"`
	Objects   []struct {
		OID  string `json:"oid"`
		Size int64  `json:"size"`
	} `json:"objects"`
}

// ServeBatch answers a batch request. It serves any path that ends in
// /objects/batch, so a test can mount it next to a Git server.
func (s *Server) ServeBatch(writer http.ResponseWriter, request *http.Request) {
	s.record("batch", request)
	if request.Method != http.MethodPost || !strings.HasSuffix(request.URL.Path, "/objects/batch") {
		http.NotFound(writer, request)
		return
	}
	if s.BatchStatus != 0 {
		// The message echoes the Authorization header, as a hostile or
		// careless server can, so a test can check that drydock never
		// prints it.
		writer.WriteHeader(s.BatchStatus)
		if err := json.NewEncoder(writer).Encode(map[string]string{"message": "batch refused for " + request.Header.Get("Authorization")}); err != nil {
			http.Error(writer, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	var parsed batchRequest
	if err := json.NewDecoder(request.Body).Decode(&parsed); err != nil || parsed.Operation != "download" {
		http.Error(writer, "bad batch request", http.StatusUnprocessableEntity)
		return
	}
	objects := make([]Object, 0, len(parsed.Objects))
	for _, object := range parsed.Objects {
		objects = append(objects, s.batchObject(object.OID, object.Size))
	}
	if s.Mutate != nil {
		objects = s.Mutate(objects)
	}
	writer.Header().Set("Content-Type", "application/vnd.git-lfs+json")
	if err := json.NewEncoder(writer).Encode(batchResponse{Transfer: "basic", Objects: objects, HashAlgo: "sha256"}); err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) batchObject(oid string, size int64) Object {
	s.mu.Lock()
	_, ok := s.objects[oid]
	s.mu.Unlock()
	if !ok {
		return Object{OID: oid, Size: size, Error: &ObjectError{Code: 404, Message: "Object does not exist"}}
	}
	return Object{OID: oid, Size: size, Actions: map[string]Action{
		"download": {Href: s.Objects.URL + "/objects/" + oid + "?X-Signature=signed-token", Header: s.ActionHeader},
	}}
}

func (s *Server) serveObject(writer http.ResponseWriter, request *http.Request) {
	s.record("objects", request)
	if s.ObjectDelay > 0 {
		time.Sleep(s.ObjectDelay)
	}
	if s.RedirectObjects != "" {
		http.Redirect(writer, request, strings.TrimSuffix(s.RedirectObjects, "/")+request.URL.RequestURI(), http.StatusFound)
		return
	}
	oid := strings.TrimPrefix(request.URL.Path, "/objects/")
	s.mu.Lock()
	content, ok := s.objects[oid]
	s.mu.Unlock()
	if !ok {
		http.NotFound(writer, request)
		return
	}
	if s.Body != nil {
		content = s.Body(oid, content)
	}
	_, _ = writer.Write(content)
}
