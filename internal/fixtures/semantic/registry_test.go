package semantic_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/fixtures/semantic"
)

func TestCasesAreWellFormed(t *testing.T) {
	t.Parallel()

	seen := map[string]struct{}{}
	for _, tc := range semantic.Cases() {
		if strings.TrimSpace(tc.ID) == "" {
			t.Fatalf("case has empty ID: %#v", tc)
		}
		if _, ok := seen[tc.ID]; ok {
			t.Fatalf("duplicate fixture ID %q", tc.ID)
		}
		seen[tc.ID] = struct{}{}

		if strings.TrimSpace(tc.Phase) == "" {
			t.Fatalf("%s has empty phase", tc.ID)
		}
		if strings.TrimSpace(tc.Category) == "" {
			t.Fatalf("%s has empty category", tc.ID)
		}
		if !strings.HasPrefix(tc.FixturePath, "testdata/semantic-remediation/") {
			t.Fatalf("%s fixture path %q must be under testdata/semantic-remediation", tc.ID, tc.FixturePath)
		}
		if filepath.IsAbs(tc.FixturePath) {
			t.Fatalf("%s fixture path %q must be relative", tc.ID, tc.FixturePath)
		}
		switch tc.Status {
		case semantic.StatusActive:
		case semantic.StatusPending, semantic.StatusDocumentedBoundary:
			if strings.TrimSpace(tc.Reason) == "" {
				t.Fatalf("%s status %q requires a reason", tc.ID, tc.Status)
			}
			if strings.TrimSpace(tc.VerificationScope) == "" {
				t.Fatalf("%s status %q requires a verification scope", tc.ID, tc.Status)
			}
		default:
			t.Fatalf("%s has invalid status %q", tc.ID, tc.Status)
		}
	}
}

func TestFixturePathsExist(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	for _, tc := range semantic.Cases() {
		path := filepath.Join(root, filepath.FromSlash(tc.FixturePath))
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("%s fixture path %s: %v", tc.ID, tc.FixturePath, err)
		}
		if !info.IsDir() {
			t.Fatalf("%s fixture path %s is not a directory", tc.ID, tc.FixturePath)
		}
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		next := filepath.Dir(dir)
		if next == dir {
			t.Fatal("could not find repo root")
		}
		dir = next
	}
}
