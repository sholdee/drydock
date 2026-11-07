package chart

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/argoproj/argo-cd/v3/util/versions"
	"github.com/sholdee/drydock/internal/cache"
)

// VersionPolicy says how a chart request names its version.
type VersionPolicy string

const (
	// VersionAsWritten fetches the repository index entry whose version is
	// the request version as written, or without a leading "v". It is the
	// policy of Kustomize helmCharts entries.
	VersionAsWritten VersionPolicy = ""
	// VersionArgoTargetRevision is the targetRevision of a chart source, as
	// the Argo CD v3.6.1 repo-server resolves it
	// (newHelmClientResolveRevision, reposerver/repository/repository.go:
	// 3029-3066). A version that parses as semver (versions.IsVersion), also
	// "1.2", stays as written. On an HTTP repository helm pull then takes the
	// index entry with that version, or else the highest one that matches it
	// as a constraint (helmIndexGet), so a partial version such as "1.2"
	// resolves like a range. On an OCI repository helm pull takes the tag as
	// written. Any other revision is a constraint: the repo-server takes the
	// highest index version or registry tag that matches it
	// (versions.MaxVersion), and helm pull fetches that version.
	VersionArgoTargetRevision VersionPolicy = "argo-target-revision"
	// VersionHelmDependency is the version of a Chart.yaml dependency
	// without a Chart.lock, as `helm dependency build` resolves it
	// (Manager.Update and resolver.Resolve, helm v4.3.1
	// internal/resolver/resolver.go:56-205): the highest index version
	// with an archive URL, or the highest registry tag, that matches the
	// version as a constraint. On an OCI repository, a version that parses
	// as semver is an explicit tag and stays as written.
	VersionHelmDependency VersionPolicy = "helm-dependency"
	// VersionHelmLocked is the version of a Chart.lock entry, which
	// `helm dependency build` fetches as helm pull does (helmIndexGet). A
	// partial version in a hand-edited lock on an HTTP repository resolves
	// like a partial targetRevision.
	VersionHelmLocked VersionPolicy = "helm-locked"
)

// needsVersionResolution reports whether request names a version that the
// acquirer must resolve against the repository index or the registry tags
// before it can look in the cache.
func needsVersionResolution(request Request) bool {
	version := strings.TrimSpace(request.Version)
	switch request.VersionPolicy {
	case VersionArgoTargetRevision:
		if request.Kind == RepositoryOCI {
			// helm pull takes a semver version as an exact tag
			// (registry.Client.ValidateReference, helm v4.3.1
			// pkg/registry/client.go:883-887).
			return !versions.IsVersion(version)
		}
		return !IsExactVersion(version)
	case VersionHelmDependency:
		if request.Kind == RepositoryOCI {
			// resolver.Resolve uses an explicit OCI version as written
			// (helm v4.3.1 internal/resolver/resolver.go:135-147).
			_, err := semver.NewVersion(version)
			return err != nil
		}
		return !IsExactVersion(version)
	case VersionHelmLocked:
		return request.Kind == RepositoryHTTP && !IsExactVersion(version)
	case VersionAsWritten:
		return false
	default:
		return false
	}
}

// exactVersionPattern is a full major.minor.patch version, with an optional
// leading "v", prerelease, and build metadata.
var exactVersionPattern = regexp.MustCompile(`^v?\d+\.\d+\.\d+([-+].*)?$`)

// IsExactVersion reports whether version names one full chart version,
// such as 1.2.3 or v1.2.3-rc.1. A partial version such as 1.2 or 1 is not
// exact: on an HTTP repository helm takes the highest version that matches
// it, so it resolves like a range.
func IsExactVersion(version string) bool {
	version = strings.TrimSpace(version)
	if !exactVersionPattern.MatchString(version) {
		return false
	}
	_, err := semver.NewVersion(version)
	return err == nil
}

// The version list of each chart that an online resolve read is recorded
// under cache.ChartVersionsRecordDir, so a version range resolves under
// --offline. The cache prune removes old lists with an age prune.

// chartVersionsRecord is the version list of one chart in one repository:
// the index entries of an HTTP repository, with whether each one has an
// archive URL, or the tags of an OCI repository.
type chartVersionsRecord struct {
	// Found is false when the HTTP index has no entry for the chart.
	Found    bool                   `json:"found"`
	Versions []recordedChartVersion `json:"versions,omitempty"`
	// FetchedAt is when the online resolve read the list. An offline
	// resolve from a list older than the chart cache max age reports the
	// result stale, as for an expired chart cache entry.
	FetchedAt time.Time `json:"fetchedAt,omitzero"`
	// UsedAt is when a resolve, online or offline, last used the list. An
	// age prune removes lists by this time, as it prunes chart entries by
	// last use.
	UsedAt time.Time `json:"usedAt,omitzero"`
}

type recordedChartVersion struct {
	Version string `json:"version"`
	HasURL  bool   `json:"hasURL,omitempty"`
}

func (record chartVersionsRecord) tags() []string {
	tags := make([]string, 0, len(record.Versions))
	for _, version := range record.Versions {
		tags = append(tags, version.Version)
	}
	return tags
}

// entries returns the recorded versions as index entries, for helmIndexGet.
func (record chartVersionsRecord) entries() []repositoryChartVersion {
	out := make([]repositoryChartVersion, 0, len(record.Versions))
	for _, version := range record.Versions {
		out = append(out, repositoryChartVersion{Version: version.Version})
	}
	return out
}

func versionsRecordPath(cacheDir string, request Request) (string, error) {
	normalized, err := NormalizeCacheRepository(request.Repository, request.Kind)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{string(request.Kind), normalized, strings.TrimSpace(request.Name)}, "\x00")))
	return filepath.Join(cache.ChartVersionsRecordDir(cacheDir, string(request.Kind)), hex.EncodeToString(sum[:])+".json"), nil
}

func readVersionsRecord(cacheDir string, request Request) (chartVersionsRecord, bool) {
	path, err := versionsRecordPath(cacheDir, request)
	if err != nil {
		return chartVersionsRecord{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return chartVersionsRecord{}, false
	}
	var record chartVersionsRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return chartVersionsRecord{}, false
	}
	return record, true
}

// writeVersionsRecord is best-effort: a failed write only makes a later
// offline resolve miss. The write is atomic (temp+rename).
func writeVersionsRecord(cacheDir string, request Request, record chartVersionsRecord, forbiddenRoots []string) {
	path, err := versionsRecordPath(cacheDir, request)
	if err != nil || rejectForbiddenCachePath(path, forbiddenRoots) != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	data, err := json.Marshal(record)
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	_ = os.Rename(tmpName, path)
}

// resolvedVersion is the version that a request named, as resolveVersion
// found it.
type resolvedVersion struct {
	version string
	// index is the HTTP repository index that the resolve read, if any.
	index *repositoryIndex
	// stale reports an offline resolve from a version list older than the
	// chart cache max age, or of unknown age under a max age.
	stale bool
	// listFetchedAt is when an online run fetched the version list that an
	// offline resolve used, zero when unknown.
	listFetchedAt time.Time
}

// resolveVersion returns the version that request names, under its
// VersionPolicy. Online it reads the index or the registry tags and
// records their versions with the fetch time. Offline it resolves from
// that record, and fails as a cache miss without one. An offline resolve
// from a record past opts.MaxAge is still used and reported stale, as an
// expired chart cache entry is (Expired).
func (acquirer DefaultAcquirer) resolveVersion(ctx context.Context, request Request, opts Options) (resolvedVersion, error) {
	if opts.Offline {
		record, ok := readVersionsRecord(opts.CacheDir, request)
		if !ok {
			return resolvedVersion{}, fmt.Errorf("offline cache miss for chart %s %s: no recorded version list to resolve the version range", request.Name, request.Version)
		}
		resolved, err := selectChartVersion(request, record)
		stale := Expired(record.FetchedAt, !record.FetchedAt.IsZero(), opts.MaxAge, time.Now())
		used := record
		used.UsedAt = time.Now().UTC()
		writeVersionsRecord(opts.CacheDir, request, used, opts.ForbiddenRoots)
		return resolvedVersion{version: resolved, stale: stale, listFetchedAt: record.FetchedAt}, err
	}
	now := time.Now().UTC()
	record := chartVersionsRecord{FetchedAt: now, UsedAt: now}
	var index *repositoryIndex
	switch request.Kind {
	case RepositoryHTTP:
		fetched, err := acquirer.fetchHTTPIndex(ctx, request, opts)
		if err != nil {
			return resolvedVersion{}, err
		}
		index = &fetched
		entries, found := fetched.Entries[request.Name]
		record.Found = found
		for _, entry := range entries {
			record.Versions = append(record.Versions, recordedChartVersion{Version: entry.Version, HasURL: len(entry.URLs) > 0})
		}
	case RepositoryOCI:
		tags, err := acquirer.listOCIChartTags(ctx, request, opts)
		if err != nil {
			return resolvedVersion{}, err
		}
		record.Found = true
		for _, tag := range tags {
			record.Versions = append(record.Versions, recordedChartVersion{Version: tag, HasURL: true})
		}
	default:
		return resolvedVersion{}, fmt.Errorf("unsupported chart repository kind %q", request.Kind)
	}
	writeVersionsRecord(opts.CacheDir, request, record, opts.ForbiddenRoots)
	resolved, err := selectChartVersion(request, record)
	return resolvedVersion{version: resolved, index: index}, err
}

// selectChartVersion picks the version that request names from record.
func selectChartVersion(request Request, record chartVersionsRecord) (string, error) {
	version := strings.TrimSpace(request.Version)
	switch request.VersionPolicy {
	case VersionArgoTargetRevision:
		if request.Kind == RepositoryHTTP && versions.IsVersion(version) {
			// A partial version: helm pull takes the index entry that
			// IndexFile.Get returns for it.
			return helmIndexGetVersion(request, record)
		}
		if request.Kind == RepositoryHTTP && !record.Found {
			// helm.Index.GetEntries (util/helm/index.go:27-33 at v3.6.1).
			return "", fmt.Errorf("chart '%s' not found in index", request.Name)
		}
		resolved, err := versions.MaxVersion(version, record.tags(), "")
		if err != nil {
			return "", fmt.Errorf("invalid revision: %w", err)
		}
		return resolved, nil
	case VersionHelmDependency:
		return resolveHelmDependencyVersion(request, record)
	case VersionHelmLocked:
		return helmIndexGetVersion(request, record)
	case VersionAsWritten:
		return version, nil
	default:
		return version, nil
	}
}

// helmIndexGetVersion returns the version of the index entry that helm pull
// takes for the version of request (helmIndexGet).
func helmIndexGetVersion(request Request, record chartVersionsRecord) (string, error) {
	version := strings.TrimSpace(request.Version)
	entry, err := helmIndexGet(record.entries(), request.Name, version)
	if errors.Is(err, errChartVersionNotFound) {
		return "", fmt.Errorf("chart %s version %s not found in repository index", request.Name, version)
	}
	return entry.Version, err
}

// resolveHelmDependencyVersion is the remote branch of resolver.Resolve
// (helm v4.3.1 internal/resolver/resolver.go:56-205) for one dependency:
// the first version of the index, sorted from highest to lowest, that
// parses as semver, has an archive URL (HTTP only), and matches the
// version as a constraint.
func resolveHelmDependencyVersion(request Request, record chartVersionsRecord) (string, error) {
	constraint, err := semver.NewConstraint(request.Version)
	if err != nil {
		return "", fmt.Errorf("dependency %q has an invalid version/constraint format: %w", request.Name, err)
	}
	if request.Kind == RepositoryHTTP && !record.Found {
		return "", fmt.Errorf("%s chart not found in repo %s", request.Name, request.Repository)
	}
	for _, candidate := range sortedRecordedVersions(record.Versions) {
		v, err := semver.NewVersion(candidate.Version)
		if err != nil || (request.Kind == RepositoryHTTP && !candidate.HasURL) {
			continue
		}
		if constraint.Check(v) {
			return v.Original(), nil
		}
	}
	return "", fmt.Errorf("can't get a valid version for 1 subchart(s): %q (repository %q, version %q). Make sure a matching chart version exists in the repo, or change the version constraint in Chart.yaml", request.Name, request.Repository, request.Version)
}

// sortedRecordedVersions sorts versions from highest to lowest semver, with
// the ones that do not parse last, as helm sorts a loaded index
// (IndexFile.SortEntries, pkg/repo/v1/index.go:68-79,173-177).
func sortedRecordedVersions(input []recordedChartVersion) []recordedChartVersion {
	out := append([]recordedChartVersion(nil), input...)
	sort.SliceStable(out, func(i, j int) bool {
		return semverHigher(out[i].Version, out[j].Version)
	})
	return out
}

// semverHigher reports whether version a sorts before version b in a helm
// index sorted from highest to lowest: a parses as semver and b does not,
// or both parse and a is higher.
func semverHigher(a, b string) bool {
	va, errA := semver.NewVersion(a)
	vb, errB := semver.NewVersion(b)
	switch {
	case errA != nil:
		return false
	case errB != nil:
		return true
	default:
		return vb.LessThan(va)
	}
}

// errChartVersionNotFound marks an index without a version that a request
// names.
var errChartVersionNotFound = errors.New("not found in repository index")

// helmIndexGet is IndexFile.Get (helm v4.3.1 pkg/repo/v1/index.go:196-241),
// which helm pull and `helm dependency build` use to find the archive of a
// version: the entry whose version is version as written, or else the
// highest entry that matches version as a constraint.
func helmIndexGet(entries []repositoryChartVersion, name, version string) (repositoryChartVersion, error) {
	var constraint *semver.Constraints
	if version == "" {
		constraint, _ = semver.NewConstraint("*")
	} else {
		var err error
		constraint, err = semver.NewConstraint(version)
		if err != nil {
			return repositoryChartVersion{}, err
		}
		for _, entry := range entries {
			if entry.Version == version {
				return entry, nil
			}
		}
	}
	sorted := append([]repositoryChartVersion(nil), entries...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return semverHigher(sorted[i].Version, sorted[j].Version)
	})
	for _, entry := range sorted {
		test, err := semver.NewVersion(entry.Version)
		if err != nil {
			continue
		}
		if constraint.Check(test) {
			return entry, nil
		}
	}
	return repositoryChartVersion{}, fmt.Errorf("chart %s version %s %w", name, version, errChartVersionNotFound)
}
