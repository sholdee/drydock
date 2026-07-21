package app

import (
	"fmt"
	"sort"
	"strings"

	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/gitref"
	"github.com/sholdee/drydock/internal/ociartifact"
	"github.com/sholdee/drydock/internal/render"
	sourcepkg "github.com/sholdee/drydock/internal/source"
)

// selfRepoNearMissCode identifies the fork near-miss remediation hint. The
// code is strict-exempt: normalizeDiagnostics and diagnosticFailure both skip
// it under --strict (see strictExemptDiagnostic), so the warning stays a
// warning and never fails the run.
const selfRepoNearMissCode = "source.self-repo-near-miss"

// selfRepoRefs identifies "the local repository under analysis" for source
// resolution. Diff entry points populate it from the diff request and side
// paths; ensureBuildSelfRepoRefs populates it for single-tree build/list
// surfaces. The zero value disables everything.
type selfRepoRefs struct {
	urlKeys   []string // CanonicalGitURLKey of every remote URL of the local repo
	revisions []string // symbolic revisions beyond ""/HEAD that track the local
	// tree(s): the trimmed --ref/--ref-orig names on diffs plus the
	// default-branch names read from remote HEAD symrefs (non-SHA)
}

// detectSelfRepoRefsFromPaths is the shared detector core: the union of every
// path's configured remotes identifies the repository (non-git paths
// contribute nothing), and extraRevisions plus every path's default-branch
// names form the symbolic revisions. The default-branch NAME tracks the local
// tree exactly like a --ref/--ref-orig name does: a spec pinned to "main"
// should read the tree drydock is pointed at (the #207 post-merge argument),
// and real repos commonly write targetRevision: main rather than HEAD. The
// names come from remote HEAD symrefs ONLY — no fallback guessing:
// init.defaultBranch or the checked-out HEAD would wrongly treat a PR branch
// itself as the default branch.
func detectSelfRepoRefsFromPaths(paths, extraRevisions []string) selfRepoRefs {
	urls := make([]string, 0, len(paths))
	branchNames := make([]string, 0, len(paths))
	for _, path := range paths {
		urls = append(urls, gitref.RemoteURLs(path)...)
		branchNames = append(branchNames, gitref.DefaultBranchNames(path)...)
	}
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
	for _, revision := range append(append([]string(nil), extraRevisions...), branchNames...) {
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

// detectSelfRepoRefs identifies the repository under diff from the union of
// the diff repo path and both side paths: ref diffs materialize .git-less
// snapshots so the side paths contribute nil, while path diffs point at real
// checkouts whose remotes all contribute. No all-or-nothing fallback — a
// matching remote on either side is enough.
func detectSelfRepoRefs(request DiffRequest, repoPath string) selfRepoRefs {
	return detectSelfRepoRefsFromPaths(
		[]string{repoPath, request.LeftPath, request.RightPath},
		[]string{request.Ref, request.RefOrig},
	)
}

// detectBuildSelfRepoRefs identifies the local checkout for single-tree
// build/list surfaces. Revisions come from the checkout's remote HEAD
// symrefs only — symref-or-nothing, exactly like the diff side.
func detectBuildSelfRepoRefs(path string) selfRepoRefs {
	return detectSelfRepoRefsFromPaths([]string{path}, nil)
}

// clone deep-copies both slices so concurrent consumers (diff sides in
// particular) never share mutable state through their BuildRequests.
func (r selfRepoRefs) clone() selfRepoRefs {
	return selfRepoRefs{
		urlKeys:   append([]string(nil), r.urlKeys...),
		revisions: append([]string(nil), r.revisions...),
	}
}

// isSelfRepoRef reports whether the source names the local repository at a
// revision tracking its tree ("", HEAD, a diffed ref name, or a
// symref-derived default-branch name). Pinned commit SHAs always acquire;
// tags and branches beyond the tracked revisions always acquire.
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

// selfRepoNearMissDiagnostics warns when a source resembles the local
// repository — same host and trailing repo segment as one of its remotes but
// a different full canonical key (classic fork topology) — while the revision
// gate would have passed and no explicit --repo-map covers the URL. The
// previously silent survival mode is now loud. Warnings dedupe once per URL
// per provider via selfRepoNearMissOnce.
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
	// OCI sources never resolve to the local checkout (classification runs
	// before the self-repo branch), so a scheme-stripped oci:// URL matching
	// a git remote's host and repo name is not a fork near-miss.
	if ociartifact.IsOCIURL(repoURL) {
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
		Message:  fmt.Sprintf("source repository %q resembles a remote of the local checkout (remote %q) but matches none of its configured remotes; $ref value files are fetched from the remote repository and may not reflect the local tree — add --repo-map <url>=<path> if these are the same repository", sourcepkg.RedactURL(repoURL), matched),
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
