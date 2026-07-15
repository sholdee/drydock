package source

import "testing"

func TestCanonicalGitURLKey(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  string
		want string
	}{
		{name: "https", raw: "https://github.com/x/y", want: "github.com/x/y"},
		{name: "https with .git", raw: "https://github.com/x/y.git", want: "github.com/x/y"},
		{name: "trailing slash", raw: "https://github.com/x/y/", want: "github.com/x/y"},
		{name: "trailing slash after .git", raw: "https://github.com/x/y.git/", want: "github.com/x/y"},
		{name: "ssh with user and port", raw: "ssh://git@github.com:22/x/y.git", want: "github.com/x/y"},
		{name: "scp-style", raw: "git@github.com:x/y.git", want: "github.com/x/y"},
		{name: "host lowercased", raw: "https://GITHUB.COM/x/y", want: "github.com/x/y"},
		{name: "path case preserved", raw: "https://github.com/Example/Repo.git", want: "github.com/Example/Repo"},
		{name: "userinfo stripped", raw: "https://user:secret@github.com/x/y.git", want: "github.com/x/y"},
		{name: "whitespace trimmed", raw: "  https://github.com/x/y.git  ", want: "github.com/x/y"},
		{name: "empty", raw: "", want: ""},
		{name: "host only", raw: "https://github.com", want: "github.com"},
		{name: "bracketed ipv6 without port", raw: "ssh://git@[::1]/x/y", want: "[::1]/x/y"},
		{name: "bracketed ipv6 with port", raw: "ssh://git@[::1]:2222/x/y.git", want: "[::1]/x/y"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := CanonicalGitURLKey(tt.raw); got != tt.want {
				t.Fatalf("CanonicalGitURLKey(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestCanonicalGitURLKeyEquatesURLForms(t *testing.T) {
	forms := []string{
		"ssh://git@github.com/x/y",
		"git@github.com:x/y.git",
		"https://github.com/x/y",
		"https://github.com/x/y.git",
	}
	want := CanonicalGitURLKey(forms[0])
	if want == "" {
		t.Fatalf("CanonicalGitURLKey(%q) = empty", forms[0])
	}
	for _, form := range forms[1:] {
		if got := CanonicalGitURLKey(form); got != want {
			t.Fatalf("CanonicalGitURLKey(%q) = %q, want %q", form, got, want)
		}
	}
}

func TestIsCommitSHA(t *testing.T) {
	for _, tt := range []struct {
		name string
		rev  string
		want bool
	}{
		{name: "40-hex", rev: "0123456789abcdef0123456789abcdef01234567", want: true},
		{name: "64-hex", rev: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", want: true},
		{name: "39-hex", rev: "0123456789abcdef0123456789abcdef0123456", want: false},
		{name: "short sha", rev: "abc1234", want: false},
		{name: "branch", rev: "main", want: false},
		{name: "HEAD", rev: "HEAD", want: false},
		{name: "uppercase hex", rev: "0123456789ABCDEF0123456789ABCDEF01234567", want: false},
		{name: "empty", rev: "", want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsCommitSHA(tt.rev); got != tt.want {
				t.Fatalf("IsCommitSHA(%q) = %v, want %v", tt.rev, got, tt.want)
			}
		})
	}
}

func TestIsDefaultRevision(t *testing.T) {
	for _, tt := range []struct {
		name string
		rev  string
		want bool
	}{
		{name: "empty", rev: "", want: true},
		{name: "HEAD", rev: "HEAD", want: true},
		{name: "whitespace HEAD", rev: "  HEAD  ", want: true},
		{name: "lowercase head", rev: "head", want: false},
		{name: "branch", rev: "main", want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsDefaultRevision(tt.rev); got != tt.want {
				t.Fatalf("IsDefaultRevision(%q) = %v, want %v", tt.rev, got, tt.want)
			}
		})
	}
}
