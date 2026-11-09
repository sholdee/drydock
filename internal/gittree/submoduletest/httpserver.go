package submoduletest

import (
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

// HTTPRequest is one request that an HTTPServer received.
type HTTPRequest struct {
	Method string
	// Path is the URL path and query of the request.
	Path string
	// Authorization is the Authorization header of the request.
	Authorization string
}

// HTTPServer serves the bare repositories below its root over Git smart
// HTTP with `git http-backend`, on a local port. It records the
// Authorization header of every request. Each server has its own port,
// so two servers are two hosts for a credential check.
type HTTPServer struct {
	// URL is the base URL, such as http://127.0.0.1:12345.
	URL string

	git      string
	mu       sync.Mutex
	root     string
	redirect string
	requests []HTTPRequest
}

// ServeSmartHTTP starts an HTTPServer for the bare repositories below
// root. It skips the test when git is not installed. The server stops with
// the test.
func ServeSmartHTTP(t testing.TB, root string) *HTTPServer {
	t.Helper()
	server := &HTTPServer{git: Git(t), root: root}
	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)
	server.URL = httpServer.URL
	return server
}

// SetRoot changes the directory that the server serves.
func (s *HTTPServer) SetRoot(root string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.root = root
}

// RedirectTo makes the server answer every request with a 302 redirect to
// the same path and query below base. An empty base stops the redirects.
func (s *HTTPServer) RedirectTo(base string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.redirect = base
}

// Requests returns the requests that the server received, in order.
func (s *HTTPServer) Requests() []HTTPRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]HTTPRequest(nil), s.requests...)
}

// AuthorizationHeaders returns the Authorization header of every request
// that had one.
func (s *HTTPServer) AuthorizationHeaders() []string {
	var out []string
	for _, request := range s.Requests() {
		if request.Authorization != "" {
			out = append(out, request.Authorization)
		}
	}
	return out
}

func (s *HTTPServer) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	s.mu.Lock()
	s.requests = append(s.requests, HTTPRequest{
		Method:        request.Method,
		Path:          request.URL.RequestURI(),
		Authorization: request.Header.Get("Authorization"),
	})
	root, redirect := s.root, s.redirect
	s.mu.Unlock()
	if redirect != "" {
		http.Redirect(writer, request, strings.TrimRight(redirect, "/")+request.URL.RequestURI(), http.StatusFound)
		return
	}
	handler := &cgi.Handler{
		Path: s.git,
		Args: []string{"http-backend"},
		Dir:  root,
		Env: []string{
			"GIT_PROJECT_ROOT=" + root,
			"GIT_HTTP_EXPORT_ALL=1",
			"GIT_CONFIG_GLOBAL=" + os.DevNull,
			"GIT_CONFIG_NOSYSTEM=1",
		},
	}
	handler.ServeHTTP(writer, request)
}
