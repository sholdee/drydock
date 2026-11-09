package lfstest

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/gittree/submoduletest"
)

// Repo is a Git repository served over smart HTTP, with the batch API of
// a Server on the same host at <repo>.git/info/lfs, where git-lfs looks
// for it.
type Repo struct {
	// URL is the repository URL, such as http://127.0.0.1:1234/repo.git.
	URL string
	// Git records the Git requests.
	Git *submoduletest.HTTPServer
	// Work is the work tree that the bare repository was cloned from.
	Work string
}

// ServeRepo commits files to a new repository and serves it, with the
// batch API of s on the same host. The objects stay on the object host of
// s.
func ServeRepo(t testing.TB, s *Server, files map[string]string) Repo {
	t.Helper()
	dir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	submoduletest.NewRepo(t, dir, "repo", files)
	git := submoduletest.ServeSmartHTTP(t, dir)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.Contains(request.URL.Path, "/info/lfs/") {
			s.ServeBatch(writer, request)
			return
		}
		git.ServeHTTP(writer, request)
	}))
	t.Cleanup(server.Close)
	return Repo{URL: server.URL + "/repo.git", Git: git, Work: filepath.Join(dir, "repo-work")}
}
