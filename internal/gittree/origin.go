package gittree

import (
	git "github.com/go-git/go-git/v5"
)

// OriginMatches reports whether repo has an origin remote, and whether
// sameRepository accepts each of its URLs. A drydock cache entry is a
// clone of one repository URL, keyed by that URL. An entry whose origin
// names another repository, for example one written under a wrong key or
// changed on disk, must not be fetched with the credentials of the request,
// and its content must not be served for the request.
//
// It reads go-git's view of the remote, which applies url.insteadOf rules
// of the entry's config, so such a rule that changes the URL also fails
// the check.
func OriginMatches(repo *git.Repository, sameRepository func(originURL string) bool) bool {
	remote, err := repo.Remote("origin")
	if err != nil {
		return false
	}
	urls := remote.Config().URLs
	if len(urls) == 0 {
		return false
	}
	for _, originURL := range urls {
		if !sameRepository(originURL) {
			return false
		}
	}
	return true
}
