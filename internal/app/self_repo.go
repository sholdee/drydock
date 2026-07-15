package app

import (
	"fmt"
	"sort"
	"strings"

	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/gitref"
	"github.com/sholdee/drydock/internal/render"
	sourcepkg "github.com/sholdee/drydock/internal/source"
)

// selfRepoNearMissCode identifies the fork near-miss remediation hint. The
// code is strict-exempt: normalizeDiagnostics and diagnosticFailure both skip
// it under --strict (see strictExemptDiagnostic), so the warning stays a
// warning and never fails the diff.
const selfRepoNearMissCode = "source.self-repo-near-miss"

// selfRepoRefs identifies "the repository under diff" for source resolution.
// Populated ONLY by diff entry points; zero value disables everything.
type selfRepoRefs struct {
	urlKeys   []string // CanonicalGitURLKey of every remote URL of the diffed repo
	revisions []string // symbolic revisions beyond ""/HEAD that track the diffed
	// tree: the trimmed --ref and --ref-orig names (non-SHA)
}

// detectSelfRepoRefs identifies the repository under diff from the union of
// the diff repo path and both side paths: ref diffs materialize .git-less
// snapshots so the side paths contribute nil, while path diffs point at real
// checkouts whose remotes all contribute. No all-or-nothing fallback — a
// matching remote on either side is enough.
func detectSelfRepoRefs(request DiffRequest, repoPath string) selfRepoRefs {
	urls := gitref.RemoteURLs(repoPath)
	urls = append(urls, gitref.RemoteURLs(request.LeftPath)...)
	urls = append(urls, gitref.RemoteURLs(request.RightPath)...)
	// The repo's default-branch NAME tracks the diffed tree exactly like a
	// --ref/--ref-orig name does: a spec pinned to "main" diffed across any
	// two trees of this repository should read the side trees (the post-merge
	// argument), and a mixed remote-tree render would be less coherent. Real
	// repos commonly write targetRevision: main rather than HEAD.
	branchNames := gitref.DefaultBranchNames(repoPath)
	branchNames = append(branchNames, gitref.DefaultBranchNames(request.LeftPath)...)
	branchNames = append(branchNames, gitref.DefaultBranchNames(request.RightPath)...)
	var refs selfRepoRefs
	seenKeys := map[string]struct{}{}
	for _, url := range urls {
		key := sourcepkg.CanonicalGitURLKey(url)
		if key == "" {
			continue
		}
		if _, ok := seenKeys[key]; ok {
			continue
		}
		seenKeys[key] = struct{}{}
		refs.urlKeys = append(refs.urlKeys, key)
	}
	seenRevisions := map[string]struct{}{}
	for _, revision := range append([]string{request.Ref, request.RefOrig}, branchNames...) {
		revision = strings.TrimSpace(revision)
		if revision == "" || sourcepkg.IsDefaultRevision(revision) || sourcepkg.IsCommitSHA(revision) {
			continue
		}
		if _, ok := seenRevisions[revision]; ok {
			continue
		}
		seenRevisions[revision] = struct{}{}
		refs.revisions = append(refs.revisions, revision)
	}
	return refs
}

// clone deep-copies both slices so concurrent diff sides never share mutable
// state through their BuildRequests.
func (r selfRepoRefs) clone() selfRepoRefs {
	return selfRepoRefs{
		urlKeys:   append([]string(nil), r.urlKeys...),
		revisions: append([]string(nil), r.revisions...),
	}
}

// isSelfRepoRef reports whether the source names the repository under diff at
// a revision tracking the diffed tree. Pinned commit SHAs always acquire;
// tags and branches not named by --ref/--ref-orig always acquire.
func (p localProvider) isSelfRepoRef(repoURL, revision string) bool {
	if len(p.selfRepoURLKeys) == 0 {
		return false
	}
	rev := strings.TrimSpace(revision)
	if sourcepkg.IsCommitSHA(rev) {
		return false // pinned SHAs always acquire
	}
	if !sourcepkg.IsDefaultRevision(rev) {
		if _, ok := p.selfRepoRevisions[rev]; !ok {
			return false // tags/unrelated branches acquire
		}
	}
	if strings.TrimSpace(repoURL) == "" {
		return false
	}
	_, ok := p.selfRepoURLKeys[sourcepkg.CanonicalGitURLKey(repoURL)]
	return ok
}

// selfRepoNearMissDiagnostics warns when a source resembles the repository
// under diff — same host and trailing repo segment as one of its remotes but
// a different full canonical key (classic fork topology) — while the revision
// gate would have passed and no explicit --repo-map covers the URL. The
// previously silent survival mode is now loud. Warnings dedupe once per URL
// per side via selfRepoNearMissOnce.
func (p localProvider) selfRepoNearMissDiagnostics(source render.ResolvedSource, refSources map[string]render.ResolvedSource) []diagnostic.Diagnostic {
	if len(p.selfRepoURLKeys) == 0 || p.selfRepoNearMissOnce == nil {
		return nil
	}
	candidates := make([]render.ResolvedSource, 0, len(refSources)+1)
	candidates = append(candidates, source)
	refKeys := make([]string, 0, len(refSources))
	for refKey := range refSources {
		refKeys = append(refKeys, refKey)
	}
	sort.Strings(refKeys)
	for _, refKey := range refKeys {
		candidates = append(candidates, refSources[refKey])
	}
	var diags []diagnostic.Diagnostic
	for _, candidate := range candidates {
		if diag, ok := p.selfRepoNearMissDiagnostic(candidate); ok {
			diags = append(diags, diag)
		}
	}
	return diags
}

func (p localProvider) selfRepoNearMissDiagnostic(source render.ResolvedSource) (diagnostic.Diagnostic, bool) {
	repoURL := strings.TrimSpace(source.RepoURL)
	if repoURL == "" {
		return diagnostic.Diagnostic{}, false
	}
	rev := strings.TrimSpace(source.TargetRevision)
	if sourcepkg.IsCommitSHA(rev) {
		return diagnostic.Diagnostic{}, false
	}
	if !sourcepkg.IsDefaultRevision(rev) {
		if _, ok := p.selfRepoRevisions[rev]; !ok {
			return diagnostic.Diagnostic{}, false
		}
	}
	key := sourcepkg.CanonicalGitURLKey(repoURL)
	if key == "" {
		return diagnostic.Diagnostic{}, false
	}
	if _, ok := p.selfRepoURLKeys[key]; ok {
		return diagnostic.Diagnostic{}, false
	}
	if p.sourceResolver != nil {
		if _, ok := p.sourceResolver.MappedPath(repoURL); ok {
			return diagnostic.Diagnostic{}, false
		}
	}
	selfKeys := make([]string, 0, len(p.selfRepoURLKeys))
	for selfKey := range p.selfRepoURLKeys {
		selfKeys = append(selfKeys, selfKey)
	}
	sort.Strings(selfKeys)
	matched := ""
	for _, selfKey := range selfKeys {
		if sameGitURLKeyHostAndRepoName(key, selfKey) {
			matched = selfKey
			break
		}
	}
	if matched == "" {
		return diagnostic.Diagnostic{}, false
	}
	if _, loaded := p.selfRepoNearMissOnce.LoadOrStore(key, struct{}{}); loaded {
		return diagnostic.Diagnostic{}, false
	}
	// Always SeverityWarning. Direct construction (never Reporter.Warn) avoids
	// construction-time escalation; the pipeline-wide escalation and failure
	// under --strict are skipped via strictExemptDiagnostic keyed on this code.
	return diagnostic.Diagnostic{
		Code:     selfRepoNearMissCode,
		Severity: diagnostic.SeverityWarning,
		Category: "source",
		Message:  fmt.Sprintf("source repository %q resembles the repository under diff (remote %q) but matches none of its configured remotes; $ref value files are fetched from the remote repository and may not reflect this diff side — add --repo-map <url>=<path> if these are the same repository", sourcepkg.RedactURL(repoURL), matched),
	}, true
}

func sameGitURLKeyHostAndRepoName(left, right string) bool {
	leftHost, leftName := splitGitURLKeyHostAndRepoName(left)
	rightHost, rightName := splitGitURLKeyHostAndRepoName(right)
	return leftHost != "" && leftName != "" && leftHost == rightHost && leftName == rightName
}

func splitGitURLKeyHostAndRepoName(key string) (string, string) {
	host, repoPath, ok := strings.Cut(key, "/")
	if !ok || host == "" || repoPath == "" {
		return "", ""
	}
	name := repoPath
	if idx := strings.LastIndex(repoPath, "/"); idx >= 0 {
		name = repoPath[idx+1:]
	}
	if name == "" {
		return "", ""
	}
	return host, name
}
