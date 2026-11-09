package gittree

import (
	"context"
	"errors"
	"net/url"
	"path/filepath"
	"testing"

	git "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/sholdee/drydock/internal/gittree/submoduletest"
)

// TestSplitURLUserinfoReadsTheHostOfNetURL checks that the URL that
// SplitURLUserinfo returns has the host that net/url reads from the input.
// The cache key comes from net/url and the clone from the clean URL, so a
// different host lets a URL fill the cache entry of another host. Before
// the fix, "https://a.test?@b.test/x" came back as "https://b.test/x".
func TestSplitURLUserinfoReadsTheHostOfNetURL(t *testing.T) {
	for _, raw := range []string{
		"https://a.test?@b.test/org/repo.git",
		"https://a.test#@b.test/org/repo.git",
		"https://a.test?x=@b.test",
		"https://user:secret@a.test/org/repo.git",
		"https://user:secret@a.test?q=1",
		"ssh://git:secret@a.test/org/repo.git",
		"http://a.test:8080/org/repo.git",
	} {
		t.Run(raw, func(t *testing.T) {
			clean, _, _ := SplitURLUserinfo(raw)
			want, err := url.Parse(raw)
			if err != nil {
				t.Fatalf("url.Parse(%q) error = %v", raw, err)
			}
			got, err := url.Parse(clean)
			if err != nil {
				t.Fatalf("url.Parse(clean %q) error = %v", clean, err)
			}
			if got.Host != want.Host {
				t.Fatalf("SplitURLUserinfo(%q) = %q with host %q, want the net/url host %q", raw, clean, got.Host, want.Host)
			}
		})
	}
}

// TestCheckURLAuthority checks that a URL with "?", "#", or "\" before the
// first "/" after "://" fails, and that ordinary repository URLs pass.
func TestCheckURLAuthority(t *testing.T) {
	for _, raw := range []string{
		"https://a.test?@b.test/org/repo.git",
		"https://a.test#@b.test/org/repo.git",
		"https://a.test?",
		`https://a.test\@b.test/org/repo.git`,
		"ssh://git@a.test#x/org/repo.git",
		"https://ci:p?ss@github.com/org/repo.git",
	} {
		if err := CheckURLAuthority(raw); err == nil {
			t.Errorf("CheckURLAuthority(%q) = nil, want an error", raw)
		}
	}
	for _, raw := range []string{
		"https://github.com/org/repo.git",
		"https://user:secret@github.com/org/repo.git",
		"https://ci:p%3Fss%23@github.com/org/repo.git",
		"https://github.com/org/repo.git?ref=main#frag",
		"http://127.0.0.1:8080/repo.git",
		"ssh://git@github.com/org/repo.git",
		"git@github.com:org/repo.git",
		"file:///srv/repo.git",
	} {
		if err := CheckURLAuthority(raw); err != nil {
			t.Errorf("CheckURLAuthority(%q) = %v, want nil", raw, err)
		}
	}
}

// TestOpenSubmoduleStoreRefusesAStoreOfAnotherRepository checks a store
// whose origin names another repository than the request, as a cache entry
// written under a wrong key or changed on disk does. Offline, drydock does
// not serve its content. Online, it clones the request URL into the entry
// again, so it never fetches the other origin.
func TestOpenSubmoduleStoreRefusesAStoreOfAnotherRepository(t *testing.T) {
	fixture := submoduletest.New(t)
	commit := plumbing.NewHash(fixture.GrandchildCommit)
	path := filepath.Join(t.TempDir(), "store")
	// The entry of the grandchild URL holds a clone of the child.
	if _, err := git.PlainClone(path, true, &git.CloneOptions{URL: fixture.ChildURL}); err != nil {
		t.Fatal(err)
	}
	request := StoreRequest{Path: path, URL: fixture.GrandchildURL, Commit: plumbing.NewHash(fixture.ChildCommit), Transport: "file"}
	if _, _, err := OpenSubmoduleStore(context.Background(), request); !errors.Is(err, ErrOfflineCacheMiss) {
		t.Fatalf("offline OpenSubmoduleStore() error = %v, want ErrOfflineCacheMiss for a commit of the other repository", err)
	}
	request.Commit = commit
	request.AllowNetwork = true
	repo, fetched, err := OpenSubmoduleStore(context.Background(), request)
	if err != nil || !fetched {
		t.Fatalf("OpenSubmoduleStore() = fetched %t, error %v; want a new clone", fetched, err)
	}
	if !OriginMatches(repo, func(origin string) bool { return origin == fixture.GrandchildURL }) {
		t.Fatal("the store origin is not the request URL after the new clone")
	}
	if !hasCommit(repo, commit) {
		t.Fatal("the store lacks the commit of the request")
	}
}

// TestOriginMatchesNeedsEveryOriginURL checks that OriginMatches accepts
// an entry only when every URL of its origin remote names the repository,
// and refuses an entry without an origin remote or without origin URLs.
func TestOriginMatchesNeedsEveryOriginURL(t *testing.T) {
	const want = "https://git.example.test/org/repo.git"
	same := func(origin string) bool { return origin == want }
	for _, tc := range []struct {
		name string
		urls []string
		ok   bool
	}{
		{name: "one matching URL", urls: []string{want}, ok: true},
		{name: "matching URL first", urls: []string{want, "https://other.example.test/org/repo.git"}},
		{name: "matching URL last", urls: []string{"https://other.example.test/org/repo.git", want}},
		{name: "no origin remote"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, err := git.PlainInit(t.TempDir(), true)
			if err != nil {
				t.Fatal(err)
			}
			if len(tc.urls) > 0 {
				if _, err := repo.CreateRemote(&gitconfig.RemoteConfig{Name: "origin", URLs: tc.urls}); err != nil {
					t.Fatal(err)
				}
			}
			if got := OriginMatches(repo, same); got != tc.ok {
				t.Fatalf("OriginMatches() = %t, want %t", got, tc.ok)
			}
		})
	}
}
