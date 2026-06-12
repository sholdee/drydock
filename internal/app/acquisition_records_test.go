package app

import (
	"context"
	"reflect"
	"testing"

	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/render"
	sourcepkg "github.com/sholdee/drydock/internal/source"
)

func TestResolveSourceRootRecordsGitAcquisition(t *testing.T) {
	acquired := t.TempDir()
	collector := cacheevent.NewAcquisitionCollector()
	provider := localProvider{
		repoRoot:       t.TempDir(),
		sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{}),
		gitAcquirer: &recordingGitAcquirer{
			path:     acquired,
			revision: "0123456789abcdef0123456789abcdef01234567",
		},
		acquisitions: collector,
	}

	root, err := provider.resolveSourceRoot(context.Background(), render.ResolvedSource{
		RepoURL:        "https://git.example.test/org/repo.git",
		Path:           "manifests/demo",
		TargetRevision: "main",
	})
	if err != nil {
		t.Fatalf("resolveSourceRoot() error = %v", err)
	}
	if root != acquired {
		t.Fatalf("resolveSourceRoot() = %s, want %s", root, acquired)
	}
	want := []cacheevent.AcquisitionRecord{{
		Kind:              cacheevent.AcquisitionGit,
		RequestedRevision: "main",
		ResolvedRevision:  "0123456789abcdef0123456789abcdef01234567",
	}}
	if got := collector.Records(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Records() = %#v, want %#v", got, want)
	}
}

func TestResolveSourceRootLocalPathRecordsNothing(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root+"/manifests/demo/kustomization.yaml", "kind: Kustomization\n")
	collector := cacheevent.NewAcquisitionCollector()
	provider := localProvider{
		repoRoot:       root,
		sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{}),
		acquisitions:   collector,
	}

	if _, err := provider.resolveSourceRoot(context.Background(), render.ResolvedSource{
		RepoURL:        "https://git.example.test/org/repo.git",
		Path:           "manifests/demo",
		TargetRevision: "main",
	}); err != nil {
		t.Fatalf("resolveSourceRoot() error = %v", err)
	}
	if got := collector.Records(); len(got) != 0 {
		t.Fatalf("Records() = %#v, want none for local path resolution", got)
	}
}
