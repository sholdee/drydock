package gittree

import (
	"context"
	"errors"
	"strings"
	"testing"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// errOpenerCalled marks that a checkout passed a URL to its opener.
var errOpenerCalled = errors.New("opener called")

// checkoutURL runs CheckoutSubmodules over a tree with one gitlink whose
// .gitmodules URL is value, and reports whether the URL reached the
// opener, and the URL the opener got.
func checkoutURL(t *testing.T, value string) (bool, string, error) {
	t.Helper()
	// Quoted, so "#" and ";" stay in the value instead of starting a
	// comment.
	quoted := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value)
	modules := "[submodule \"sub\"]\n\tpath = sub\n\turl = \"" + quoted + "\"\n"
	_, tree := memoryTree(t, map[string]string{".gitmodules": modules}, map[string]plumbing.Hash{"sub": plumbing.NewHash(strings.Repeat("3", 40))})
	var got string
	opener := SubmoduleOpenerFunc(func(_ context.Context, ref SubmoduleRef) (*git.Repository, error) {
		got = ref.URL
		return nil, errOpenerCalled
	})
	_, err := CheckoutSubmodules(context.Background(), SubmoduleRequest{Root: t.TempDir(), Tree: tree, RemoteURL: "https://git.example.test/org/parent.git", Opener: opener})
	return errors.Is(err, errOpenerCalled), got, err
}

// TestCheckoutSubmodulesRefusesURLsBeforeTheyLoseUserinfo pins that the
// real checkout checks a submodule URL as git reads it, userinfo
// included, before it strips the userinfo: each of these reached the
// opener when the userinfo was stripped first.
func TestCheckoutSubmodulesRefusesURLsBeforeTheyLoseUserinfo(t *testing.T) {
	for _, value := range []string{
		"https://u#@h.invalid/org/repo.git",
		"https://u?@h.invalid/org/repo.git",
		"https://u\\@h.invalid/org/repo.git",
		"ssh://user:pass@host/org/repo.git",
		"ssh://user:pass@host:22/org/repo.git",
		"git://git@host/org/repo.git",
		"git://-oProxyCommand=x@host/org/repo.git",
		// git reads "@[" after the authority as the host (item 3).
		"ssh://evil.invalid/@[192.0.2.10]/org/repo.git",
		"git://evil.invalid/@[192.0.2.10]/org/repo.git",
		// A query or fragment can carry a token (item 5).
		"https://host/org/repo.git?private_token=abc",
		"https://host/org/repo.git#frag",
	} {
		if called, got, err := checkoutURL(t, value); called || err == nil {
			t.Fatalf("checkout of %q reached the opener with %q (error %v), want the URL refused", value, got, err)
		}
	}
}

// TestCheckoutSubmodulesRefusesFuzzMismatchesInTheRealFlow runs the fuzz
// table of TestClassifySubmoduleURLRefusesFuzzMismatches through
// CheckoutSubmodules.
func TestCheckoutSubmodulesRefusesFuzzMismatchesInTheRealFlow(t *testing.T) {
	for _, value := range fuzzMismatchURLs {
		if strings.ContainsAny(value, "\x00\n") {
			continue // .gitmodules cannot hold them.
		}
		if called, got, err := checkoutURL(t, value); called || err == nil {
			t.Fatalf("checkout of %q reached the opener with %q (error %v), want the URL refused", value, got, err)
		}
	}
}

// TestCheckoutSubmodulesAllowsCommonURLs pins that common submodule URLs,
// an http user and password, and a percent-encoded http path reach the
// opener, without userinfo.
func TestCheckoutSubmodulesAllowsCommonURLs(t *testing.T) {
	for value, want := range map[string]string{
		"https://dev.azure.com/org/My%20Project/_git/repo":    "https://dev.azure.com/org/My%20Project/_git/repo",
		"https://ci-token:secret@gitlab.example.test/a/b.git": "https://gitlab.example.test/a/b.git",
		"https://github.com/org/repo.git":                     "https://github.com/org/repo.git",
		"ssh://git@github.com/org/repo.git":                   "ssh://git@github.com/org/repo.git",
		"git@github.com:org/repo.git":                         "git@github.com:org/repo.git",
		"git://host.example.test/repo.git":                    "git://host.example.test/repo.git",
		"../sibling.git":                                      "https://git.example.test/org/sibling.git",
	} {
		called, got, err := checkoutURL(t, value)
		if !called || got != want {
			t.Fatalf("checkout of %q: opener called %t with %q, error %v; want %q", value, called, got, err, want)
		}
	}
}
