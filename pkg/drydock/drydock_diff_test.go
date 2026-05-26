package drydock

import (
	"context"
	"strings"
	"testing"
)

func TestDiffApplications(t *testing.T) {
	left, right := writeDiffTrees(t, "v1", "v2")

	result, err := DiffApplications(context.Background(), Config{
		PathOrig:    left,
		Path:        right,
		ChangedOnly: boolPtr(false),
	})
	if err != nil {
		t.Fatalf("DiffApplications() error = %v", err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("Results = %d, want 1", len(result.Results))
	}
	if result.Results[0].Change != "modified" {
		t.Fatalf("Change = %q, want modified", result.Results[0].Change)
	}
}
func TestPublicDiffApplicationsParallelismPreservesResults(t *testing.T) {
	left, right := writeDiffTrees(t, "v1", "v2")

	result, err := DiffApplications(context.Background(), Config{
		PathOrig:    left,
		Path:        right,
		ChangedOnly: boolPtr(false),
		Parallelism: 2,
	})
	if err != nil {
		t.Fatalf("DiffApplications() error = %v", err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("Results = %d, want 1", len(result.Results))
	}
	if result.Results[0].Change != "modified" {
		t.Fatalf("Change = %q, want modified", result.Results[0].Change)
	}
}

func TestPublicDiffApplicationsRefOrig(t *testing.T) {
	root := t.TempDir()
	repo, wt := initPublicGitRepo(t, root)
	writeAPIAppTree(t, root, "demo", configMapBody("demo", "baseline"))
	commitPublicGitRepo(t, repo, wt, "baseline")
	writeAPIAppTree(t, root, "demo", configMapBody("demo", "working"))

	result, err := DiffApplications(context.Background(), Config{
		Path:        root,
		Repo:        root,
		RefOrig:     "HEAD",
		ChangedOnly: boolPtr(false),
	})
	if err != nil {
		t.Fatalf("DiffApplications() error = %v", err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("Results = %d, want one diff", len(result.Results))
	}
	if !strings.Contains(result.Results[0].Diff, "+  version: working") {
		t.Fatalf("Diff = %q, want working tree value", result.Results[0].Diff)
	}
}

func TestPublicDiffApplicationsRefAndRefOrig(t *testing.T) {
	root := t.TempDir()
	repo, wt := initPublicGitRepo(t, root)
	writeAPIAppTree(t, root, "demo", configMapBody("demo", "baseline"))
	commitPublicGitRepo(t, repo, wt, "baseline")
	checkoutPublicGitBranch(t, wt, "feature")
	writeAPIAppTree(t, root, "demo", configMapBody("demo", "feature"))
	commitPublicGitRepo(t, repo, wt, "feature")
	writeAPIAppTree(t, root, "demo", configMapBody("demo", "uncommitted"))

	result, err := DiffApplications(context.Background(), Config{
		Repo:        root,
		RefOrig:     "master",
		Ref:         "feature",
		ChangedOnly: boolPtr(false),
	})
	if err != nil {
		t.Fatalf("DiffApplications() error = %v", err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("Results = %d, want one diff", len(result.Results))
	}
	if !strings.Contains(result.Results[0].Diff, "-  version: baseline") {
		t.Fatalf("Diff = %q, want baseline ref value", result.Results[0].Diff)
	}
	if !strings.Contains(result.Results[0].Diff, "+  version: feature") {
		t.Fatalf("Diff = %q, want feature ref value", result.Results[0].Diff)
	}
	if strings.Contains(result.Results[0].Diff, "uncommitted") {
		t.Fatalf("Diff = %q, want committed ref diff only", result.Results[0].Diff)
	}
}

func TestDiffImages(t *testing.T) {
	left, right := writeImageDiffTrees(t, "repo/demo:v1", "repo/demo:v2")

	result, err := DiffImages(context.Background(), Config{
		PathOrig:    left,
		Path:        right,
		ChangedOnly: boolPtr(false),
	})
	if err != nil {
		t.Fatalf("DiffImages() error = %v", err)
	}
	if !containsString(result.Removed, "repo/demo:v1") {
		t.Fatalf("Removed = %#v, want repo/demo:v1", result.Removed)
	}
	if !containsString(result.Added, "repo/demo:v2") {
		t.Fatalf("Added = %#v, want repo/demo:v2", result.Added)
	}
}
func TestPublicDiffImagesParallelismPreservesResults(t *testing.T) {
	left, right := writeImageDiffTrees(t, "repo/demo:v1", "repo/demo:v2")

	result, err := DiffImages(context.Background(), Config{
		PathOrig:    left,
		Path:        right,
		ChangedOnly: boolPtr(false),
		Parallelism: 2,
	})
	if err != nil {
		t.Fatalf("DiffImages() error = %v", err)
	}
	if !containsString(result.Removed, "repo/demo:v1") {
		t.Fatalf("Removed = %#v, want repo/demo:v1", result.Removed)
	}
	if !containsString(result.Added, "repo/demo:v2") {
		t.Fatalf("Added = %#v, want repo/demo:v2", result.Added)
	}
}
