package render

import (
	"strings"
	"testing"
)

func TestParseKustomizeRemoteRef(t *testing.T) {
	tests := []struct {
		name     string
		ref      string
		kind     kustomizeRemoteKind
		url      string
		repoURL  string
		revision string
		subpath  string
	}{
		{
			name: "raw HTTP file",
			ref:  "https://raw.githubusercontent.com/org/repo/main/deploy.yaml",
			kind: kustomizeRemoteHTTPFile,
			url:  "https://raw.githubusercontent.com/org/repo/main/deploy.yaml",
		},
		{
			name:     "git URL ref",
			ref:      "https://github.com/org/repo//deploy/base?ref=v1.2.3",
			kind:     kustomizeRemoteGit,
			repoURL:  "https://github.com/org/repo",
			revision: "v1.2.3",
			subpath:  "deploy/base",
		},
		{
			name:     "github root shorthand ref",
			ref:      "https://github.com/org/repo?ref=v1",
			kind:     kustomizeRemoteGit,
			repoURL:  "https://github.com/org/repo",
			revision: "v1",
			subpath:  ".",
		},
		{
			name:     "github git suffix root shorthand ref",
			ref:      "https://github.com/org/repo.git?ref=v1",
			kind:     kustomizeRemoteGit,
			repoURL:  "https://github.com/org/repo.git",
			revision: "v1",
			subpath:  ".",
		},
		{
			name:     "github root shorthand defaults revision",
			ref:      "https://github.com/org/repo",
			kind:     kustomizeRemoteGit,
			repoURL:  "https://github.com/org/repo",
			revision: "HEAD",
			subpath:  ".",
		},
		{
			name:     "github shorthand ref",
			ref:      "https://github.com/argoproj/argo-cd/manifests/crds?ref=v3.4.2",
			kind:     kustomizeRemoteGit,
			repoURL:  "https://github.com/argoproj/argo-cd",
			revision: "v3.4.2",
			subpath:  "manifests/crds",
		},
		{
			name:     "git path segment shorthand ref",
			ref:      "https://git.example.test/platform/repo.git/deploy/base?ref=main",
			kind:     kustomizeRemoteGit,
			repoURL:  "https://git.example.test/platform/repo.git",
			revision: "main",
			subpath:  "deploy/base",
		},
		{
			name:     "git path segment root ref",
			ref:      "https://git.example.test/platform/repo.git?ref=main",
			kind:     kustomizeRemoteGit,
			repoURL:  "https://git.example.test/platform/repo.git",
			revision: "main",
			subpath:  ".",
		},
		{
			name:     "git URL ref defaults revision",
			ref:      "https://github.com/org/repo.git//deploy/base",
			kind:     kustomizeRemoteGit,
			repoURL:  "https://github.com/org/repo.git",
			revision: "HEAD",
			subpath:  "deploy/base",
		},
		{
			name:     "git prefix URL ref",
			ref:      "git::https://github.com/org/repo.git//deploy/base?ref=main",
			kind:     kustomizeRemoteGit,
			repoURL:  "https://github.com/org/repo.git",
			revision: "main",
			subpath:  "deploy/base",
		},
		{
			name:     "git prefix root URL ref",
			ref:      "git::https://host.example.test/org/repo.git?ref=v1",
			kind:     kustomizeRemoteGit,
			repoURL:  "https://host.example.test/org/repo.git",
			revision: "v1",
			subpath:  ".",
		},
		{
			name:     "git prefix root URL defaults revision",
			ref:      "git::https://host.example.test/org/repo.git",
			kind:     kustomizeRemoteGit,
			repoURL:  "https://host.example.test/org/repo.git",
			revision: "HEAD",
			subpath:  ".",
		},
		{
			name:     "ssh URL ref",
			ref:      "ssh://git@github.com/org/repo.git//deploy/base?ref=release",
			kind:     kustomizeRemoteGit,
			repoURL:  "ssh://git@github.com/org/repo.git",
			revision: "release",
			subpath:  "deploy/base",
		},
		{
			name:     "ssh root URL ref",
			ref:      "ssh://git@host.example.test/org/repo.git?ref=v1",
			kind:     kustomizeRemoteGit,
			repoURL:  "ssh://git@host.example.test/org/repo.git",
			revision: "v1",
			subpath:  ".",
		},
		{
			name:     "scp-like ref",
			ref:      "git@github.com:org/repo.git//deploy/base?ref=release",
			kind:     kustomizeRemoteGit,
			repoURL:  "git@github.com:org/repo.git",
			revision: "release",
			subpath:  "deploy/base",
		},
		{
			name:     "scp-like root ref",
			ref:      "git@host.example.test:org/repo.git?ref=v1",
			kind:     kustomizeRemoteGit,
			repoURL:  "git@host.example.test:org/repo.git",
			revision: "v1",
			subpath:  ".",
		},
		{
			name:     "scp-like root defaults revision",
			ref:      "git@host.example.test:org/repo.git",
			kind:     kustomizeRemoteGit,
			repoURL:  "git@host.example.test:org/repo.git",
			revision: "HEAD",
			subpath:  ".",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok, err := parseKustomizeRemoteRef(tt.ref)
			if err != nil {
				t.Fatalf("parseKustomizeRemoteRef() error = %v", err)
			}
			if !ok {
				t.Fatal("parseKustomizeRemoteRef() ok = false, want true")
			}
			if got.Kind != tt.kind || got.URL != tt.url || got.RepoURL != tt.repoURL || got.Revision != tt.revision || got.Subpath != tt.subpath {
				t.Fatalf("parseKustomizeRemoteRef() = %#v", got)
			}
		})
	}
}

func TestParseKustomizeRemoteRefIgnoresLocalRefs(t *testing.T) {
	for _, ref := range []string{
		"",
		"deployment.yaml",
		"./base",
		"../base",
		"overlays/prod?not-a-remote",
		"config: value",
	} {
		got, ok, err := parseKustomizeRemoteRef(ref)
		if err != nil {
			t.Fatalf("parseKustomizeRemoteRef(%q) error = %v", ref, err)
		}
		if ok {
			t.Fatalf("parseKustomizeRemoteRef(%q) = %#v, true; want false", ref, got)
		}
	}
}

func TestParseKustomizeRemoteRefRejectsAmbiguousHTTPGitLikeRefs(t *testing.T) {
	for _, ref := range []string{
		"https://example.test/org/repo?ref=v1",
		"https://example.test/org/repo",
		"https://example.test/org/repo/base?ref=v1",
	} {
		got, ok, err := parseKustomizeRemoteRef(ref)
		if err != nil {
			t.Fatalf("parseKustomizeRemoteRef(%q) error = %v", ref, err)
		}
		if ok {
			t.Fatalf("parseKustomizeRemoteRef(%q) = %#v, true; want false", ref, got)
		}
	}
}

func TestParseKustomizeRemoteRefRejectsSecretBearingUnsupportedQuery(t *testing.T) {
	for _, ref := range []string{
		"https://github.com/org/repo//base?token=secret&ref=main",
		"https://github.com/org/repo//base?secret=value&ref=main",
		"https://github.com/org/repo?token=secret&ref=main",
	} {
		_, ok, err := parseKustomizeRemoteRef(ref)
		if err == nil {
			t.Fatalf("parseKustomizeRemoteRef(%q) error = nil, want unsupported query error", ref)
		}
		if !ok {
			t.Fatalf("parseKustomizeRemoteRef(%q) ok = false, want true for rejected remote ref", ref)
		}
		assertDoesNotLeakKustomizeSecret(t, err.Error())
	}
}

func TestParseKustomizeRemoteRefRejectsUserinfo(t *testing.T) {
	for _, ref := range []string{
		"https://user:secret@example.test/file.yaml",
		"https://user:secret@github.com/org/repo//base?ref=main",
		"https://user:secret@github.com/org/repo?ref=main",
		"git::https://user:secret@github.com/org/repo.git//base?ref=main",
		"alice:secret@github.com:org/repo.git?ref=main",
	} {
		_, ok, err := parseKustomizeRemoteRef(ref)
		if err == nil {
			t.Fatalf("parseKustomizeRemoteRef(%q) error = nil, want userinfo error", ref)
		}
		if !ok {
			t.Fatalf("parseKustomizeRemoteRef(%q) ok = false, want true for rejected remote ref", ref)
		}
		assertDoesNotLeakKustomizeSecret(t, err.Error())
	}
}

func TestGeneratedRemoteRefNameRedactsSensitiveInput(t *testing.T) {
	ref, ok, err := parseKustomizeRemoteRef("https://github.com/org/repo//deploy/base?ref=secret-token")
	if err != nil {
		t.Fatalf("parseKustomizeRemoteRef() error = %v", err)
	}
	if !ok {
		t.Fatal("parseKustomizeRemoteRef() ok = false, want true")
	}
	got := generatedRemoteRefName("remote", ref)
	if got == "" {
		t.Fatal("generatedRemoteRefName() = empty")
	}
	assertDoesNotLeakKustomizeSecret(t, got)
}

func TestGeneratedRemoteRefNameIncludesRevisionIdentity(t *testing.T) {
	left, ok, err := parseKustomizeRemoteRef("https://github.com/org/repo//deploy/base?ref=v1")
	if err != nil || !ok {
		t.Fatalf("parse left = %#v, %v, want ok", left, err)
	}
	right, ok, err := parseKustomizeRemoteRef("https://github.com/org/repo//deploy/base?ref=v2")
	if err != nil || !ok {
		t.Fatalf("parse right = %#v, %v, want ok", right, err)
	}
	leftName := generatedRemoteRefName("remote", left)
	rightName := generatedRemoteRefName("remote", right)
	if leftName == rightName {
		t.Fatalf("generated names are equal: %q", leftName)
	}
	assertDoesNotLeakKustomizeSecret(t, leftName)
	assertDoesNotLeakKustomizeSecret(t, rightName)
}

func TestRedactKustomizeRemoteRef(t *testing.T) {
	tests := map[string]string{
		"https://user:secret@example.test/file.yaml?token=secret#fragment": "[remote-ref]",
		"https://github.com/org/repo//base?ref=secret#fragment":            "https://github.com/org/repo//base",
		"git::https://user:secret@github.com/org/repo.git//base?ref=main":  "[remote-ref]",
		"git@github.com:org/repo.git//base?ref=secret":                     "git@github.com:org/repo.git//base",
		"alice:secret@github.com:org/repo.git//base?ref=main":              "[remote-ref]",
		"./local?token=secret": "./local",
	}
	for ref, want := range tests {
		got := redactKustomizeRemoteRef(ref)
		if got != want {
			t.Fatalf("redactKustomizeRemoteRef(%q) = %q, want %q", ref, got, want)
		}
		assertDoesNotLeakKustomizeSecret(t, got)
	}
}

func assertDoesNotLeakKustomizeSecret(t *testing.T, got string) {
	t.Helper()
	for _, leaked := range []string{"secret", "token=", "user:secret", "alice:secret", "?token", "#fragment", "secret-fragment"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("%q leaked %q", got, leaked)
		}
	}
}
