package app

import (
	"fmt"

	"github.com/sholdee/drydock/internal/pathsafety"
	sourcepkg "github.com/sholdee/drydock/internal/source"

	"strings"
)

func validateBuildNetworkOptions(request BuildRequest) error {
	gitCacheDir := request.GitCacheDir
	if gitCacheDir == "" {
		defaultDir, err := sourcepkg.DefaultGitCacheDir()
		if err != nil {
			return err
		}
		gitCacheDir = defaultDir
	}
	root := request.Path
	if root == "" {
		root = "."
	}
	forbiddenRoots := append([]string(nil), request.RemoteResourceForbiddenRoots...)
	forbiddenRoots = append(forbiddenRoots, root)
	for _, repoMap := range request.RepoMaps {
		if strings.TrimSpace(repoMap.Path) != "" {
			forbiddenRoots = append(forbiddenRoots, repoMap.Path)
		}
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
