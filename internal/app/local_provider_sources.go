package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/render"
	sourcepkg "github.com/sholdee/drydock/internal/source"
)

func (p localProvider) resolveSourceRoot(ctx context.Context, source render.ResolvedSource) (string, error) {
	root, _, err := p.resolveSourceRootIdentity(ctx, source)
	return root, err
}

// resolveSourceRootIdentity resolves the source root exactly as
// resolveSourceRoot always has, and additionally reports the source's resolved
// SourceIdentity for the persistent render cache key. A zero identity marks
// the source persistence-ineligible.
func (p localProvider) resolveSourceRootIdentity(ctx context.Context, source render.ResolvedSource) (string, SourceIdentity, error) {
	if source.Path == "" && source.Chart != "" {
		return p.repoRoot, chartOnlySourceIdentity(source), nil
	}
	if p.sourceResolver != nil {
		if mappedPath, ok := p.sourceResolver.MappedPath(source.RepoURL); ok {
			p.recordCacheEvent(cacheevent.Event{Source: cacheevent.SourceGit, Action: cacheevent.ActionMapped, Target: source.RepoURL, Revision: source.TargetRevision})
			abs, err := filepath.Abs(mappedPath)
			return abs, SourceIdentity{}, err
		}
	}
	if source.Path != "" {
		if exists, err := sourcePathExists(p.repoRoot, source.Path); err != nil {
			return "", SourceIdentity{}, err
		} else if exists {
			p.recordCacheEvent(cacheevent.Event{Source: cacheevent.SourceGit, Action: cacheevent.ActionLocal, Target: source.RepoURL, Revision: source.TargetRevision})
			return p.repoRoot, p.rootIdentity, nil
		}
	}
	if p.isSelfRepoRef(source.RepoURL, source.TargetRevision) {
		// The source names the local repository at a revision tracking this
		// tree — the tree drydock is pointed at IS the desired state (#207
		// rationale, extended from diffs to all render surfaces). Resolve to
		// this provider's root: never consult the git acquirer, whose
		// snapshot-memo and persistent-cache keys are blind to which local
		// tree is rendering (URL+revision string). rootIdentity is
		// root-scoped (per-side rootRevision on diffs, worktree state on
		// builds), so the app stays persistent-render-cache eligible with
		// tree-accurate keys — also fixing the tree-blind identity for pure
		// ref-only sources.
		p.recordCacheEvent(cacheevent.Event{Source: cacheevent.SourceGit, Action: cacheevent.ActionLocal, Target: source.RepoURL, Revision: source.TargetRevision})
		return p.repoRoot, p.rootIdentity, nil
	}
	if strings.TrimSpace(source.RepoURL) == "" {
		return p.repoRoot, p.rootIdentity, nil
	}
	if p.sourceResolver == nil {
		return p.repoRoot, p.rootIdentity, nil
	}
	if _, err := p.sourceResolver.Resolve(source.RepoURL, source.TargetRevision); err != nil {
		return "", SourceIdentity{}, fmt.Errorf("source path %q is not present under local repository root and %w", source.Path, err)
	}
	acquirer := p.gitAcquirer
	if acquirer == nil {
		acquirer = sourcepkg.DefaultGitAcquirer{}
	}
	acquirer = p.acquisition.GitAcquirer(acquirer)
	acquired, err := acquirer.Acquire(ctx, sourcepkg.GitRequest{
		URL:      source.RepoURL,
		Revision: source.TargetRevision,
	}, sourcepkg.GitOptions{
		AllowNetwork: !p.offline,
		CacheDir:     p.gitCacheDir,
		Refresh:      p.refreshGit,
		Credentials:  p.gitCredentials,
	})
	if err != nil {
		acquireError := cacheevent.NewAcquisitionError(cacheevent.AcquisitionEventInput{
			Source:            cacheevent.SourceGit,
			Target:            source.RepoURL,
			RequestedRevision: source.TargetRevision,
			Offline:           p.offline,
			Refresh:           p.refreshGit,
			Err:               err,
			ErrorText:         sourcepkg.RedactGitCredentialError(err.Error(), p.gitCredentials),
			SensitiveValues:   sourceGitSensitiveValues(p.gitCredentials),
		})
		p.recordCacheEvent(acquireError.Event)
		return "", SourceIdentity{}, fmt.Errorf("%s", acquireError.RedactedError)
	}
	p.recordCacheEvent(cacheevent.NewAcquisitionEvent(cacheevent.AcquisitionEventInput{
		Source:            cacheevent.SourceGit,
		Target:            source.RepoURL,
		Revision:          acquired.Revision,
		RequestedRevision: source.TargetRevision,
		FromCache:         acquired.FromCache,
		Network:           acquired.Network,
		Offline:           p.offline,
		Refresh:           p.refreshGit,
	}))
	p.recordAcquisition(cacheevent.AcquisitionRecord{
		Kind:              cacheevent.AcquisitionGit,
		RequestedRevision: source.TargetRevision,
		ResolvedRevision:  acquired.Revision,
	})
	return acquired.Path, SourceIdentity{
		Kind:     sourceIdentityKindGit,
		Locator:  sourcepkg.NormalizeURL(source.RepoURL),
		Revision: acquired.Revision,
	}, nil
}

// chartOnlySourceIdentity derives the identity from the spec alone: drydock
// supports exact chart versions only, so name+version fully determine content
// per registry contract.
func chartOnlySourceIdentity(source render.ResolvedSource) SourceIdentity {
	version := strings.TrimSpace(source.TargetRevision)
	if version == "" {
		return SourceIdentity{}
	}
	return SourceIdentity{
		Kind:     sourceIdentityKindChart,
		Locator:  strings.TrimSpace(source.RepoURL) + "::" + strings.TrimSpace(source.Chart),
		Revision: version,
	}
}

func (p localProvider) resolveRefRoots(ctx context.Context, refSources map[string]render.ResolvedSource) (map[string]string, error) {
	if len(refSources) == 0 {
		return map[string]string{}, nil
	}
	out := make(map[string]string, len(refSources))
	for refKey, refSource := range refSources {
		root, err := p.resolveSourceRoot(ctx, refSource)
		if err != nil {
			return nil, fmt.Errorf("ref root %s: %w", refKey, err)
		}
		out[refKey] = root
	}
	return out, nil
}

func sourcePathExists(repoRoot, sourcePath string) (bool, error) {
	clean, err := cleanLocalSourcePath(sourcePath)
	if err != nil {
		return false, err
	}
	return localPathExists(filepath.Join(repoRoot, clean))
}

func sourceGitSensitiveValues(credentials sourcepkg.GitCredentials) []string {
	return cacheevent.CompactSensitiveValues(
		credentials.Username,
		credentials.Password,
		credentials.BearerToken,
		credentials.SSHPrivateKey,
		credentials.SSHPassphrase,
	)
}
