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
	if source.Path == "" && source.Chart != "" {
		return p.repoRoot, nil
	}
	if p.sourceResolver != nil {
		if mappedPath, ok := p.sourceResolver.MappedPath(source.RepoURL); ok {
			p.recordCacheEvent(cacheevent.Event{Source: cacheevent.SourceGit, Action: cacheevent.ActionMapped, Target: source.RepoURL, Revision: source.TargetRevision})
			return filepath.Abs(mappedPath)
		}
	}
	if source.Path != "" {
		if exists, err := sourcePathExists(p.repoRoot, source.Path); err != nil {
			return "", err
		} else if exists {
			p.recordCacheEvent(cacheevent.Event{Source: cacheevent.SourceGit, Action: cacheevent.ActionLocal, Target: source.RepoURL, Revision: source.TargetRevision})
			return p.repoRoot, nil
		}
	}
	if strings.TrimSpace(source.RepoURL) == "" {
		return p.repoRoot, nil
	}
	if p.sourceResolver == nil {
		return p.repoRoot, nil
	}
	if _, err := p.sourceResolver.Resolve(source.RepoURL, source.TargetRevision); err != nil {
		return "", fmt.Errorf("source path %q is not present under local repository root and %w", source.Path, err)
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
		return "", fmt.Errorf("%s", acquireError.RedactedError)
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
	return acquired.Path, nil
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
