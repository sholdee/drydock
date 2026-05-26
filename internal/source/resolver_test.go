package source

import "testing"

func TestResolverUsesRepoMapBeforeNetwork(t *testing.T) {
	resolver := NewResolver(Options{
		RepoMaps: []RepoMap{{
			URL:  "https://github.com/example/repo",
			Path: "/work/current",
		}},
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

func TestNormalizeURLDoesNotDoubleAppendGitSuffix(t *testing.T) {
	got := NormalizeURL("https://github.com/example/repo.git")
	if got != "https://github.com/example/repo" {
		t.Fatalf("NormalizeURL() = %q, want https://github.com/example/repo", got)
	}
}

func TestResolverDefaultsUnmappedRepositoryToNetwork(t *testing.T) {
	resolver := NewResolver(Options{})

	resolved, err := resolver.Resolve("https://github.com/example/other.git", "main")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !resolved.Network {
		t.Fatalf("Network = false, want true")
	}
	if resolved.Mapped {
		t.Fatalf("Mapped = true, want false")
	}
	if resolved.NormalizedURL != "https://github.com/example/other" {
		t.Fatalf("NormalizedURL = %q, want normalized URL", resolved.NormalizedURL)
	}
	if resolved.DeclaredRevision != "main" {
		t.Fatalf("DeclaredRevision = %s, want main", resolved.DeclaredRevision)
	}
}

func TestResolverOfflineLeavesUnmappedRepositoryCacheResolvable(t *testing.T) {
	resolver := NewResolver(Options{Offline: true})

	resolved, err := resolver.Resolve("https://github.com/example/other.git", "main")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.Network {
		t.Fatalf("Network = true, want false for offline cache-only resolution")
	}
	if resolved.Mapped {
		t.Fatalf("Mapped = true, want false")
	}
	if resolved.NormalizedURL != "https://github.com/example/other" {
		t.Fatalf("NormalizedURL = %q, want normalized URL", resolved.NormalizedURL)
	}
}

func TestRedactURLStripsSensitiveParts(t *testing.T) {
	got := RedactURL(" https://user:secret@example.com/org/repo.git?access_token=abc#fragment ")
	if got != "https://example.com/org/repo.git" {
		t.Fatalf("RedactURL() = %q", got)
	}
}

func TestRedactURLStripsSCPUserAndSensitiveParts(t *testing.T) {
	got := RedactURL(" user@github.com:org/repo.git?token=secret#fragment ")
	if got != "github.com:org/repo.git" {
		t.Fatalf("RedactURL() = %q", got)
	}
}

func TestRedactURLStripsEmbeddedSchemeUserAndSensitiveParts(t *testing.T) {
	got := RedactURL(" git::https://user:secret@example.com/org/repo.git?token=secret#fragment ")
	if got != "git::https://example.com/org/repo.git" {
		t.Fatalf("RedactURL() = %q", got)
	}
}

func TestRedactURLStripsOpaqueSchemeUserAndSensitiveParts(t *testing.T) {
	got := RedactURL(" https:user:secret@example.com/org/repo.git?token=secret#fragment ")
	if got != "https:example.com/org/repo.git" {
		t.Fatalf("RedactURL() = %q", got)
	}
}

func TestResolverAllowsNetwork(t *testing.T) {
	resolver := NewResolver(Options{})

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
