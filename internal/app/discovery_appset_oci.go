package app

import (
	"context"
	"path/filepath"
	"sync"

	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/render"
)

// appsetOCIArtifactFetcher backs the ApplicationSet oci generator with the
// same acquisition path first-class oci:// Application sources use: a
// --repo-map entry for the repoURL wins, otherwise the artifact resolves and
// extracts through the orchestrator's OCI artifact acquirer under the
// request's --offline, cache, credential, and snapshot-session settings.
// Cache events land on the discovery side's events.
//
// The provider is built lazily, so runs without oci generators never build it
// (and never open --registry-config). Its settings are empty on purpose: OCI
// artifact acquisition reads none of the Argo CD settings.
type appsetOCIArtifactFetcher struct {
	ctx         context.Context
	newProvider func(*cacheevent.Recorder) (localProvider, func(), error)
	recorder    *cacheevent.Recorder

	mu       sync.Mutex
	built    bool
	provider localProvider
	cleanup  func()
	buildErr error
}

func newAppsetOCIArtifactFetcher(ctx context.Context, o Orchestrator, root string, request BuildRequest) *appsetOCIArtifactFetcher {
	return &appsetOCIArtifactFetcher{
		ctx: ctx,
		newProvider: func(recorder *cacheevent.Recorder) (localProvider, func(), error) {
			return newLocalProvider(ctx, o, root, config.ArgoSettings{}, request, recorder, "drydock-appset-oci-cache-snapshots-*")
		},
		recorder: cacheevent.NewRecorder(request.RecordCacheEvents),
	}
}

// FetchOCIArtifact implements appset.OCIArtifactFetcher.
func (f *appsetOCIArtifactFetcher) FetchOCIArtifact(repoURL, revision string) (string, error) {
	provider, err := f.ensureProvider()
	if err != nil {
		return "", err
	}
	if provider.sourceResolver != nil {
		if mappedPath, ok := provider.sourceResolver.MappedPath(repoURL); ok {
			provider.recordCacheEvent(cacheevent.Event{Source: cacheevent.SourceGit, Action: cacheevent.ActionMapped, Target: repoURL, Revision: revision})
			return filepath.Abs(mappedPath)
		}
	}
	root, _, err := provider.resolveOCIArtifactSource(f.ctx, render.ResolvedSource{RepoURL: repoURL, TargetRevision: revision})
	return root, err
}

func (f *appsetOCIArtifactFetcher) ensureProvider() (localProvider, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.built {
		f.built = true
		f.provider, f.cleanup, f.buildErr = f.newProvider(f.recorder)
	}
	return f.provider, f.buildErr
}

// close releases the provider's private snapshot area (when the request has
// no shared snapshot session) and returns the recorded cache events.
func (f *appsetOCIArtifactFetcher) close() []cacheevent.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cleanup != nil {
		f.cleanup()
		f.cleanup = nil
	}
	return f.recorder.Events()
}
