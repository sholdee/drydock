package cache

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/sholdee/drydock/internal/rendercache"
)

type Source string

const (
	SourceGit    Source = "git"
	SourceChart  Source = "chart"
	SourceRemote Source = "remote"
	SourceRender Source = "render"
	// SourceOCI is the first-class OCI artifact image cache. The name does not
	// collide with the chart cache's "oci" repository kind: Source and Kind are
	// distinct fields on entries and metadata, and chart "oci" only appears as
	// a Kind under SourceChart (listChartEntries below).
	SourceOCI Source = "oci"
)

type Options struct {
	GitCacheDir    string
	ChartCacheDir  string
	RemoteCacheDir string
	RenderCacheDir string
	OCICacheDir    string
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
	// PruneReason is set to "age" or "size" only on entries selected for removal
	// by Prune(). Zero value is omitted from JSON/YAML output so that cache list
	// output remains byte-identical to pre-prune output.
	PruneReason string `json:"pruneReason,omitempty" yaml:"pruneReason,omitempty"`
	cacheRoot   string
}

type OperationOptions struct {
	Options
	OlderThan           time.Duration
	DryRun              bool
	Yes                 bool
	Source              Source
	Kind                string
	Key                 string
	All                 bool
	RenderCacheMaxBytes int64
	// MaxBytes caps the summed SizeBytes of the entries selected by the source/kind
	// filters. Evicts least-recently-used (oldest entryAgeTime) entries until the
	// total is at or below this cap. 0 disables the size phase. Named to parallel
	// RenderCacheMaxBytes.
	MaxBytes int64
}

type OperationResult struct {
	Entries      []Entry                  `json:"entries" yaml:"entries"`
	RemovedCount int                      `json:"removedCount" yaml:"removedCount"`
	DryRun       bool                     `json:"dryRun" yaml:"dryRun"`
	RenderSweep  *rendercache.SweepResult `json:"renderSweep,omitempty" yaml:"renderSweep,omitempty"`
	// SizeEvictedBytes is the sum of SizeBytes for all entries removed by the size
	// phase. Populated on both dry-run and real runs. Always present in JSON/YAML
	// output (not omitempty) for a consistent numeric summary.
	SizeEvictedBytes int64 `json:"sizeEvictedBytes" yaml:"sizeEvictedBytes"`
	// TotalSizeBytes is the selected-set total after both age and size phases,
	// BEFORE the render sweep. The sweep (which runs after removals on real runs,
	// never on dry-run) can trim renders further; its effect is reported separately
	// in RenderSweep. Always present in JSON/YAML output (not omitempty).
	TotalSizeBytes int64 `json:"totalSizeBytes" yaml:"totalSizeBytes"`
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
	if enabled[SourceOCI] {
		ociEntries, err := listSimpleEntries(SourceOCI, "image", opts.OCICacheDir, opts.ForbiddenRoots)
		if err != nil {
			return nil, err
		}
		entries = append(entries, ociEntries...)
	}
	if enabled[SourceRender] {
		renderEntries, err := listRenderEntries(opts.RenderCacheDir, opts.ForbiddenRoots)
		if err != nil {
			return nil, err
		}
		entries = append(entries, renderEntries...)
	}
	sortEntries(entries)
	return entries, nil
}

func Prune(opts OperationOptions) (OperationResult, error) {
	if opts.OlderThan <= 0 && opts.MaxBytes <= 0 {
		return OperationResult{}, fmt.Errorf("at least one of older-than or max-size is required")
	}
	if !opts.DryRun && !opts.Yes {
		return OperationResult{}, fmt.Errorf("--yes is required for non-dry-run cache prune")
	}
	entries, err := List(opts.Options)
	if err != nil {
		return OperationResult{}, err
	}
	selected, result := buildPruneResult(entries, opts)
	if opts.DryRun {
		return result, nil
	}
	if err := removeEntries(selected); err != nil {
		return OperationResult{}, err
	}
	result.RemovedCount = len(selected)
	if enabledSources(opts.Sources)[SourceRender] && (opts.Source == "" || opts.Source == SourceRender) {
		sweep, err := pruneRenderSweep(opts.RenderCacheDir, opts.RenderCacheMaxBytes, opts.ForbiddenRoots)
		if err != nil {
			return OperationResult{}, err
		}
		result.RenderSweep = sweep
	}
	return result, nil
}

// buildPruneResult runs phase A (age) and phase B (size) selection and
// assembles the OperationResult fields that are populated on both dry and real
// runs. It is extracted from Prune to keep Prune's cyclomatic complexity low.
func buildPruneResult(entries []Entry, opts OperationOptions) ([]Entry, OperationResult) {
	ageSelected := selectAgeEntries(entries, opts)
	sizeSelected := selectSizePhase(entries, ageSelected, opts)

	// Merge: age-selected first, then size-selected.
	selected := make([]Entry, 0, len(ageSelected)+len(sizeSelected))
	selected = append(selected, ageSelected...)
	selected = append(selected, sizeSelected...)

	totalSizeBytes := computeTotalSizeBytes(entries, selected, opts)
	sizeEvictedBytes := sumSizeBytes(sizeSelected)

	result := OperationResult{
		Entries:          selected,
		DryRun:           opts.DryRun,
		SizeEvictedBytes: sizeEvictedBytes,
		TotalSizeBytes:   totalSizeBytes,
	}
	return selected, result
}

// selectAgeEntries runs phase A: selects entries older than opts.OlderThan and
// tags them with PruneReason "age". Returns nil when OlderThan is not set.
func selectAgeEntries(entries []Entry, opts OperationOptions) []Entry {
	if opts.OlderThan <= 0 {
		return nil
	}
	cutoff := time.Now().Add(-opts.OlderThan)
	selected := selectPruneEntries(entries, opts.Source, opts.Kind, cutoff)
	for i := range selected {
		selected[i].PruneReason = "age"
	}
	return selected
}

// selectSizePhase runs phase B: LRU eviction until the filtered-source total
// is at or below opts.MaxBytes, excluding entries already selected by the age
// phase. Tags the selected (evicted) entries with PruneReason "size". Returns
// nil when MaxBytes is not set.
//
// Note: the render cache already uses a 90% hysteresis target to avoid
// re-triggering on every post-run write. We evict to exactly ≤ cap here
// because this is a manual command with no re-trigger loop — predictability
// wins over hysteresis.
func selectSizePhase(entries []Entry, ageSelected []Entry, opts OperationOptions) []Entry {
	if opts.MaxBytes <= 0 {
		return nil
	}
	selected := selectSizeEntries(entries, ageSelected, opts.Source, opts.Kind, opts.MaxBytes)
	for i := range selected {
		selected[i].PruneReason = "size"
	}
	return selected
}

// computeTotalSizeBytes returns the sum of SizeBytes for all in-scope entries
// that were NOT selected for removal (i.e. entries surviving both phases),
// BEFORE the render sweep runs.
func computeTotalSizeBytes(entries []Entry, selected []Entry, opts OperationOptions) int64 {
	selectedPaths := make(map[string]struct{}, len(selected))
	for _, e := range selected {
		selectedPaths[e.Path] = struct{}{}
	}
	var total int64
	for _, e := range entries {
		if opts.Source != "" && e.Source != opts.Source {
			continue
		}
		if opts.Kind != "" && e.Kind != opts.Kind {
			continue
		}
		if _, removed := selectedPaths[e.Path]; !removed {
			total += e.SizeBytes
		}
	}
	return total
}

// sumSizeBytes returns the sum of SizeBytes across a slice of entries.
func sumSizeBytes(entries []Entry) int64 {
	var total int64
	for _, e := range entries {
		total += e.SizeBytes
	}
	return total
}

func selectPruneEntries(entries []Entry, source Source, kind string, cutoff time.Time) []Entry {
	selected := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		if source != "" && entry.Source != source {
			continue
		}
		if kind != "" && entry.Kind != kind {
			continue
		}
		if entryAgeTime(entry).Before(cutoff) {
			selected = append(selected, entry)
		}
	}
	return selected
}

// selectSizeEntries evicts oldest-first (by entryAgeTime) until the total size
// of in-scope entries is at or below maxBytes. alreadySelected is the age-phase
// result; its entries are excluded from the candidate pool and their sizes are
// already subtracted from the total. Ties in entryAgeTime are broken by Source,
// Kind, then Key for deterministic output.
func selectSizeEntries(entries []Entry, alreadySelected []Entry, source Source, kind string, maxBytes int64) []Entry {
	// Build a set of paths already removed by the age phase.
	excludePaths := make(map[string]struct{}, len(alreadySelected))
	for _, e := range alreadySelected {
		excludePaths[e.Path] = struct{}{}
	}

	// Collect candidates (in scope, not yet age-selected) and compute current total.
	var candidates []Entry
	var total int64
	for _, e := range entries {
		if source != "" && e.Source != source {
			continue
		}
		if kind != "" && e.Kind != kind {
			continue
		}
		if _, excluded := excludePaths[e.Path]; excluded {
			continue
		}
		candidates = append(candidates, e)
		total += e.SizeBytes
	}

	if total <= maxBytes {
		// Already at or below cap; nothing to evict.
		return nil
	}

	// Sort candidates oldest-first (LRU) with deterministic tie-break:
	// (entryAgeTime, Source, Kind, Key) ascending.
	sort.Slice(candidates, func(i, j int) bool {
		ti := entryAgeTime(candidates[i])
		tj := entryAgeTime(candidates[j])
		if !ti.Equal(tj) {
			return ti.Before(tj)
		}
		if candidates[i].Source != candidates[j].Source {
			return candidates[i].Source < candidates[j].Source
		}
		if candidates[i].Kind != candidates[j].Kind {
			return candidates[i].Kind < candidates[j].Kind
		}
		return candidates[i].Key < candidates[j].Key
	})

	// Evict oldest first until total ≤ cap.
	var selected []Entry
	for _, e := range candidates {
		if total <= maxBytes {
			break
		}
		selected = append(selected, e)
		total -= e.SizeBytes
	}
	return selected
}

func pruneRenderSweep(dir string, maxBytes int64, forbiddenRoots []string) (*rendercache.SweepResult, error) {
	root, ok, err := resolveCacheRoot(dir, forbiddenRoots)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil //nolint:nilnil // no render root configured; nil sweep pointer signals "sweep not run"
	}
	sweep, err := rendercache.SweepDir(root, maxBytes)
	if err != nil {
		return nil, err
	}
	return &sweep, nil
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
		entry, ok := buildEntry(source, kind, child.Name(), GitEntryPath(root, child.Name()), root)
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
		kindRoot := ChartKindRoot(root, kind)
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
			entry, ok := buildEntry(SourceChart, kind, child.Name(), ChartEntryPath(root, kind, child.Name()), root)
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
		entryPath := RemoteEntryPath(root, child.Name())
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

func listRenderEntries(root string, forbiddenRoots []string) ([]Entry, error) {
	root, ok, err := resolveCacheRoot(root, forbiddenRoots)
	if err != nil || !ok {
		return nil, err
	}
	files, err := rendercache.Entries(root)
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(files))
	for _, file := range files {
		entries = append(entries, Entry{
			Source:     SourceRender,
			Kind:       "output",
			Key:        file.Key,
			Path:       file.Path,
			SizeBytes:  file.SizeBytes,
			ModifiedAt: file.ModifiedAt,
			cacheRoot:  root,
		})
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
		out[SourceRender] = true
		out[SourceOCI] = true
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
			for _, v := range slices.Backward(missing) {
				resolved = filepath.Join(resolved, v)
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
