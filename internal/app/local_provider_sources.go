package app

import (
	"context"
	"encoding/base64"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/ociartifact"
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
	if chartFlowSource(source) {
		return p.repoRoot, chartOnlySourceIdentity(source), nil
	}
	if p.sourceResolver != nil {
		if mappedPath, ok := p.sourceResolver.MappedPath(source.RepoURL); ok {
			p.recordCacheEvent(cacheevent.Event{Source: cacheevent.SourceGit, Action: cacheevent.ActionMapped, Target: source.RepoURL, Revision: source.TargetRevision})
			abs, err := filepath.Abs(mappedPath)
			return abs, SourceIdentity{}, err
		}
	}
	// OCI classification is total over oci:// spellings, including
	// chart-bearing ones (chart is ignored for them, see normalizeOCISource),
	// and sits AFTER the repo-map escape hatch (a --repo-map of the oci URL
	// must keep winning, for the chart shape too) but BEFORE the path-exists,
	// self-repo, and git-acquisition branches: `path: .` always exists
	// locally, so falling through would silently render the local checkout
	// (issue #220's masked failure mode), and CanonicalGitURLKey strips
	// schemes, so an oci:// URL could otherwise full-match a git remote in
	// the self-repo branch.
	if ociartifact.IsOCIURL(source.RepoURL) {
		return p.resolveOCIArtifactSource(ctx, source)
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

// resolveOCIArtifactSource acquires a first-class OCI artifact source: the
// revision resolves to a digest, the artifact content extracts into the
// session snapshot area, and the extraction root becomes the source root.
func (p localProvider) resolveOCIArtifactSource(ctx context.Context, source render.ResolvedSource) (string, SourceIdentity, error) {
	acquirer := p.acquisition.OCIArtifactAcquirer(p.ociArtifactAcquirer)
	// Per-host --registry-config credentials resolve once per source, before
	// acquisition: the file-sourced password must reach ociSensitiveValues so
	// cache events and error text redact it, and a missing or unusable file
	// fails here — at the first oci:// acquisition — rather than at request
	// construction, where Validate stays TLS-only so git-only runs never see
	// it. The acquirer's own ForRepository call is a no-op on this copy.
	effective, err := p.ociCredentials.ForRepository(ctx, source.RepoURL)
	if err != nil {
		return "", SourceIdentity{}, p.recordOCIAcquisitionError(source, err, p.ociCredentials)
	}
	acquiredFromCache := (*bool)(nil)
	opts := ociartifact.Options{
		CacheDir:       p.ociCacheDir,
		Offline:        p.offline,
		ForbiddenRoots: append([]string(nil), p.ociForbiddenRoots...),
		Credentials:    effective,
		OnAcquired: func(fromImageCache bool) {
			acquiredFromCache = &fromImageCache
		},
	}
	digest, err := acquirer.Resolve(ctx, source.RepoURL, source.TargetRevision, opts)
	if err != nil {
		return "", SourceIdentity{}, p.recordOCIAcquisitionError(source, err, effective)
	}
	root, _, err := acquirer.Extract(ctx, source.RepoURL, digest, opts)
	if err != nil {
		return "", SourceIdentity{}, p.recordOCIAcquisitionError(source, err, effective)
	}
	// Session-memoized calls skip OnAcquired: only real acquisitions emit an
	// event, so a two-sided diff at one digest records a single event.
	if acquiredFromCache != nil {
		p.recordCacheEvent(cacheevent.NewAcquisitionEvent(cacheevent.AcquisitionEventInput{
			Source:            cacheevent.SourceOCI,
			Target:            source.RepoURL,
			Revision:          digest,
			RequestedRevision: source.TargetRevision,
			FromCache:         *acquiredFromCache,
			Network:           !*acquiredFromCache,
			Offline:           p.offline,
			SensitiveValues:   ociSensitiveValues(effective),
		}))
		p.recordAcquisition(cacheevent.AcquisitionRecord{
			Kind:              cacheevent.AcquisitionOCI,
			RequestedRevision: source.TargetRevision,
			ResolvedRevision:  digest,
		})
	}
	// The resolved digest rides in the same identity field the git flow uses
	// for commit SHAs (SourceIdentity.Revision, see the git branch above), so
	// persistent render-cache keys rotate with content, not tag spelling.
	return root, SourceIdentity{
		Kind:     sourceIdentityKindOCI,
		Locator:  ociartifact.RedactURL(source.RepoURL),
		Revision: digest,
	}, nil
}

// recordOCIAcquisitionError records the failure as a cache event and returns
// the redacted error. credentials are the ones in effect for the failed step
// (the per-host resolved set once ForRepository succeeded, the request's own
// set when ForRepository itself failed), so whichever password was in play is
// scrubbed from both.
func (p localProvider) recordOCIAcquisitionError(source render.ResolvedSource, err error, credentials ociartifact.Credentials) error {
	acquireError := cacheevent.NewAcquisitionError(cacheevent.AcquisitionEventInput{
		Source:            cacheevent.SourceOCI,
		Target:            source.RepoURL,
		RequestedRevision: source.TargetRevision,
		Offline:           p.offline,
		Err:               err,
		SensitiveValues:   ociSensitiveValues(credentials),
	})
	p.recordCacheEvent(acquireError.Event)
	return fmt.Errorf("%s", acquireError.RedactedError)
}

// chartFlowSource reports whether source renders through the Helm chart flow
// from the provider root: a chart-only source on a schemeless OCI registry or
// an HTTP(S) Helm repository. An oci:// repoURL never does: Argo CD dispatches
// on source.IsOCI() before source.IsHelm() (repository.go:358-364,395-427 at
// v3.5.4; IsOCI is a bare prefix check, types.go:337-339) and IsHelmOciRepo
// only accepts schemeless registries (util/helm/client.go:446-454), so an
// oci:// spelling classifies as an artifact source whatever chart says.
func chartFlowSource(source render.ResolvedSource) bool {
	return source.Path == "" && source.Chart != "" && !ociartifact.IsOCIURL(source.RepoURL)
}

// chartOnlySourceIdentity derives the identity of a schemeless-OCI or HTTP(S)
// chart-only source from the spec alone: drydock supports exact chart versions
// only, so name+version fully determine content per registry contract. An
// oci:// source never takes this identity — it resolves to a digest through
// resolveOCIArtifactSource, chart or no chart.
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
		// Upstream parity: $ref value-file materialization is git-only in
		// argo-cd v3.4.5 (resolveReferencedSources, repository.go:546-592,
		// 820-870). Without this gate every ref funnels through
		// resolveSourceRoot and OCI refs would work by accident.
		if ociartifact.IsOCIURL(refSource.RepoURL) {
			return nil, fmt.Errorf("ref root %s: OCI sources cannot be referenced as $ref value sources (unsupported by Argo CD)", refKey)
		}
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

// ociSensitiveValues mirrors the chart/git sensitive-value pattern
// (chartSensitiveValues, sourceGitSensitiveValues). The base64(user:pass)
// Basic-auth form is redacted alongside the literals: registry error bodies
// and proxies can echo the received Authorization header, which carries the
// secret in that encoding.
func ociSensitiveValues(credentials ociartifact.Credentials) []string {
	values := []string{credentials.Username, credentials.Password}
	if credentials.Username != "" || credentials.Password != "" {
		values = append(values, base64.StdEncoding.EncodeToString([]byte(credentials.Username+":"+credentials.Password)))
	}
	return cacheevent.CompactSensitiveValues(values...)
}
