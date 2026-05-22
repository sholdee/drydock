package source

import "testing"

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
