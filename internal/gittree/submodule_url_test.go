package gittree

import (
	"testing"

	"github.com/go-git/go-git/v5/plumbing/object"
)

// TestResolveSubmoduleURLMatchesGit pins relative URL resolution against
// the cases of git's t/t0060-path-utils.sh (submodule--helper
// resolve-relative-url) and the URL shapes Argo CD repositories use.
func TestResolveSubmoduleURLMatchesGit(t *testing.T) {
	cases := []struct {
		remote, url, want string
	}{
		{"https://github.com/org/repo.git", "../sub.git", "https://github.com/org/sub.git"},
		{"https://github.com/org/repo", "../sub", "https://github.com/org/sub"},
		{"https://github.com/org/repo/", "../sub", "https://github.com/org/sub"},
		{"https://github.com/org/repo.git", "./sub", "https://github.com/org/repo.git/sub"},
		{"https://github.com/org/repo.git", "../../other/sub.git", "https://github.com/other/sub.git"},
		{"git@github.com:org/repo.git", "../sub.git", "git@github.com:org/sub.git"},
		{"git@github.com:repo.git", "../sub.git", "git@github.com:sub.git"},
		{"ssh://git@host/org/repo.git", "../sub.git", "ssh://git@host/org/sub.git"},
		{"/abs/path/repo", "../sub", "/abs/path/sub"},
		{"/abs/path/repo", "./sub/", "/abs/path/repo/sub"},
		{"https://github.com/org/repo.git", "https://other.example/x.git", "https://other.example/x.git"},
		{"https://github.com/org/repo.git", "git@other:x.git", "git@other:x.git"},
		{"foo", "../sub", "sub"},
		{"foo/bar", "../sub", "foo/sub"},
	}
	for _, tc := range cases {
		got, err := ResolveSubmoduleURL(tc.remote, tc.url)
		if err != nil {
			t.Fatalf("ResolveSubmoduleURL(%q, %q) error = %v", tc.remote, tc.url, err)
		}
		if got != tc.want {
			t.Fatalf("ResolveSubmoduleURL(%q, %q) = %q, want %q", tc.remote, tc.url, got, tc.want)
		}
	}
}

// TestResolveSubmoduleURLFailsLikeGit pins the cases where git cannot
// resolve a relative URL.
func TestResolveSubmoduleURLFailsLikeGit(t *testing.T) {
	for _, tc := range []struct{ remote, url string }{
		{"", "../sub"},
		{"foo", "../../sub"},
	} {
		if got, err := ResolveSubmoduleURL(tc.remote, tc.url); err == nil {
			t.Fatalf("ResolveSubmoduleURL(%q, %q) = %q, want an error", tc.remote, tc.url, got)
		}
	}
}

// TestSubmoduleTransportsMatchGitProtocolPolicy pins the transports that
// the repo-server's git allows for a submodule: http, https, git, and ssh,
// never file or ext.
func TestSubmoduleTransportsMatchGitProtocolPolicy(t *testing.T) {
	allowed := map[string]bool{
		"https://h/x.git":     true,
		"http://h/x.git":      true,
		"git://h/x.git":       true,
		"ssh://git@h/x.git":   true,
		"git@h:x.git":         true,
		"file:///tmp/x.git":   false,
		"/tmp/x.git":          false,
		"../x.git":            false,
		"ext::sh -c touch% x": false,
		"fd::3":               false,
		"ftp://h/x.git":       false,
	}
	for value, want := range allowed {
		if got := AllowedSubmoduleTransport(SubmoduleTransport(value)); got != want {
			t.Fatalf("transport of %q allowed = %v, want %v", value, got, want)
		}
	}
}

// TestParseModulesSkipsWhatGitIgnores pins that a module name with a ".."
// component, and a path or URL that looks like an option, are ignored as
// git ignores them, and that the last value of a key wins.
func TestParseModulesSkipsWhatGitIgnores(t *testing.T) {
	modules, err := ParseModules([]byte(`[submodule "ok"]
	path = a
	url = ../a.git
	url = ../a2.git
	update = none
[submodule "../evil"]
	path = b
	url = ../b.git
[submodule "dash"]
	path = c
	url = --upload-pack=touch
[submodule "dashpath"]
	path = -d
	url = ../d.git
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := modules["a"]; got.Name != "ok" || got.URL != "../a2.git" || got.Update != "none" {
		t.Fatalf("module a = %+v", got)
	}
	if _, ok := modules["b"]; ok {
		t.Fatal("module with a .. name kept, want it ignored")
	}
	if got := modules["c"]; got.URL != "" {
		t.Fatalf("module c URL = %q, want an option-like URL ignored", got.URL)
	}
	if _, ok := modules["-d"]; ok {
		t.Fatal("module with an option-like path kept, want it ignored")
	}
}

// TestWriteTreeRefusesGitPathsInSubmodules pins that a submodule tree that
// names a .git path fails instead of writing it into the checkout.
func TestWriteTreeRefusesGitPathsInSubmodules(t *testing.T) {
	files := []object.File{{Name: "sub/.GIT/config"}}
	if err := rejectGitPaths(files); err == nil {
		t.Fatal("rejectGitPaths() error = nil, want the .git path refused")
	}
	if err := rejectGitPaths([]object.File{{Name: "sub/.github/x"}}); err != nil {
		t.Fatalf("rejectGitPaths(.github) error = %v", err)
	}
}
