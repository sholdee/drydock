package gittree

import (
	"context"
	"errors"
	"strings"
	"testing"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// secretMarker stands for a token in the userinfo of a submodule URL.
const secretMarker = "MARKER-SECRET-4242"

// secretRefusals are submodule URLs with secretMarker in their userinfo
// that drydock refuses, one for each refusal path.
var secretRefusals = []string{
	"https://user:" + secretMarker + "@host/org/repo.git#frag",
	"https://user:" + secretMarker + "@host/org/repo.git?q",
	"https://user:" + secretMarker + "@host/org/a%3Fb.git",
	"https://user:" + secretMarker + "@host:99999/org/repo.git",
	"https://user:" + secretMarker + "@host:abc/org/repo.git",
	"https://user:" + secretMarker + "@-host/org/repo.git",
	"https://user:" + secretMarker + "@@host/org/repo.git",
	"https://user:" + secretMarker + "@ho%41st/org/repo.git",
	"https://user:" + secretMarker + "@host\\x/org/repo.git",
	"https://user:" + secretMarker + "@host/",
	"https://" + secretMarker + "@-host/org/repo.git",
	"HTTPS://user:" + secretMarker + "@host/org/repo.git",
	"ssh://user:" + secretMarker + "@host/org/repo.git",
	"ssh://user:" + secretMarker + "@host:22/org/repo.git",
	"ssh://" + secretMarker + "@[host]/org/repo.git",
	"git://user:" + secretMarker + "@host/org/repo.git",
	"git://" + secretMarker + "@host/org/repo.git",
	"user:" + secretMarker + "@host:org/repo.git",
	"user:" + secretMarker + "@[host]/repo.git",
	"git@host::" + secretMarker,
	"host:-" + secretMarker,
	"host:x~" + secretMarker,
}

// TestRefusalsNeverShowUserinfo pins that no refusal message shows the
// user or password of a submodule URL, through ClassifySubmoduleURL and
// through CheckoutSubmodules, whose errors reach every source error of a
// snapshot.
func TestRefusalsNeverShowUserinfo(t *testing.T) {
	for _, value := range secretRefusals {
		_, err := ClassifySubmoduleURL(value, false)
		if err == nil {
			t.Fatalf("ClassifySubmoduleURL(%q) error = nil, want it refused", value)
		}
		if strings.Contains(err.Error(), secretMarker) {
			t.Fatalf("ClassifySubmoduleURL(%q) error shows the secret: %v", value, err)
		}
		called, _, err := checkoutURL(t, value)
		if called || err == nil || strings.Contains(err.Error(), secretMarker) {
			t.Fatalf("checkout of %q: opener called %t, error %v; want a refusal without the secret", value, called, err)
		}
	}
}

// TestHTTPPathEscapesThatGoGitSendsDifferentlyAreRefused pins that an
// encoded "?", "#", or "%" in an http path is refused: go-git decodes the
// path and sends another one than git, while an encoded space passes.
func TestHTTPPathEscapesThatGoGitSendsDifferentlyAreRefused(t *testing.T) {
	for _, value := range []string{
		"https://host/org/a%3Fb.git",
		"https://host/org/a%23b.git",
		"https://host/org/a%25b.git",
		"https://host/org/a%zz.git",
	} {
		if _, err := ClassifySubmoduleURL(value, false); err == nil {
			t.Fatalf("ClassifySubmoduleURL(%q) error = nil, want it refused", value)
		}
	}
	if _, err := ClassifySubmoduleURL("https://dev.azure.com/org/My%20Project/_git/repo", false); err != nil {
		t.Fatalf("ClassifySubmoduleURL(encoded space) error = %v", err)
	}
}

// TestPercentInUserinfoPasses pins that a percent-encoded password, as a
// relative URL of a superproject cloned with a token inherits it, passes,
// and that the decoded password becomes the userinfo of the submodule.
func TestPercentInUserinfoPasses(t *testing.T) {
	if _, err := ClassifySubmoduleURL("https://tok:p%40ss@host/org/repo.git", false); err != nil {
		t.Fatalf("ClassifySubmoduleURL() error = %v", err)
	}
	clean, info, found := SplitURLUserinfo("https://tok:p%40ss@host/org/repo.git")
	if !found || clean != "https://host/org/repo.git" || info.Username != "tok" || info.Password != "p@ss" {
		t.Fatalf("SplitURLUserinfo() = %q, %+v, %t", clean, info, found)
	}
	if _, err := ClassifySubmoduleURL("https://tok:pass@ho%41st/org/repo.git", false); err == nil {
		t.Fatal("ClassifySubmoduleURL(percent in host) error = nil, want it refused")
	}
}

// TestNestedRelativeURLsKeepTheInheritedUserinfo pins that a nested
// relative submodule URL resolves against its parent's URL with userinfo,
// as git resolves it, so a grandchild of a superproject cloned with a
// token gets the token as in-scope userinfo, and its URL never holds it.
func TestNestedRelativeURLsKeepTheInheritedUserinfo(t *testing.T) {
	grandchild := plumbing.NewHash(strings.Repeat("4", 40))
	child, childCommit := memoryCommit(t, map[string]string{".gitmodules": "[submodule \"deep\"]\n\tpath = deep\n\turl = ../grand.git\n"}, map[string]plumbing.Hash{"deep": grandchild})
	_, tree := memoryTree(t, map[string]string{".gitmodules": "[submodule \"sub\"]\n\tpath = sub\n\turl = ../child.git\n"}, map[string]plumbing.Hash{"sub": childCommit})
	var nested SubmoduleRef
	opener := SubmoduleOpenerFunc(func(_ context.Context, ref SubmoduleRef) (*git.Repository, error) {
		if ref.Path == "sub" {
			return child, nil
		}
		nested = ref
		return nil, errOpenerCalled
	})
	_, err := CheckoutSubmodules(context.Background(), SubmoduleRequest{Root: t.TempDir(), Tree: tree, RemoteURL: "https://ci:tok@git.example.test/org/parent.git", Opener: opener})
	if !errors.Is(err, errOpenerCalled) {
		t.Fatalf("CheckoutSubmodules() error = %v, want the grandchild opened", err)
	}
	if nested.URL != "https://git.example.test/org/grand.git" || nested.Userinfo != (URLUserinfo{Username: "ci", Password: "tok"}) {
		t.Fatalf("grandchild ref = %q with %+v, want the clean URL and the inherited userinfo", nested.URL, nested.Userinfo)
	}
}
