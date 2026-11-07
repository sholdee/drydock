package gitref

import (
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/sholdee/drydock/internal/source"
)

// HeadRevision returns the full commit SHA that HEAD names in the Git
// repository whose top-level directory is repoPath. It opens repoPath the
// way WorktreeChangeSet does, so a directory below the top of a repository
// is not a repository here. It returns "" when repoPath is no repository
// top or when HEAD names no commit yet. A tag is peeled to its commit.
func HeadRevision(repoPath string) string {
	repo, _, err := openLocalRepository(repoPath)
	if err != nil {
		return ""
	}
	hash, err := source.ResolveGitRevision(repo, "HEAD")
	if err != nil {
		return ""
	}
	return hash.String()
}

// PinnedRevision returns source.PinnedRevision for revision in the Git
// repository whose top-level directory is repoPath. A revision that is not
// hex returns before the repository is opened. When repoPath is no
// repository top, only a full SHA resolves.
func PinnedRevision(repoPath, revision, repoURL string) (single, multi string, ok bool, err error) {
	if !source.IsHexRevision(revision) {
		return "", "", false, nil
	}
	repo, _, openErr := openLocalRepository(repoPath)
	if openErr != nil {
		return source.PinnedRevision(nil, revision, repoURL)
	}
	return source.PinnedRevision(repo, revision, repoURL)
}

// CurrentBranch returns the short name of the branch that HEAD names in the
// Git repository whose top-level directory is repoPath, such as "main". It
// returns "" when repoPath is no repository top or when HEAD is detached.
func CurrentBranch(repoPath string) string {
	repo, _, err := openLocalRepository(repoPath)
	if err != nil {
		return ""
	}
	head, err := repo.Storer.Reference(plumbing.HEAD)
	if err != nil || head.Type() != plumbing.SymbolicReference || !head.Target().IsBranch() {
		return ""
	}
	return head.Target().Short()
}
