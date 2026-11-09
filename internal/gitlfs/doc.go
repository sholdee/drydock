// Package gitlfs downloads Git LFS objects and puts their content in place
// of Git LFS pointer files, as `git lfs fetch` and `git lfs checkout` do in
// the Argo CD v3.6.1 repo-server when a repository Secret sets enableLfs.
//
// It implements the download operation of the Git LFS batch API
// (docs/api/batch.md of git-lfs 3.x) with the basic transfer adapter
// (docs/api/basic-transfers.md) over http and https. It never runs git-lfs.
// Downloaded objects go to a content-addressed store in the drydock Git
// cache, shared by every repository, after drydock makes sure that their
// size and SHA-256 match the pointer.
package gitlfs
