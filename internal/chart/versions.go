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
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/argoproj/argo-cd/v3/util/versions"
	"github.com/sholdee/drydock/internal/cache"
	"github.com/sholdee/drydock/internal/filelock"
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
	// `helm dependency build` fetches from the repository that the
	// repo-server added with `helm repo add` (Manager.findChartURL and
	// findVersionedEntry, helm v4.3.1 pkg/downloader/manager.go:724-806):
	// the first index entry, from highest to lowest, that has an archive
	// URL and whose version equals the locked one as semver, or as text
	// when the locked version does not parse. So a partial version such as
	// "1.2" in a hand-edited lock takes 1.2.0, not the highest 1.2.x.
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
		// The cache entry carries the full version that a partial locked
		// version matches, so a Kustomize helmCharts "1.2" never reads it.
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
	Found bool `json:"found"`
	// Versions are the entries as helm loads the index (loadHelmIndex):
	// valid entries only, in helm's sort order. For OCI they are the tags.
	Versions []recordedChartVersion `json:"versions,omitempty"`
	// AllVersions are the versions of every HTTP index entry as written, in
	// index order, which the repo-server's own index read gives
	// versions.MaxVersion.
	AllVersions []string `json:"allVersions,omitempty"`
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

// tags returns the versions that the repo-server's MaxVersion reads.
func (record chartVersionsRecord) tags() []string {
	if len(record.AllVersions) > 0 {
		return append([]string(nil), record.AllVersions...)
	}
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
		entry := repositoryChartVersion{Version: version.Version}
		if version.HasURL {
			// Only whether an entry has an archive URL counts here.
			entry.URLs = []string{""}
		}
		out = append(out, entry)
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

// versionsRecordLockTimeout bounds the wait for the lock on the version
// lists. A use that cannot get it in time records nothing.
var versionsRecordLockTimeout = 5 * time.Second

// writeVersionsRecord writes record as the version list of request.
func writeVersionsRecord(cacheDir string, request Request, record chartVersionsRecord, forbiddenRoots []string) {
	updateVersionsRecord(cacheDir, request, forbiddenRoots, func(chartVersionsRecord, bool) (chartVersionsRecord, bool) {
		return record, true
	})
}

// touchVersionsRecord records that a resolve used the version list of
// request now. It re-reads the list under the lock and keeps it, so a newer
// list that another process wrote stays.
func touchVersionsRecord(cacheDir string, request Request, forbiddenRoots []string) {
	now := time.Now().UTC()
	updateVersionsRecord(cacheDir, request, forbiddenRoots, func(current chartVersionsRecord, ok bool) (chartVersionsRecord, bool) {
		if !ok || current.UsedAt.After(now) {
			return current, false
		}
		current.UsedAt = now
		return current, true
	})
}

// updateVersionsRecord changes the version list of request under a file
// lock that every drydock process sharing the cache takes, so a use never
// overwrites a list that another process wrote in between. update gets the
// current list and whether it exists, and returns the new list and whether
// to write it. It is best-effort: a cache that cannot be locked or written,
// such as a read-only one, only makes a later offline resolve miss or a
// prune see an older use, and never fails the render. The write is atomic
// (temp+rename).
func updateVersionsRecord(cacheDir string, request Request, forbiddenRoots []string, update func(chartVersionsRecord, bool) (chartVersionsRecord, bool)) {
	path, err := versionsRecordPath(cacheDir, request)
	if err != nil || rejectForbiddenCachePath(path, forbiddenRoots) != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), versionsRecordLockTimeout)
	defer cancel()
	lock, err := filelock.Acquire(ctx, filepath.Join(filepath.Dir(path), ".lock"))
	if err != nil {
		return
	}
	defer lock.Release()
	record, write := update(readVersionsRecord(cacheDir, request))
	if !write {
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
		touchVersionsRecord(opts.CacheDir, request, opts.ForbiddenRoots)
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
		raw, found := fetched.Entries[request.Name]
		record.Found = found
		for _, entry := range raw {
			record.AllVersions = append(record.AllVersions, entry.Version)
		}
		entries, err := fetched.helmEntries(request.Name)
		if err != nil {
			return resolvedVersion{}, err
		}
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
		entry, err := helmFindVersionedEntry(record.entries(), request.Name, version)
		return entry.Version, err
	case VersionAsWritten:
		return version, nil
	default:
		return version, nil
	}
}

// helmFindVersionedEntry is findVersionedEntry (helm v4.3.1
// pkg/downloader/manager.go:781-806) on entries in the order that helm loads
// an index (loadHelmIndex): the first entry with an archive URL whose
// version equals version (versionEquals). A miss fails with the text of
// `helm dependency build` (downloadAll, manager.go:316-319).
func helmFindVersionedEntry(entries []repositoryChartVersion, name, version string) (repositoryChartVersion, error) {
	for _, entry := range entries {
		if len(entry.URLs) == 0 {
			continue
		}
		if version == "" || helmVersionEquals(version, entry.Version) {
			return entry, nil
		}
	}
	return repositoryChartVersion{}, fmt.Errorf("chart %s version %s: could not find : no matching version", name, version)
}

// helmVersionEquals is versionEquals (helm v4.3.1
// pkg/downloader/manager.go:795-806): equal as semver, or as text when v1
// does not parse.
func helmVersionEquals(v1, v2 string) bool {
	sv1, err := semver.NewVersion(v1)
	if err != nil {
		return v1 == v2
	}
	sv2, err := semver.NewVersion(v2)
	if err != nil {
		return false
	}
	return sv1.Equal(sv2)
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
// the first version, in the order that helm loads the index or lists the
// tags, that parses as semver, has an archive URL (HTTP only), and matches
// the version as a constraint.
func resolveHelmDependencyVersion(request Request, record chartVersionsRecord) (string, error) {
	constraint, err := semver.NewConstraint(request.Version)
	if err != nil {
		return "", fmt.Errorf("dependency %q has an invalid version/constraint format: %w", request.Name, err)
	}
	if request.Kind == RepositoryHTTP && !record.Found {
		return "", fmt.Errorf("%s chart not found in repo %s", request.Name, request.Repository)
	}
	for _, candidate := range record.Versions {
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

// errChartVersionNotFound marks an index without a version that a request
// names.
var errChartVersionNotFound = errors.New("not found in repository index")

// helmIndexGet is IndexFile.Get (helm v4.3.1 pkg/repo/v1/index.go:196-241),
// which helm pull uses to find the archive of a version, on entries in the
// order that helm loads an index (loadHelmIndex): the entry whose version
// is version as written, or else the first entry that matches version as a
// constraint.
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
	for _, entry := range entries {
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
