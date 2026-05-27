package render

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/chart"
	"github.com/sholdee/drydock/internal/pathsafety"
	"github.com/sholdee/drydock/internal/remote"
)

//nolint:gocyclo // Remote acquisition branches on file/dir mode, Git copies, and recursive graph preparation.
func (w *kustomizeWorkspace) acquireAndCopyKustomizeRef(ctx context.Context, dir, field string, graphIndex, refIndex int, request remote.Request, ref kustomizeRemoteRef, mode remotePathMode, recurseDirs bool) (string, string, string, error) {
	acquirer := w.opts.RemoteResourceAcquirer
	if acquirer == nil {
		acquirer = remote.DefaultAcquirer{}
	}
	forbiddenRoots := w.opts.RemoteResourceForbiddenRoots
	if len(forbiddenRoots) == 0 {
		forbiddenRoots = []string{w.originalRepoRoot}
	}
	acquired, err := acquirer.Acquire(ctx, request, remote.Options{
		CacheDir:       w.opts.RemoteResourceCacheDir,
		Offline:        w.opts.OfflineRemoteResources,
		Refresh:        w.opts.RefreshRemoteResources,
		ForbiddenRoots: forbiddenRoots,
		Credentials:    w.opts.RemoteResourceCredentials,
		GitCredentials: w.opts.RemoteResourceGitCredentials,
	})
	if err != nil {
		recordRemoteCacheEvent(w.opts, request, err, remote.Result{})
		return "", "", "", fmt.Errorf("acquire remote kustomize resource %s: %s", redactKustomizeRef(ref.Original), redactRemoteKustomizeAcquireError(err, ref, w.opts))
	}
	release := acquired.Release
	defer func() {
		if release != nil {
			release()
		}
	}()
	recordRemoteCacheEvent(w.opts, request, nil, acquired)
	acquiredPath, err := acquiredRemoteKustomizePath(acquired, ref)
	if err != nil {
		return "", "", "", err
	}
	info, err := os.Lstat(acquiredPath)
	if err != nil {
		return "", "", "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", "", "", fmt.Errorf("remote kustomize resource %s is a symlink", redactKustomizeRef(ref.Original))
	}

	generatedName := generatedRemoteRefName(fmt.Sprintf("%03d-%03d", graphIndex, refIndex), ref)
	if info.IsDir() {
		if mode == remotePathFile {
			return "", "", "", fmt.Errorf("kustomize %s %q must resolve to a regular file", field, redactKustomizeRef(ref.Original))
		}
		generatedKind := "remotes"
		if recurseDirs {
			generatedKind = "git"
		}
		generatedRel := filepath.ToSlash(filepath.Join(".drydock", generatedKind, generatedName))
		generatedRoot, err := generatedKustomizeWorkspacePath(dir, generatedRel)
		if err != nil {
			return "", "", "", err
		}
		if !recurseDirs {
			if err := copyRegularTree(acquiredPath, generatedRoot); err != nil {
				return "", "", "", fmt.Errorf("copy remote kustomize resource %s: %w", redactKustomizeRef(ref.Original), err)
			}
			return generatedRel, "", "", nil
		}
		recurseDir := generatedRoot
		rewritten := generatedRel
		if ref.Kind == kustomizeRemoteGit {
			repoRoot := filepath.Clean(acquired.Path)
			_, graph, err := collectKustomizeGraphForPreparation(ctx, repoRoot, acquiredPath)
			if err != nil {
				return "", "", "", fmt.Errorf("collect remote kustomize graph %s: %w", redactKustomizeRef(ref.Original), err)
			}
			if err := copyPreparedKustomizeWorkspaceTree(repoRoot, acquiredPath, generatedRoot, graph); err != nil {
				return "", "", "", fmt.Errorf("copy remote kustomize resource %s: %w", redactKustomizeRef(ref.Original), err)
			}
			subpath := path.Clean(strings.TrimPrefix(ref.Subpath, "/"))
			rewritten = path.Join(generatedRel, filepath.ToSlash(filepath.FromSlash(subpath)))
			recurseDir = filepath.Join(generatedRoot, filepath.FromSlash(subpath))
		} else {
			if err := copyRegularTree(acquiredPath, generatedRoot); err != nil {
				return "", "", "", fmt.Errorf("copy remote kustomize resource %s: %w", redactKustomizeRef(ref.Original), err)
			}
		}
		return rewritten, recurseDir, generatedRoot, nil
	}
	if mode == remotePathDir {
		return "", "", "", fmt.Errorf("kustomize %s %q must resolve to a Kustomization directory", field, redactKustomizeRef(ref.Original))
	}
	if !info.Mode().IsRegular() {
		return "", "", "", fmt.Errorf("remote kustomize resource %s is not a regular file or directory", redactKustomizeRef(ref.Original))
	}
	generatedRel := filepath.ToSlash(filepath.Join(".drydock", "remotes", generatedName))
	generatedPath, err := generatedKustomizeWorkspacePath(dir, generatedRel)
	if err != nil {
		return "", "", "", err
	}
	if err := copyAcquiredRemoteKustomizeResource(acquiredPath, generatedPath); err != nil {
		return "", "", "", fmt.Errorf("copy remote kustomize resource %s: %w", redactKustomizeRef(ref.Original), err)
	}
	return generatedRel, "", "", nil
}

func recordRemoteCacheEvent(opts RenderOptions, request remote.Request, acquireErr error, acquired remote.Result) {
	if opts.CacheEventRecorder == nil {
		return
	}
	input := cacheevent.AcquisitionEventInput{
		Source:            cacheevent.SourceRemote,
		Target:            remoteTargetForEvent(request),
		RequestedRevision: request.Revision,
		Offline:           opts.OfflineRemoteResources,
		Refresh:           opts.RefreshRemoteResources,
		RawTargets: []string{
			request.URL,
			request.RepoURL,
		},
		SensitiveValues: remoteSensitiveValues(opts.RemoteResourceCredentials, opts.RemoteResourceGitCredentials),
	}
	if acquireErr != nil {
		input.Err = acquireErr
		opts.CacheEventRecorder.Record(cacheevent.NewAcquisitionError(input).Event)
		return
	}
	input.Revision = acquired.Revision
	input.FromCache = acquired.FromCache
	input.Network = !acquired.FromCache
	opts.CacheEventRecorder.Record(cacheevent.NewAcquisitionEvent(input))
}

func remoteTargetForEvent(request remote.Request) string {
	if request.RepoURL != "" {
		return request.RepoURL
	}
	return request.URL
}

func remoteSensitiveValues(credentials remote.Credentials, gitCredentials remote.GitCredentials) []string {
	return cacheevent.CompactSensitiveValues(
		credentials.Username,
		credentials.Password,
		credentials.BearerToken,
		gitCredentials.Username,
		gitCredentials.Password,
		gitCredentials.BearerToken,
		gitCredentials.SSHPrivateKey,
		gitCredentials.SSHPassphrase,
	)
}

func chartSensitiveValues(credentials chart.ChartCredentials) []string {
	return cacheevent.CompactSensitiveValues(credentials.Username, credentials.Password, credentials.BearerToken)
}

func redactRemoteKustomizeAcquireError(err error, ref kustomizeRemoteRef, opts RenderOptions) string {
	message := remote.RedactCredentialError(err.Error(), opts.RemoteResourceCredentials, opts.RemoteResourceGitCredentials)
	replacements := []struct {
		raw      string
		redacted string
	}{
		{raw: ref.Original, redacted: redactKustomizeRef(ref.Original)},
		{raw: strings.TrimPrefix(ref.Original, "git::"), redacted: redactKustomizeRef(ref.Original)},
		{raw: ref.URL, redacted: redactKustomizeRef(ref.URL)},
		{raw: ref.RepoURL, redacted: remote.RedactGitRepoURL(ref.RepoURL)},
	}
	for _, replacement := range replacements {
		raw := strings.TrimSpace(replacement.raw)
		if raw == "" || replacement.redacted == "" {
			continue
		}
		message = strings.ReplaceAll(message, raw, replacement.redacted)
	}
	if revision := strings.TrimSpace(ref.Revision); revision != "" && revision != "HEAD" {
		message = strings.ReplaceAll(message, revision, "[redacted]")
	}
	for _, value := range rawKustomizeRemoteQueryValues(ref.Original, "ref") {
		if value == "" {
			continue
		}
		message = strings.ReplaceAll(message, value, "[redacted]")
	}
	return message
}

func rawKustomizeRemoteQueryValues(ref, key string) []string {
	withoutPrefix := strings.TrimPrefix(strings.TrimSpace(ref), "git::")
	_, rawQuery, ok := strings.Cut(withoutPrefix, "?")
	if !ok {
		return nil
	}
	rawQuery, _, _ = strings.Cut(rawQuery, "#")
	var out []string
	for _, part := range strings.Split(rawQuery, "&") {
		rawKey, rawValue, hasValue := strings.Cut(part, "=")
		decodedKey, err := url.QueryUnescape(rawKey)
		if err != nil || decodedKey != key || !hasValue {
			continue
		}
		out = append(out, rawValue)
		if decodedValue, err := url.QueryUnescape(rawValue); err == nil {
			out = append(out, decodedValue)
		}
	}
	return out
}

func isAcquirableRemoteKustomizeResource(ref string) bool {
	_, _, ok, err := remoteRequestForKustomizeRef(ref)
	return err == nil && ok
}

func remoteRequestForKustomizeRef(ref string) (remote.Request, kustomizeRemoteRef, bool, error) {
	parsed, ok, err := parseKustomizeRemoteRef(ref)
	if err != nil || !ok {
		return remote.Request{}, parsed, ok, err
	}
	switch parsed.Kind {
	case kustomizeRemoteNone:
		return remote.Request{}, parsed, false, nil
	case kustomizeRemoteHTTPFile:
		return remote.Request{
			URL:  parsed.URL,
			Kind: remote.RequestHTTPFile,
		}, parsed, true, nil
	case kustomizeRemoteGit:
		return remote.Request{
			URL:      parsed.Original,
			Kind:     remote.RequestGitRepo,
			RepoURL:  parsed.RepoURL,
			Revision: parsed.Revision,
		}, parsed, true, nil
	default:
		return remote.Request{}, parsed, false, nil
	}
}

func acquiredRemoteKustomizePath(acquired remote.Result, ref kustomizeRemoteRef) (string, error) {
	acquiredPath := strings.TrimSpace(acquired.Path)
	if acquiredPath == "" {
		return "", fmt.Errorf("remote kustomize resource %s returned an empty path", redactKustomizeRef(ref.Original))
	}

	switch ref.Kind {
	case kustomizeRemoteNone:
		return "", fmt.Errorf("unsupported remote kustomize resource kind %q", ref.Kind)
	case kustomizeRemoteHTTPFile:
		return acquiredPath, nil
	case kustomizeRemoteGit:
		subpath := path.Clean(strings.TrimPrefix(ref.Subpath, "/"))
		if pathsafety.SlashRelEscapes(subpath) {
			return "", fmt.Errorf("remote kustomize resource %s subpath %q escapes acquired repository", redactKustomizeRef(ref.Original), ref.Subpath)
		}
		repoRoot := filepath.Clean(acquiredPath)
		info, err := os.Lstat(repoRoot)
		if err != nil {
			return "", fmt.Errorf("remote kustomize resource %s acquired repository %q: %w", redactKustomizeRef(ref.Original), repoRoot, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("remote kustomize resource %s returned symlinked repository root %q", redactKustomizeRef(ref.Original), repoRoot)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("remote kustomize resource %s acquired repository %q is not a directory", redactKustomizeRef(ref.Original), repoRoot)
		}
		target := filepath.Clean(filepath.Join(repoRoot, filepath.FromSlash(subpath)))
		if err := rejectSymlinkedPath(repoRoot, target); err != nil {
			return "", fmt.Errorf("remote kustomize resource %s subpath %q: %w", redactKustomizeRef(ref.Original), ref.Subpath, err)
		}
		inside, _, err := remote.IsPathInsideAny(target, []string{repoRoot})
		if err != nil {
			return "", err
		}
		if !inside {
			return "", fmt.Errorf("remote kustomize resource %s subpath %q escapes acquired repository %q", redactKustomizeRef(ref.Original), ref.Subpath, repoRoot)
		}
		return target, nil
	default:
		return "", fmt.Errorf("unsupported remote kustomize resource kind %q", ref.Kind)
	}
}

func unsupportedRemoteKustomizeRefError(field, ref string) error {
	return fmt.Errorf("kustomize %s %q is a remote ref; remote Kustomize refs are unsupported", field, redactKustomizeRef(ref))
}

func redactKustomizeRef(ref string) string {
	return redactKustomizeRemoteRef(ref)
}

func isInlineStrategicMergePatch(patch string) bool {
	return strings.Contains(patch, "\n")
}

func isRemoteKustomizeRef(ref string) bool {
	trimmed := strings.TrimSpace(ref)
	if _, ok, err := parseKustomizeRemoteRef(trimmed); ok || err != nil {
		return true
	}
	lower := strings.ToLower(trimmed)
	if strings.Contains(trimmed, "://") {
		return true
	}
	if strings.HasPrefix(lower, "git::") || strings.HasPrefix(lower, "git@") {
		return true
	}
	if isColonStyleKustomizeRemoteRef(trimmed) {
		return true
	}
	parsed, err := url.Parse(trimmed)
	if err == nil && parsed.Scheme != "" {
		return true
	}
	if hasRemoteQueryOrFragmentSyntax(trimmed) {
		return true
	}
	if strings.Contains(lower, "?ref=") && strings.Contains(lower, "//") {
		return true
	}
	for _, host := range []string{"github.com/", "gitlab.com/", "bitbucket.org/"} {
		if strings.HasPrefix(lower, host) {
			return true
		}
	}
	return false
}

func hasRemoteQueryOrFragmentSyntax(ref string) bool {
	if !strings.ContainsAny(ref, "?#") {
		return false
	}
	refPath := ref
	if before, _, ok := strings.Cut(refPath, "?"); ok {
		refPath = before
	}
	if before, _, ok := strings.Cut(refPath, "#"); ok {
		refPath = before
	}
	if !strings.Contains(refPath, "/") {
		return false
	}
	hostCandidate, _, _ := strings.Cut(refPath, "/")
	if user, host, ok := strings.Cut(hostCandidate, "@"); ok {
		return user != "" && host != "" && looksLikeRemoteHost(strings.ToLower(host))
	}
	hostCandidate = strings.ToLower(hostCandidate)
	return isKnownGitHost(hostCandidate) || looksLikeRemoteHost(hostCandidate)
}

func isColonStyleKustomizeRemoteRef(ref string) bool {
	beforeColon, afterColon, ok := strings.Cut(ref, ":")
	if !ok || beforeColon == "" || afterColon == "" {
		return false
	}
	if strings.ContainsAny(beforeColon, `/\`) {
		return false
	}

	host := beforeColon
	if user, afterAt, ok := strings.Cut(beforeColon, "@"); ok {
		return user != "" && afterAt != "" && !strings.ContainsAny(afterAt, `/\`)
	}
	host = strings.ToLower(host)
	return isKnownGitHost(host) || looksLikeRemoteHost(host)
}

func isKnownGitHost(host string) bool {
	for _, known := range []string{"github.com", "gitlab.com", "bitbucket.org"} {
		if host == known {
			return true
		}
	}
	return false
}

func looksLikeRemoteHost(host string) bool {
	return strings.Contains(host, ".")
}
