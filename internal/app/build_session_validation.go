package app

import (
	"fmt"
	"strings"

	"github.com/sholdee/drydock/internal/chart"
	"github.com/sholdee/drydock/internal/pathsafety"
	"github.com/sholdee/drydock/internal/remote"
	"github.com/sholdee/drydock/internal/rendercache"
	sourcepkg "github.com/sholdee/drydock/internal/source"
)

func validateBuildNetworkOptions(request BuildRequest) error {
	root := request.Path
	if root == "" {
		root = "."
	}
	forbiddenRoots := requestForbiddenRoots(root, request.AcquisitionOptions)
	if err := validateGitCacheDir(request.GitCacheDir, forbiddenRoots); err != nil {
		return err
	}
	if _, err := chart.ResolveCacheDir(request.ChartCacheDir, forbiddenRoots); err != nil {
		return err
	}
	if _, err := remote.ResolveCacheDir(request.RemoteResourceCacheDir, forbiddenRoots); err != nil {
		return err
	}
	if err := validateBuildRenderCacheRoot(request); err != nil {
		return err
	}
	return nil
}

func validateBuildRenderCacheRoot(request BuildRequest) error {
	if !request.RenderCacheEnabled {
		return nil
	}
	root := request.Path
	if root == "" {
		root = "."
	}
	forbiddenRoots := requestForbiddenRoots(root, request.AcquisitionOptions)
	dir, err := rendercache.ResolveDir(request.RenderCacheDir, forbiddenRoots)
	if err != nil {
		return err
	}
	if !request.EngineFingerprint.Known() {
		// Dev builds never open the store (persistence is disabled with
		// reason "dev-build"), so don't create or probe it here either.
		return nil
	}
	// Opening here makes an unwritable cache dir a hard configuration error,
	// consistent with the git and chart cache dirs, instead of a silent
	// cache-disabled run.
	_, err = rendercache.Open(dir, request.RenderCacheMaxBytes)
	return err
}

func validateGitCacheDir(configured string, forbiddenRoots []string) error {
	gitCacheDir := configured
	if gitCacheDir == "" {
		defaultDir, err := sourcepkg.DefaultGitCacheDir()
		if err != nil {
			return err
		}
		gitCacheDir = defaultDir
	}
	inside, matchedRoot, err := pathsafety.IsInsideAny(gitCacheDir, forbiddenRoots)
	if err != nil {
		return err
	}
	if inside {
		return fmt.Errorf("git cache dir %q must not be inside repository root %q", gitCacheDir, matchedRoot)
	}
	return nil
}

func requestForbiddenRoots(root string, options AcquisitionOptions) []string {
	forbiddenRoots := append([]string(nil), options.RemoteResourceForbiddenRoots...)
	forbiddenRoots = appendUniqueString(forbiddenRoots, root)
	for _, repoMap := range options.RepoMaps {
		if strings.TrimSpace(repoMap.Path) != "" {
			forbiddenRoots = appendUniqueString(forbiddenRoots, repoMap.Path)
		}
	}
	return forbiddenRoots
}
