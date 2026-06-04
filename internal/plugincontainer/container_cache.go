package plugincontainer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/sholdee/drydock/internal/pathsafety"
	"github.com/sholdee/drydock/internal/pluginpolicy"
)

const (
	containerCacheMetadataFile = ".drydock-cache.json"
	containerCacheLockSuffix   = ".lock"
	containerCacheMetadataV1   = 1
)

type containerCacheMetadata struct {
	Version           int    `json:"version"`
	PolicyFingerprint string `json:"policyFingerprint"`
	PluginNameSHA256  string `json:"pluginNameSHA256"`
	CacheName         string `json:"cacheName"`
	Target            string `json:"target"`
}

func prepareContainerCacheMounts(ctx context.Context, request Request) ([]containerCacheMount, func(), error) {
	if len(request.Config.CacheMounts) == 0 {
		return nil, func() {}, nil
	}
	plan, err := containerCachePlan(request)
	if err != nil {
		return nil, func() {}, err
	}
	mounts := make([]containerCacheMount, 0, len(plan.entries))
	var locks []*containerCacheLock
	cleanup := func() {
		for _, lock := range slices.Backward(locks) {
			lock.Close()
		}
	}
	for _, entry := range plan.entries {
		source, existed, err := prepareContainerCacheDirectory(entry.source, request.ForbiddenRoots)
		if err != nil {
			cleanup()
			return nil, func() {}, err
		}
		lock, err := lockContainerCacheDirectory(ctx, source)
		if err != nil {
			cleanup()
			return nil, func() {}, err
		}
		locks = append(locks, lock)
		if err := ensureContainerCacheMetadata(source, entry.metadata, existed); err != nil {
			cleanup()
			return nil, func() {}, err
		}
		mounts = append(mounts, containerCacheMount{Source: source, Target: entry.metadata.Target})
	}
	return mounts, cleanup, nil
}

type containerCachePlanEntry struct {
	source   string
	metadata containerCacheMetadata
}

type containerCachePlanResult struct {
	entries []containerCachePlanEntry
}

func containerCachePlan(request Request) (containerCachePlanResult, error) {
	pluginName := strings.TrimSpace(request.PluginName)
	if pluginName == "" {
		return containerCachePlanResult{}, fmt.Errorf("plugin name is required when cacheMounts are configured")
	}
	fingerprint := strings.TrimSpace(request.PolicyFingerprint)
	if !isPolicyFingerprint(fingerprint) {
		return containerCachePlanResult{}, fmt.Errorf("policy fingerprint is required when cacheMounts are configured")
	}
	root, err := containerCacheRoot(request.CacheRoot)
	if err != nil {
		return containerCachePlanResult{}, err
	}
	root, err = cleanContainerCachePath(root)
	if err != nil {
		return containerCachePlanResult{}, err
	}
	pluginNameHash := sha256Hex(pluginName)
	pluginDir := filepath.Join(root, fingerprint, pluginNameHash)
	entries := make([]containerCachePlanEntry, 0, len(request.Config.CacheMounts))
	seenNames := map[string]struct{}{}
	seenTargets := map[string]struct{}{}
	for _, cacheMount := range request.Config.CacheMounts {
		if err := validateContainerCacheName(cacheMount.Name); err != nil {
			return containerCachePlanResult{}, err
		}
		if _, ok := seenNames[cacheMount.Name]; ok {
			return containerCachePlanResult{}, fmt.Errorf("cache mount name %q is duplicated", cacheMount.Name)
		}
		seenNames[cacheMount.Name] = struct{}{}
		if err := validateDockerMountValue(cacheMount.Target); err != nil {
			return containerCachePlanResult{}, fmt.Errorf("cache mount %q target: %w", cacheMount.Name, err)
		}
		if _, ok := seenTargets[cacheMount.Target]; ok {
			return containerCachePlanResult{}, fmt.Errorf("cache mount target %q is duplicated", cacheMount.Target)
		}
		seenTargets[cacheMount.Target] = struct{}{}
		entries = append(entries, containerCachePlanEntry{
			source: filepath.Join(pluginDir, cacheMount.Name),
			metadata: containerCacheMetadata{
				Version:           containerCacheMetadataV1,
				PolicyFingerprint: fingerprint,
				PluginNameSHA256:  pluginNameHash,
				CacheName:         cacheMount.Name,
				Target:            cacheMount.Target,
			},
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].source < entries[j].source
	})
	return containerCachePlanResult{entries: entries}, nil
}

func containerCacheRoot(configured string) (string, error) {
	if root := strings.TrimSpace(configured); root != "" {
		return root, nil
	}
	userCacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("resolve user cache dir: %w", err)
	}
	return filepath.Join(userCacheDir, "drydock", "plugin-cache"), nil
}

func prepareContainerCacheDirectory(dir string, forbiddenRoots []string) (string, bool, error) {
	absDir, err := cleanContainerCachePath(dir)
	if err != nil {
		return "", false, err
	}
	if err := rejectForbiddenContainerCacheDir(absDir, forbiddenRoots); err != nil {
		return "", false, err
	}
	existed, err := containerCacheDirectoryExists(absDir)
	if err != nil {
		return "", false, err
	}
	if err := os.MkdirAll(absDir, 0o700); err != nil {
		return "", false, err
	}
	if err := validateContainerCacheDirectory(absDir); err != nil {
		return "", false, err
	}
	if err := os.Chmod(absDir, 0o700); err != nil {
		return "", false, err
	}
	if err := rejectForbiddenContainerCacheDir(absDir, forbiddenRoots); err != nil {
		return "", false, err
	}
	return absDir, existed, nil
}

func cleanContainerCachePath(dir string) (string, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	absDir = filepath.Clean(absDir)
	if err := validateDockerMountValue(absDir); err != nil {
		return "", err
	}
	return absDir, nil
}

func containerCacheDirectoryExists(absDir string) (bool, error) {
	info, err := os.Lstat(absDir)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return false, fmt.Errorf("cache directory %q must not be a symlink", absDir)
		}
		if !info.IsDir() {
			return false, fmt.Errorf("cache directory %q must be a directory", absDir)
		}
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func validateContainerCacheDirectory(absDir string) error {
	info, err := os.Lstat(absDir)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("cache directory %q must not be a symlink", absDir)
	}
	if !info.IsDir() {
		return fmt.Errorf("cache directory %q must be a directory", absDir)
	}
	return nil
}

func ensureContainerCacheMetadata(dir string, want containerCacheMetadata, existed bool) error {
	path := filepath.Join(dir, containerCacheMetadataFile)
	data, err := os.ReadFile(path)
	if err == nil {
		var got containerCacheMetadata
		if err := json.Unmarshal(data, &got); err != nil {
			return fmt.Errorf("cache directory %q metadata is invalid: %w", dir, err)
		}
		if got != want {
			return fmt.Errorf("cache directory %q metadata mismatch", dir)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("read cache directory %q metadata: %w", dir, err)
	}
	if existed {
		return fmt.Errorf("cache directory %q is missing metadata", dir)
	}
	return writeContainerCacheMetadata(path, want)
}

func writeContainerCacheMetadata(path string, metadata containerCacheMetadata) error {
	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temp, err := os.CreateTemp(filepath.Dir(path), ".drydock-cache-metadata-*")
	if err != nil {
		return fmt.Errorf("create cache metadata temp file: %w", err)
	}
	tempPath := temp.Name()
	cleanup := func() { _ = os.Remove(tempPath) }
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		cleanup()
		return fmt.Errorf("write cache metadata: %w", err)
	}
	if err := temp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close cache metadata: %w", err)
	}
	if err := os.Chmod(tempPath, 0o600); err != nil {
		cleanup()
		return fmt.Errorf("chmod cache metadata: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		cleanup()
		return fmt.Errorf("publish cache metadata: %w", err)
	}
	return nil
}

func containerCacheLockPath(dir string) string {
	return filepath.Clean(dir) + containerCacheLockSuffix
}

func validateContainerCacheMountsForDocker(mounts []containerCacheMount, forbiddenRoots []string) error {
	for _, mount := range mounts {
		if err := validateDockerMountValue(mount.Source); err != nil {
			return fmt.Errorf("cache mount source %q: %w", mount.Source, err)
		}
		if err := validateDockerMountValue(mount.Target); err != nil {
			return fmt.Errorf("cache mount target %q: %w", mount.Target, err)
		}
		if err := validateContainerCacheDirectory(mount.Source); err != nil {
			return err
		}
		if err := rejectForbiddenContainerCacheDir(mount.Source, forbiddenRoots); err != nil {
			return err
		}
	}
	return nil
}

func rejectForbiddenContainerCacheDir(dir string, forbiddenRoots []string) error {
	inside, matchedRoot, err := pathsafety.IsInsideAny(dir, forbiddenRoots)
	if err != nil {
		return err
	}
	if inside {
		return fmt.Errorf("cache directory %q must not be inside protected root %q", dir, matchedRoot)
	}
	return nil
}

func validateContainerCacheName(name string) error {
	if name == "" || len(name) > 63 {
		return fmt.Errorf("cache mount name %q is invalid", name)
	}
	for index, char := range name {
		valid := char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '-'
		if !valid {
			return fmt.Errorf("cache mount name %q is invalid", name)
		}
		if index == 0 && char == '-' {
			return fmt.Errorf("cache mount name %q is invalid", name)
		}
	}
	if strings.HasSuffix(name, "-") {
		return fmt.Errorf("cache mount name %q is invalid", name)
	}
	return nil
}

func validateDockerMountValue(value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("mount value must not be empty")
	}
	for _, char := range value {
		if char == ',' || unicode.IsControl(char) {
			return fmt.Errorf("mount value must not contain commas or control characters")
		}
	}
	return nil
}

func isPolicyFingerprint(value string) bool {
	if value == "" || value == pluginpolicy.NoPolicyFingerprint || len(value) != sha256.Size*2 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
