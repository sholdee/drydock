package render

import (
	"strings"
	"testing"
)

func TestGeneratedRemoteRefNameGoldenContract(t *testing.T) {
	const remoteRef = "git::https://github.com/example/platform.git//deploy/base?ref=v1.2.3"

	ref, ok, err := parseKustomizeRemoteRef(remoteRef)
	if err != nil {
		t.Fatalf("parseKustomizeRemoteRef() error = %v", err)
	}
	if !ok {
		t.Fatal("parseKustomizeRemoteRef() ok = false, want true")
	}

	name := generatedRemoteRefName("000-001", ref)
	assertRemoteRefNameShapeAndRedaction(t, name, "v1.2.3")
	if got := generatedRemoteRefName("000-001", ref); got != name {
		t.Fatalf("generatedRemoteRefName() changed across calls: %q then %q", name, got)
	}

	next, ok, err := parseKustomizeRemoteRef("git::https://github.com/example/platform.git//deploy/base?ref=v1.2.4")
	if err != nil {
		t.Fatalf("parse next ref error = %v", err)
	}
	if !ok {
		t.Fatal("parse next ref ok = false, want true")
	}
	nextName := generatedRemoteRefName("000-001", next)
	assertRemoteRefNameShapeAndRedaction(t, nextName, "v1.2.4")
	if nextName == name {
		t.Fatalf("generated names are equal for different revisions: %q", name)
	}
}

func assertRemoteRefNameShapeAndRedaction(t *testing.T, name, revision string) {
	t.Helper()
	if !strings.HasPrefix(name, "000-001-base-") {
		t.Fatalf("generated name = %q, want 000-001-base-*", name)
	}
	for _, forbidden := range []string{"github.com", "platform", "deploy/", revision, "?ref", "git::"} {
		if strings.Contains(name, forbidden) {
			t.Fatalf("generated name %q leaked %q", name, forbidden)
		}
	}
}
