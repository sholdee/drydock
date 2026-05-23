package source

import (
	"strings"
	"testing"
)

func TestResolverUsesRepoMapBeforeNetwork(t *testing.T) {
	resolver := NewResolver(Options{
		RepoMaps: []RepoMap{{
			URL:  "https://github.com/example/repo",
			Path: "/work/current",
		}},
		AllowNetwork: true,
	})

	resolved, err := resolver.Resolve("https://github.com/example/repo.git", "main")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.LocalPath != "/work/current" {
		t.Fatalf("LocalPath = %s, want /work/current", resolved.LocalPath)
	}
	if resolved.DeclaredRevision != "main" {
		t.Fatalf("DeclaredRevision = %s, want main", resolved.DeclaredRevision)
	}
	if !resolved.Mapped {
		t.Fatalf("Mapped = false, want true")
	}
	if resolved.Network {
		t.Fatalf("Network = true, want false")
	}
}

func TestResolverNormalizesRepoMapURLs(t *testing.T) {
	resolver := NewResolver(Options{
		RepoMaps: []RepoMap{{
			URL:  " https://github.com/example/repo/ ",
			Path: "/work/current",
		}},
	})

	resolved, err := resolver.Resolve(" https://github.com/example/repo.git/ ", "feature")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.NormalizedURL != "https://github.com/example/repo" {
		t.Fatalf("NormalizedURL = %q, want %q", resolved.NormalizedURL, "https://github.com/example/repo")
	}
	if resolved.LocalPath != "/work/current" {
		t.Fatalf("LocalPath = %s, want /work/current", resolved.LocalPath)
	}
	if resolved.DeclaredRevision != "feature" {
		t.Fatalf("DeclaredRevision = %s, want feature", resolved.DeclaredRevision)
	}
}

func TestResolverRejectsUnmappedWithoutNetwork(t *testing.T) {
	resolver := NewResolver(Options{})

	_, err := resolver.Resolve("https://github.com/example/other", "main")
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestResolverRedactsUnmappedRepositoryError(t *testing.T) {
	resolver := NewResolver(Options{})

	_, err := resolver.Resolve("https://user:secret@github.com/example/private.git?token=abc123#frag", "main")
	if err == nil {
		t.Fatalf("expected error")
	}
	for _, leaked := range []string{"user", "secret", "token", "abc123", "frag"} {
		if strings.Contains(err.Error(), leaked) {
			t.Fatalf("error = %q, leaked %q", err.Error(), leaked)
		}
	}
	if !strings.Contains(err.Error(), "https://github.com/example/private.git") {
		t.Fatalf("error = %q, want redacted repository URL", err.Error())
	}
}

func TestRedactURLStripsSensitiveParts(t *testing.T) {
	got := RedactURL(" https://user:secret@example.com/org/repo.git?access_token=abc#fragment ")
	if got != "https://example.com/org/repo.git" {
		t.Fatalf("RedactURL() = %q", got)
	}
}

func TestResolverAllowsNetwork(t *testing.T) {
	resolver := NewResolver(Options{AllowNetwork: true})

	resolved, err := resolver.Resolve("https://github.com/example/other", "main")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !resolved.Network {
		t.Fatalf("Network = false, want true")
	}
	if resolved.Mapped {
		t.Fatalf("Mapped = true, want false")
	}
	if resolved.DeclaredRevision != "main" {
		t.Fatalf("DeclaredRevision = %s, want main", resolved.DeclaredRevision)
	}
}
