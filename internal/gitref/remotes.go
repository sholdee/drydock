package gitref

import (
	"strings"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// RemoteURLs returns every fetch URL of every configured remote (all remotes,
// not just origin — fork/upstream layouts) of the repository at repoPath.
// Any error (non-git dir) returns nil: self-mapping silently opts out.
// Read-only: Remotes() never mutates the checkout.
func RemoteURLs(repoPath string) []string {
	repoPath = strings.TrimSpace(repoPath)
	if repoPath == "" {
		return nil
	}
	repo, err := git.PlainOpenWithOptions(repoPath, &git.PlainOpenOptions{EnableDotGitCommonDir: true})
	if err != nil {
		return nil
	}
	remotes, err := repo.Remotes()
	if err != nil {
		return nil
	}
	var urls []string
	for _, configured := range remotes {
		urls = append(urls, configured.Config().URLs...)
	}
	return urls
}

// DefaultBranchNames returns the branch name each remote's HEAD symref points
// at (refs/remotes/<remote>/HEAD -> refs/remotes/<remote>/<branch>) for the
// repository at repoPath. A clone sets origin/HEAD to the remote's default
// branch; CI checkouts often do not (pr-action's fetch-base records the pull
// request's base branch there via "git remote set-head origin <base>"). Any error returns nil: default-branch
// gating silently opts out. Read-only.
func DefaultBranchNames(repoPath string) []string {
	repoPath = strings.TrimSpace(repoPath)
	if repoPath == "" {
		return nil
	}
	repo, err := git.PlainOpenWithOptions(repoPath, &git.PlainOpenOptions{EnableDotGitCommonDir: true})
	if err != nil {
		return nil
	}
	remotes, err := repo.Remotes()
	if err != nil {
		return nil
	}
	var names []string
	for _, configured := range remotes {
		remoteName := configured.Config().Name
		if remoteName == "" {
			continue
		}
		headRef := plumbing.ReferenceName("refs/remotes/" + remoteName + "/HEAD")
		// Unresolved read: the HEAD symref's TARGET carries the branch name.
		reference, err := repo.Reference(headRef, false)
		if err != nil || reference.Type() != plumbing.SymbolicReference {
			continue
		}
		prefix := "refs/remotes/" + remoteName + "/"
		target := string(reference.Target())
		if branch, ok := strings.CutPrefix(target, prefix); ok && branch != "" && branch != "HEAD" {
			names = append(names, branch)
		}
	}
	return names
}
