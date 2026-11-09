package gitlfs

import (
	"errors"
	"strings"
	"testing"
)

// TestEndpointFollowsGitLFS pins the endpoint rules of git-lfs 3.x for the
// remote "origin": lfs.url, then remote.origin.lfsurl, from .lfsconfig,
// then the repository URL with .git/info/lfs.
func TestEndpointFollowsGitLFS(t *testing.T) {
	for _, tc := range []struct {
		name, repoURL, lfsconfig, want string
	}{
		{"https with .git", "https://git.example.test/org/repo.git", "", "https://git.example.test/org/repo.git/info/lfs"},
		{"https without .git", "https://git.example.test/org/repo", "", "https://git.example.test/org/repo.git/info/lfs"},
		{"trailing slash", "https://git.example.test/org/repo/", "", "https://git.example.test/org/repo.git/info/lfs"},
		{"port", "http://127.0.0.1:8080/repo.git", "", "http://127.0.0.1:8080/repo.git/info/lfs"},
		{"git scheme", "git://git.example.test/org/repo.git", "", "https://git.example.test/org/repo.git/info/lfs"},
		{"lfs.url", "https://git.example.test/org/repo.git", "[lfs]\n\turl = https://lfs.example.test/api/\n", "https://lfs.example.test/api"},
		{"lfs.url last wins", "https://git.example.test/org/repo.git", "[lfs]\n\turl = https://a.example.test\n\turl = https://b.example.test\n", "https://b.example.test"},
		{"remote lfsurl", "https://git.example.test/org/repo.git", "[remote \"origin\"]\n\tlfsurl = https://lfs.example.test/r\n", "https://lfs.example.test/r"},
		{"lfs.url over lfsurl", "https://git.example.test/org/repo.git", "[remote \"origin\"]\n\tlfsurl = https://b.example.test\n[lfs]\n\turl = https://a.example.test\n", "https://a.example.test"},
		{"other remote ignored", "https://git.example.test/org/repo.git", "[remote \"fork\"]\n\tlfsurl = https://b.example.test\n", "https://git.example.test/org/repo.git/info/lfs"},
		{"lfs.url for an ssh remote", "git@git.example.test:org/repo.git", "[lfs]\n\turl = https://lfs.example.test\n", "https://lfs.example.test"},
	} {
		got, err := Endpoint(tc.repoURL, []byte(tc.lfsconfig))
		if err != nil || got != tc.want {
			t.Errorf("%s: Endpoint() = %q, %v; want %q", tc.name, got, err, tc.want)
		}
	}
}

// TestEndpointRefusesWhatDrydockCannotUse pins the endpoints that drydock
// refuses: SSH, other schemes, and URLs from the repository content that
// carry a user, a password, a query, or a fragment. No error quotes the
// URL.
func TestEndpointRefusesWhatDrydockCannotUse(t *testing.T) {
	for _, tc := range []struct {
		name, repoURL, lfsconfig string
		ssh                      bool
	}{
		{"scp", "git@git.example.test:org/repo.git", "", true},
		{"ssh", "ssh://git@git.example.test/org/repo.git", "", true},
		{"git+ssh", "git+ssh://git.example.test/org/repo.git", "", true},
		{"ssh lfs.url", "https://git.example.test/r.git", "[lfs]\n\turl = ssh://git@lfs.example.test/r\n", true},
		{"file", "file:///srv/repo.git", "", false},
		{"local path", "/srv/repo.git", "", false},
		{"file lfs.url", "https://git.example.test/r.git", "[lfs]\n\turl = file:///etc\n", false},
		{"userinfo", "https://git.example.test/r.git", "[lfs]\n\turl = https://user:hunter2@lfs.example.test\n", false},
		{"query", "https://git.example.test/r.git", "[lfs]\n\turl = https://lfs.example.test/?token=hunter2\n", false},
		{"bad config", "https://git.example.test/r.git", "[lfs\n", false},
		{"huge config", "https://git.example.test/r.git", "[lfs]\n#" + strings.Repeat("x", MaxLFSConfigSize), false},
	} {
		got, err := Endpoint(tc.repoURL, []byte(tc.lfsconfig))
		if err == nil {
			t.Errorf("%s: Endpoint() = %q, want an error", tc.name, got)
			continue
		}
		if errors.Is(err, ErrSSHEndpoint) != tc.ssh {
			t.Errorf("%s: Endpoint() error = %v, want ErrSSHEndpoint %t", tc.name, err, tc.ssh)
		}
		if strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), "example.test") {
			t.Errorf("%s: Endpoint() error %q quotes the URL", tc.name, err)
		}
	}
}
