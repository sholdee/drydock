package drydock

import (
	"context"
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
