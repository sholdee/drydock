package render

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// OutOfBoundsSymlinkScope names the tree that the Argo CD repo-server checks
// for out-of-bounds symlinks before it generates manifests from it. Each
// scope has its own error text (reposerver/repository/repository.go at
// v3.6.1).
type OutOfBoundsSymlinkScope string

const (
	// OutOfBoundsSymlinkScopeRepository is a Git checkout, checked from its
	// root with the root .git skipped (repository.go:499-514 for a source,
	// 941-959 for a $ref source of Helm value files).
	OutOfBoundsSymlinkScopeRepository OutOfBoundsSymlinkScope = "repository"
	// OutOfBoundsSymlinkScopeOCI is an extracted OCI artifact
	// (repository.go:429-444).
	OutOfBoundsSymlinkScopeOCI OutOfBoundsSymlinkScope = "oci image"
	// OutOfBoundsSymlinkScopeChart is an extracted chart of a Helm
	// repository (repository.go:470-485).
	OutOfBoundsSymlinkScopeChart OutOfBoundsSymlinkScope = "chart"
)

// OutOfBoundsSymlinksAllowKey is the argocd-cmd-params-cm key that turns the
// repo-server check off.
const OutOfBoundsSymlinksAllowKey = "reposerver.allow.oob.symlinks"

// OutOfBoundsSymlinkError is the source failure for a tree that holds an
// out-of-bounds symlink. Its message holds the repo-server's message, for
// example "repository contains out-of-bounds symlinks. file: a/b.yaml".
type OutOfBoundsSymlinkError struct {
	Scope OutOfBoundsSymlinkScope
	// File is the path of the first out-of-bounds symlink in walk order,
	// relative to the tree root, with forward slashes.
	File string
}

func (e *OutOfBoundsSymlinkError) Error() string {
	return fmt.Sprintf("%s contains out-of-bounds symlinks. file: %s; Argo CD fails every Application that renders from it unless argocd-cmd-params-cm sets %s to \"true\"", e.Scope, e.File, OutOfBoundsSymlinksAllowKey)
}

// OutOfBoundsSymlinkOptions tunes FindOutOfBoundsSymlink.
type OutOfBoundsSymlinkOptions struct {
	// SkipRootGit skips the .git entry at the root, as the repo-server
	// does for a Git checkout. drydock skips it whether it is a directory or
	// a file, such as the .git file of a linked Git worktree.
	SkipRootGit bool
	// Skip reports whether the walk leaves out a path, relative to the root
	// with forward slashes. drydock uses it to leave out files that Git
	// ignores and does not track, which no Argo CD checkout holds. A
	// skipped directory is not walked.
	Skip func(rel string, isDir bool) bool
}

// FindOutOfBoundsSymlink returns the path of the first symlink under root
// that Argo CD v3.6.1 CheckOutOfBoundsSymlinks (util/app/path/path.go:51-110)
// reports: a symlink with an absolute target, or one whose target leaves
// root at any step of its components. It returns "" when there is none. The
// walk is lexical and does not follow symlinks, as filepath.Walk is, so the
// first symlink is the one the repo-server names. root itself may be a
// symlink: the walk starts at its target.
func FindOutOfBoundsSymlink(ctx context.Context, root string, opts OutOfBoundsSymlinkOptions) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("failed to get absolute path: %w", err)
	}
	// The repo-server's checkout root is never a symlink. drydock's root
	// can be one, such as a --repo-map path, and the walk would then stop at
	// the root without reading it. Paths are relative to the resolved root,
	// as the repo-server would report them for the tree itself.
	absRoot, err = filepath.EvalSymlinks(absRoot)
	if err != nil {
		return "", fmt.Errorf("failed to resolve %s: %w", root, err)
	}
	var found string
	errFound := errors.New("found")
	err = filepath.WalkDir(absRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// The repo-server ignores files that disappear during the walk.
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			return fmt.Errorf("failed to walk for symlinks in %s: %w", root, walkErr)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == absRoot {
			return nil
		}
		rel, err := filepath.Rel(absRoot, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if opts.skips(rel, entry.IsDir()) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink == 0 {
			return nil
		}
		outOfBounds, err := symlinkLeavesRoot(absRoot, path)
		if err != nil || !outOfBounds {
			return err
		}
		found = rel
		return errFound
	})
	if errors.Is(err, errFound) {
		return found, nil
	}
	return "", err
}

// skips reports whether the walk leaves out rel.
func (opts OutOfBoundsSymlinkOptions) skips(rel string, isDir bool) bool {
	return (opts.SkipRootGit && rel == ".git") || (opts.Skip != nil && opts.Skip(rel, isDir))
}

// symlinkLeavesRoot is the per-symlink test of CheckOutOfBoundsSymlinks
// (util/app/path/path.go:77-105 at v3.6.1). It reads the link text without
// following it: an absolute target is out of bounds, and so is a relative
// target when any prefix of its components, joined to the symlink's
// directory, leaves root.
func symlinkLeavesRoot(absRoot, path string) (bool, error) {
	linkTarget, err := os.Readlink(path)
	if err != nil {
		return false, fmt.Errorf("failed to read link %s: %w", path, err)
	}
	if filepath.IsAbs(linkTarget) {
		return true, nil
	}
	currentDir := filepath.Dir(path)
	for part := range strings.SplitSeq(linkTarget, string(os.PathSeparator)) {
		newDir := filepath.Join(currentDir, part)
		rel, err := filepath.Rel(absRoot, newDir)
		if err != nil {
			return false, fmt.Errorf("failed to get relative path for symlink target: %w", err)
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return true, nil
		}
		currentDir = newDir
	}
	return false, nil
}

// CheckOutOfBoundsSymlinks fails with an *OutOfBoundsSymlinkError when root
// holds an out-of-bounds symlink (see FindOutOfBoundsSymlink).
func CheckOutOfBoundsSymlinks(ctx context.Context, root string, scope OutOfBoundsSymlinkScope, opts OutOfBoundsSymlinkOptions) error {
	file, err := FindOutOfBoundsSymlink(ctx, root, opts)
	if err != nil {
		return err
	}
	if file != "" {
		return &OutOfBoundsSymlinkError{Scope: scope, File: file}
	}
	return nil
}
