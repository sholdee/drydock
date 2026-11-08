package gittree

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/sholdee/drydock/internal/gittree/submoduletest"
)

// TestOpenSubmoduleStoreLeavesStoreAloneForMissingCommit pins that a
// commit the remote does not have, such as an unpushed commit that a pull
// request pins, fails without cloning the store again: the store keeps its
// directory and gets no clone sibling.
func TestOpenSubmoduleStoreLeavesStoreAloneForMissingCommit(t *testing.T) {
	fixture := submoduletest.New(t)
	path := filepath.Join(t.TempDir(), "store")
	request := StoreRequest{Path: path, URL: fixture.GrandchildURL, Commit: plumbing.NewHash(fixture.GrandchildCommit), Transport: "file", AllowNetwork: true}
	if _, _, err := OpenSubmoduleStore(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	request.Commit = plumbing.NewHash(strings.Repeat("5", 40))
	if _, _, err := OpenSubmoduleStore(context.Background(), request); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("OpenSubmoduleStore(missing commit) error = %v, want not found", err)
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("store after a missing commit = %v, %v; want the same directory", after, err)
	}
	siblings, _ := filepath.Glob(path + ".clone-*")
	if len(siblings) != 0 {
		t.Fatalf("clone siblings = %q, want no clone", siblings)
	}
}

// TestRemoveStaleClonesReadsTheDirectory pins that the sweep before a
// clone works in a cache directory whose name holds glob characters.
func TestRemoveStaleClonesReadsTheDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache[a*")
	entry := filepath.Join(dir, "store")
	stale := entry + ".clone-1"
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * StaleCloneAge)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "storeX.clone-1")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(other, old, old); err != nil {
		t.Fatal(err)
	}
	removeStaleClones(entry, time.Now())
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stat stale clone = %v, want it removed", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("stat clone of another entry = %v, want it kept", err)
	}
}

// TestNewestChangeSeesDeepFiles pins that a fresh file deep in an old clone
// keeps the clone: a live clone writes below its top directory.
func TestNewestChangeSeesDeepFiles(t *testing.T) {
	entry := filepath.Join(t.TempDir(), "store")
	clone := entry + ".clone-1"
	deep := filepath.Join(clone, "objects", "pack")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deep, "pack-1.pack"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * StaleCloneAge)
	for _, p := range []string{deep, filepath.Dir(deep), clone} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	newest, err := NewestChange(clone)
	if err != nil || time.Since(newest) > time.Minute {
		t.Fatalf("NewestChange() = %v, %v; want the fresh file", newest, err)
	}
	removeStaleClones(entry, time.Now())
	if _, err := os.Stat(clone); err != nil {
		t.Fatalf("stat clone with a fresh file = %v, want it kept", err)
	}
}

// TestScrubRemoteUserinfoRewritesRawConfig pins that the scrub rewrites
// the raw configuration: a remote url behind a url.insteadOf rule, a
// pushurl, a submodule url, and the rule itself lose the token, through a
// rename that leaves no temporary file.
func TestScrubRemoteUserinfoRewritesRawConfig(t *testing.T) {
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	token := "ci:" + secretMarker + "@"
	config := fmt.Sprintf(`[core]
	bare = true
[url "https://%shost/"]
	insteadOf = https://%smirror.example/
[remote "origin"]
	url = https://%shost/org/repo.git
	pushurl = https://%shost/org/repo.git
	fetch = +refs/heads/*:refs/heads/*
[submodule "x"]
	url = https://%shost/org/x.git
`, token, token, token, token, token)
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ScrubRemoteUserinfo(repo); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "config"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secretMarker) || !strings.Contains(string(data), "url = https://host/org/repo.git") {
		t.Fatalf("config after the scrub:\n%s", data)
	}
	if temps, _ := filepath.Glob(filepath.Join(dir, "config.drydock-*")); len(temps) != 0 {
		t.Fatalf("temporary files = %q, want none", temps)
	}
}

// TestOpenSubmoduleStoreScrubsItsConfig pins that opening a store, also
// offline and when it holds the commit, removes a token from its config.
func TestOpenSubmoduleStoreScrubsItsConfig(t *testing.T) {
	fixture := submoduletest.New(t)
	path := filepath.Join(t.TempDir(), "store")
	request := StoreRequest{Path: path, URL: fixture.GrandchildURL, Commit: plumbing.NewHash(fixture.GrandchildCommit), Transport: "file", AllowNetwork: true}
	if _, _, err := OpenSubmoduleStore(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(path, "config")
	data, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	dirty := strings.Replace(string(data), "file://", "file://ci:"+secretMarker+"@", 1)
	if err := os.WriteFile(config, []byte(dirty), 0o644); err != nil {
		t.Fatal(err)
	}
	request.AllowNetwork = false
	if _, _, err := OpenSubmoduleStore(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(config); strings.Contains(string(data), secretMarker) {
		t.Fatalf("store config still holds the token:\n%s", data)
	}
}

// TestSubmoduleRefFormattingHidesUserinfo pins that %v, %+v, and %#v of a
// SubmoduleRef never print its userinfo or its URL with userinfo.
func TestSubmoduleRefFormattingHidesUserinfo(t *testing.T) {
	ref := SubmoduleRef{
		Path:        "sub",
		URL:         "https://host/org/repo.git",
		Userinfo:    URLUserinfo{Username: "ci", Password: secretMarker},
		resolvedURL: "https://ci:" + secretMarker + "@host/org/repo.git",
	}
	for _, format := range []string{"%v", "%+v", "%#v", "%s"} {
		if got := fmt.Sprintf(format, ref); strings.Contains(got, secretMarker) {
			t.Fatalf("Sprintf(%s) = %s, want no secret", format, got)
		}
		if got := fmt.Sprintf(format, &ref); strings.Contains(got, secretMarker) {
			t.Fatalf("Sprintf(%s, &ref) = %s, want no secret", format, got)
		}
	}
}

// TestHTTPUserIsDecodedLikeGoGit pins that an http user is compared as
// go-git decodes it, so an encoded user passes.
func TestHTTPUserIsDecodedLikeGoGit(t *testing.T) {
	if _, err := ClassifySubmoduleURL("https://us%65r:pw@host/org/repo.git", false); err != nil {
		t.Fatalf("ClassifySubmoduleURL(encoded user) error = %v", err)
	}
}
