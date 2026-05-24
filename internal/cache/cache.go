package cache

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Source string

const (
	SourceGit    Source = "git"
	SourceChart  Source = "chart"
	SourceRemote Source = "remote"
)

type Options struct {
	GitCacheDir    string
	ChartCacheDir  string
	RemoteCacheDir string
	Sources        []Source
	ForbiddenRoots []string
}

type Entry struct {
	Source       Source    `json:"source" yaml:"source"`
	Kind         string    `json:"kind" yaml:"kind"`
	Key          string    `json:"key" yaml:"key"`
	Path         string    `json:"path" yaml:"path"`
	MetadataPath string    `json:"metadataPath,omitempty" yaml:"metadataPath,omitempty"`
	SizeBytes    int64     `json:"sizeBytes" yaml:"sizeBytes"`
	ModifiedAt   time.Time `json:"modifiedAt" yaml:"modifiedAt"`
	Legacy       bool      `json:"legacy" yaml:"legacy"`
	Metadata     *Metadata `json:"metadata,omitempty" yaml:"metadata,omitempty"`
	cacheRoot    string
}

type OperationOptions struct {
	Options
	OlderThan time.Duration
	DryRun    bool
	Yes       bool
	Source    Source
	Kind      string
	Key       string
	All       bool
}

type OperationResult struct {
	Entries      []Entry `json:"entries" yaml:"entries"`
	RemovedCount int     `json:"removedCount" yaml:"removedCount"`
	DryRun       bool    `json:"dryRun" yaml:"dryRun"`
}

func List(opts Options) ([]Entry, error) {
	var entries []Entry
	enabled := enabledSources(opts.Sources)
	if enabled[SourceChart] {
		chartEntries, err := listChartEntries(opts.ChartCacheDir, opts.ForbiddenRoots)
		if err != nil {
			return nil, err
		}
		entries = append(entries, chartEntries...)
	}
	if enabled[SourceGit] {
		gitEntries, err := listSimpleEntries(SourceGit, "git", opts.GitCacheDir, opts.ForbiddenRoots)
		if err != nil {
			return nil, err
		}
		entries = append(entries, gitEntries...)
	}
	if enabled[SourceRemote] {
		remoteEntries, err := listRemoteEntries(opts.RemoteCacheDir, opts.ForbiddenRoots)
		if err != nil {
			return nil, err
		}
		entries = append(entries, remoteEntries...)
	}
	sortEntries(entries)
	return entries, nil
}

func Prune(opts OperationOptions) (OperationResult, error) {
	if opts.OlderThan <= 0 {
		return OperationResult{}, fmt.Errorf("older-than must be greater than zero")
	}
	if !opts.DryRun && !opts.Yes {
		return OperationResult{}, fmt.Errorf("--yes is required for non-dry-run cache prune")
	}
	entries, err := List(opts.Options)
	if err != nil {
		return OperationResult{}, err
	}
	cutoff := time.Now().Add(-opts.OlderThan)
	selected := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		if opts.Source != "" && entry.Source != opts.Source {
			continue
		}
		if opts.Kind != "" && entry.Kind != opts.Kind {
			continue
		}
		if entryAgeTime(entry).Before(cutoff) {
			selected = append(selected, entry)
		}
	}
	result := OperationResult{Entries: selected, DryRun: opts.DryRun}
	if opts.DryRun {
		return result, nil
	}
	if err := removeEntries(selected); err != nil {
		return OperationResult{}, err
	}
	result.RemovedCount = len(selected)
	return result, nil
}

func Delete(opts OperationOptions) (OperationResult, error) {
	if !opts.All {
		if opts.Source == "" {
			return OperationResult{}, fmt.Errorf("source is required unless --all is set")
		}
		if strings.TrimSpace(opts.Key) == "" {
			return OperationResult{}, fmt.Errorf("key is required unless --all is set")
		}
	}
	if !opts.DryRun && !opts.Yes {
		return OperationResult{}, fmt.Errorf("--yes is required for non-dry-run cache delete")
	}
	entries, err := List(opts.Options)
	if err != nil {
		return OperationResult{}, err
	}
	selected := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		if !opts.All {
			if entry.Source != opts.Source || entry.Key != opts.Key {
				continue
			}
			if opts.Kind != "" && entry.Kind != opts.Kind {
				continue
			}
		}
		selected = append(selected, entry)
	}
	result := OperationResult{Entries: selected, DryRun: opts.DryRun}
	if opts.DryRun {
		return result, nil
	}
	if err := removeEntries(selected); err != nil {
		return OperationResult{}, err
	}
	result.RemovedCount = len(selected)
	return result, nil
}

func listSimpleEntries(source Source, kind, root string, forbiddenRoots []string) ([]Entry, error) {
	root, ok, err := resolveCacheRoot(root, forbiddenRoots)
	if err != nil || !ok {
		return nil, err
	}
	children, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var entries []Entry
	for _, child := range children {
		if !child.IsDir() || !isCacheKey(child.Name()) {
			continue
		}
		entry, ok := buildEntry(source, kind, child.Name(), filepath.Join(root, child.Name()), root)
		if ok {
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

func listChartEntries(root string, forbiddenRoots []string) ([]Entry, error) {
	root, ok, err := resolveCacheRoot(root, forbiddenRoots)
	if err != nil || !ok {
		return nil, err
	}
	var entries []Entry
	for _, kind := range []string{"http", "oci"} {
		kindRoot := filepath.Join(root, kind)
		children, err := os.ReadDir(kindRoot)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, child := range children {
			if !child.IsDir() || !isCacheKey(child.Name()) {
				continue
			}
			entry, ok := buildEntry(SourceChart, kind, child.Name(), filepath.Join(kindRoot, child.Name()), root)
			if ok {
				entries = append(entries, entry)
			}
		}
	}
	return entries, nil
}

func listRemoteEntries(root string, forbiddenRoots []string) ([]Entry, error) {
	root, ok, err := resolveCacheRoot(root, forbiddenRoots)
	if err != nil || !ok {
		return nil, err
	}
	children, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var entries []Entry
	for _, child := range children {
		if !child.IsDir() || !isCacheKey(child.Name()) {
			continue
		}
		entryPath := filepath.Join(root, child.Name())
		kind := remoteEntryKind(entryPath)
		if kind != "http-file" && kind != "git-repo" {
			continue
		}
		entry, ok := buildEntry(SourceRemote, kind, child.Name(), entryPath, root)
		if ok {
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

func buildEntry(source Source, kind, key, entryPath, root string) (Entry, bool) {
	info, err := os.Lstat(entryPath)
	if err != nil || !info.IsDir() {
		return Entry{}, false
	}
	size, _ := pathSize(entryPath)
	metadata, metadataErr := ReadMetadata(entryPath, source, kind, key)
	legacy := metadata == nil
	entry := Entry{
		Source:       source,
		Kind:         kind,
		Key:          key,
		Path:         entryPath,
		MetadataPath: MetadataPath(entryPath),
		SizeBytes:    size,
		ModifiedAt:   info.ModTime(),
		Legacy:       legacy,
		Metadata:     metadata,
		cacheRoot:    root,
	}
	if metadataErr != nil {
		entry.Legacy = true
		entry.Metadata = nil
	}
	return entry, true
}

func removeEntries(entries []Entry) error {
	for _, entry := range entries {
		if err := validateEntryRemoval(entry); err != nil {
			return err
		}
		if err := os.RemoveAll(entry.Path); err != nil {
			return err
		}
	}
	return nil
}

func validateEntryRemoval(entry Entry) error {
	if strings.TrimSpace(entry.Path) == "" || strings.TrimSpace(entry.cacheRoot) == "" {
		return fmt.Errorf("cache entry path and root are required before removal")
	}
	entryPath, err := filepath.Abs(entry.Path)
	if err != nil {
		return err
	}
	root, err := filepath.Abs(entry.cacheRoot)
	if err != nil {
		return err
	}
	entryPath = filepath.Clean(entryPath)
	root = filepath.Clean(root)
	if entryPath == root {
		return fmt.Errorf("refusing to remove cache root %s", root)
	}
	inside, err := pathInside(root, entryPath)
	if err != nil {
		return err
	}
	if !inside {
		return fmt.Errorf("cache entry %s is outside cache root %s", entryPath, root)
	}
	return nil
}

func resolveCacheRoot(root string, forbiddenRoots []string) (string, bool, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", false, nil
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", false, err
	}
	absRoot = filepath.Clean(absRoot)
	if inside, matched, err := pathInsideAny(absRoot, forbiddenRoots); err != nil {
		return "", false, err
	} else if inside {
		return "", false, fmt.Errorf("cache root %q must not be inside protected root %q", absRoot, matched)
	}
	if gitRoot, inside, err := gitRepositoryRoot(absRoot); err != nil {
		return "", false, err
	} else if inside {
		return "", false, fmt.Errorf("cache root %q must not be inside Git repository %q", absRoot, gitRoot)
	}
	return absRoot, true, nil
}

func enabledSources(sources []Source) map[Source]bool {
	out := map[Source]bool{}
	if len(sources) == 0 {
		out[SourceGit] = true
		out[SourceChart] = true
		out[SourceRemote] = true
		return out
	}
	for _, source := range sources {
		out[source] = true
	}
	return out
}

func isCacheKey(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func remoteEntryKind(entryPath string) string {
	if info, err := os.Lstat(filepath.Join(entryPath, "repo")); err == nil && info.IsDir() {
		return "git-repo"
	}
	if info, err := os.Lstat(filepath.Join(entryPath, "resource.yaml")); err == nil && info.Mode().IsRegular() {
		return "http-file"
	}
	return ""
}

func entryAgeTime(entry Entry) time.Time {
	if entry.Metadata != nil && !entry.Metadata.UpdatedAt.IsZero() {
		return entry.Metadata.UpdatedAt
	}
	return entry.ModifiedAt
}

func sortEntries(entries []Entry) {
	sort.Slice(entries, func(i, j int) bool {
		left := entries[i]
		right := entries[j]
		if left.Source != right.Source {
			return left.Source < right.Source
		}
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		return left.Key < right.Key
	})
}

func pathSize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total, err
}

func pathInsideAny(targetPath string, roots []string) (bool, string, error) {
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		inside, err := pathInside(root, targetPath)
		if err != nil {
			return false, "", err
		}
		if inside {
			absRoot, _ := filepath.Abs(root)
			return true, filepath.Clean(absRoot), nil
		}
	}
	return false, "", nil
}

func pathInside(root, targetPath string) (bool, error) {
	resolvedRoot, err := resolvePathForContainment(root)
	if err != nil {
		return false, err
	}
	resolvedTarget, err := resolvePathForContainment(targetPath)
	if err != nil {
		return false, err
	}
	rel, err := filepath.Rel(resolvedRoot, resolvedTarget)
	if err != nil {
		return false, err
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)), nil
}

func resolvePathForContainment(targetPath string) (string, error) {
	absPath, err := filepath.Abs(targetPath)
	if err != nil {
		return "", err
	}
	current := filepath.Clean(absPath)
	var missing []string
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return filepath.Clean(resolved), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return filepath.Clean(absPath), nil
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func gitRepositoryRoot(targetPath string) (string, bool, error) {
	resolved, err := resolvePathForContainment(targetPath)
	if err != nil {
		return "", false, err
	}
	current := resolved
	for {
		if info, err := os.Lstat(filepath.Join(current, ".git")); err == nil && (info.IsDir() || info.Mode().IsRegular()) {
			return current, true, nil
		} else if err != nil && !os.IsNotExist(err) {
			return "", false, err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", false, nil
		}
		current = parent
	}
}
