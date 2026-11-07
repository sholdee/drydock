package chart

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/argoproj/argo-cd/v3/util/versions"
)

// rangeIndexVersions are the versions of the chart "demo" in the range test
// repository. 1.1.0 has no archive URL. The order is not sorted, as in a
// real index.
var rangeIndexVersions = []string{"1.0.0", "1.2.0", "2.0.0", "1.2.3", "1.3.0-rc.1", "1.1.0", "v1.4.0", "not-semver"}

// newRangeIndexServer serves an index with rangeIndexVersions and an
// archive for each version, and counts the requests per path.
func newRangeIndexServer(t *testing.T) (*httptest.Server, map[string]int) {
	t.Helper()
	requests := map[string]int{}
	archives := map[string][]byte{}
	var index strings.Builder
	index.WriteString("apiVersion: v1\nentries:\n  demo:\n")
	for _, version := range rangeIndexVersions {
		fmt.Fprintf(&index, "    - version: %q\n", version)
		if version == "1.1.0" {
			continue
		}
		fmt.Fprintf(&index, "      urls: [demo-%s.tgz]\n", version)
		archives["/demo-"+version+".tgz"] = chartArchive(t, "demo", map[string]string{
			"Chart.yaml": "apiVersion: v2\nname: demo\nversion: 0.0.1\ndescription: " + version + "\n",
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests[r.URL.Path]++
		if r.URL.Path == "/index.yaml" {
			_, _ = w.Write([]byte(index.String()))
			return
		}
		if archive, ok := archives[r.URL.Path]; ok {
			_, _ = w.Write(archive)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	return server, requests
}

// upstreamChartVersions is repo.ChartVersions (helm v4.3.1
// pkg/repo/v1/index.go:58-79), copied because the package pulls in helm's
// plugin runtime.
type upstreamChartVersions []string

func (c upstreamChartVersions) Len() int      { return len(c) }
func (c upstreamChartVersions) Swap(i, j int) { c[i], c[j] = c[j], c[i] }
func (c upstreamChartVersions) Less(a, b int) bool {
	i, err := semver.NewVersion(c[a])
	if err != nil {
		return true
	}
	j, err := semver.NewVersion(c[b])
	if err != nil {
		return false
	}
	return i.LessThan(j)
}

// upstreamHelmIndexGet is helm pull's index lookup (IndexFile.Get, helm
// v4.3.1 pkg/repo/v1/index.go:196-241), copied, on rangeIndexVersions after
// the index is sorted as LoadIndexFile sorts it (SortEntries).
func upstreamHelmIndexGet(t *testing.T, version string) string {
	t.Helper()
	vs := append(upstreamChartVersions(nil), rangeIndexVersions...)
	sort.Sort(sort.Reverse(vs))
	var constraint *semver.Constraints
	if version == "" {
		constraint, _ = semver.NewConstraint("*")
	} else {
		var err error
		constraint, err = semver.NewConstraint(version)
		if err != nil {
			return "error: " + err.Error()
		}
	}
	if version != "" {
		for _, ver := range vs {
			if version == ver {
				return ver
			}
		}
	}
	for _, ver := range vs {
		test, err := semver.NewVersion(ver)
		if err != nil {
			continue
		}
		if constraint.Check(test) {
			return ver
		}
	}
	return "error: no chart version found"
}

func chartDescription(t *testing.T, chartDir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(chartDir, "Chart.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	_, description, _ := strings.Cut(string(data), "description: ")
	return strings.TrimSpace(description)
}

// TestDefaultAcquirerResolvesTargetRevisionRanges pins how a chart source
// targetRevision on an HTTP repository resolves in the Argo CD v3.6.1
// repo-server (newHelmClientResolveRevision,
// reposerver/repository/repository.go:3029-3066): a constraint resolves to
// the highest index version that matches it (versions.MaxVersion, called
// here as the oracle), with prereleases only for a prerelease constraint,
// and helm pull then fetches that version. A revision that parses as
// semver, such as "1.2", stays as written, and helm pull takes the highest
// version that matches it (IndexFile.Get, also the oracle). The cache entry
// and the result carry the version that helm pull takes, so a partial
// version picks up a new release on the next online run.
func TestDefaultAcquirerResolvesTargetRevisionRanges(t *testing.T) {
	for _, tt := range []struct {
		revision string
		// fetched is the archive that helm pull fetches.
		fetched string
	}{
		{revision: "1.2.*", fetched: "1.2.3"},
		{revision: ">=1.0.0 <2.0.0", fetched: "v1.4.0"},
		{revision: ">=1.0.0, <1.3.0", fetched: "1.2.3"},
		{revision: "*", fetched: "2.0.0"},
		{revision: "~1.3.0-0", fetched: "1.3.0-rc.1"},
		{revision: "^1.4", fetched: "v1.4.0"},
		{revision: "1.2", fetched: "1.2.3"},
		{revision: "1.2.0", fetched: "1.2.0"},
	} {
		t.Run(tt.revision, func(t *testing.T) {
			server, _ := newRangeIndexServer(t)
			request := Request{Repository: server.URL, Name: "demo", Version: tt.revision, Kind: RepositoryHTTP, VersionPolicy: VersionArgoTargetRevision}
			result, err := (DefaultAcquirer{Client: server.Client()}).Acquire(context.Background(), request, Options{CacheDir: t.TempDir()})
			if err != nil {
				t.Fatalf("Acquire() error = %v", err)
			}
			// The repo-server passes a semver revision to helm pull as
			// written, and resolves a constraint with MaxVersion. drydock
			// caches the version that helm pull then takes.
			want := tt.revision
			if !versions.IsVersion(tt.revision) {
				var err error
				if want, err = versions.MaxVersion(tt.revision, rangeIndexVersions, ""); err != nil {
					t.Fatalf("upstream MaxVersion() error = %v", err)
				}
			}
			want = upstreamHelmIndexGet(t, want)
			if result.Version != want {
				t.Fatalf("result version = %q, want helm pull's %q", result.Version, want)
			}
			if got := chartDescription(t, result.ChartDir); got != want || got != tt.fetched {
				t.Fatalf("fetched archive = %q, want helm pull's %q (%q)", got, want, tt.fetched)
			}
			resolved := request
			resolved.Version = want
			if result.CacheKey != mustCacheKey(t, resolved) {
				t.Fatalf("cache key = %q, want the key of %q", result.CacheKey, want)
			}
		})
	}
}

// TestDefaultAcquirerTargetRevisionRangeErrors pins the repo-server's errors
// for a constraint that matches nothing and for a chart that the index
// does not list.
func TestDefaultAcquirerTargetRevisionRangeErrors(t *testing.T) {
	server, _ := newRangeIndexServer(t)
	_, wantErr := versions.MaxVersion(">=3.0.0", rangeIndexVersions, "")
	for _, tt := range []struct {
		name, chart, revision, wantErr string
	}{
		{name: "no match", chart: "demo", revision: ">=3.0.0", wantErr: "invalid revision: " + wantErr.Error()},
		{name: "chart not in index", chart: "other", revision: "1.*", wantErr: "chart 'other' not found in index"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			request := Request{Repository: server.URL, Name: tt.chart, Version: tt.revision, Kind: RepositoryHTTP, VersionPolicy: VersionArgoTargetRevision}
			_, err := (DefaultAcquirer{Client: server.Client()}).Acquire(context.Background(), request, Options{CacheDir: t.TempDir()})
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("Acquire() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

// TestDefaultAcquirerResolvesRangesOffline pins that an online resolve
// records the version list, so the same range resolves under --offline
// from that record and the cached chart, without the network. Without a
// record, an offline range fails as a cache miss.
func TestDefaultAcquirerResolvesRangesOffline(t *testing.T) {
	server, requests := newRangeIndexServer(t)
	cacheDir := t.TempDir()
	acquirer := DefaultAcquirer{Client: server.Client()}
	request := Request{Repository: server.URL, Name: "demo", Version: "1.2.*", Kind: RepositoryHTTP, VersionPolicy: VersionArgoTargetRevision}
	if _, err := acquirer.Acquire(context.Background(), request, Options{CacheDir: cacheDir}); err != nil {
		t.Fatalf("online Acquire() error = %v", err)
	}
	if requests["/index.yaml"] != 1 {
		t.Fatalf("index requests = %d, want one: the archive fetch reuses the index", requests["/index.yaml"])
	}
	server.Close()
	result, err := acquirer.Acquire(context.Background(), request, Options{CacheDir: cacheDir, Offline: true})
	if err != nil {
		t.Fatalf("offline Acquire() error = %v", err)
	}
	if result.Version != "1.2.3" || !result.FromCache {
		t.Fatalf("offline result = %#v, want cached 1.2.3", result)
	}

	_, err = acquirer.Acquire(context.Background(), request, Options{CacheDir: t.TempDir(), Offline: true})
	if err == nil || !strings.Contains(err.Error(), "offline cache miss for chart demo 1.2.*") {
		t.Fatalf("offline Acquire() without a record error = %v, want an offline cache miss", err)
	}
}

// TestDefaultAcquirerResolvesHelmDependencyRanges pins how `helm dependency
// build` without a Chart.lock resolves a Chart.yaml version
// (resolver.Resolve, helm v4.3.1 internal/resolver/resolver.go:56-205): the
// highest index version with an archive URL that matches the constraint.
func TestDefaultAcquirerResolvesHelmDependencyRanges(t *testing.T) {
	for _, tt := range []struct {
		version, want, wantErr string
	}{
		{version: "~1.2.0", want: "1.2.3"},
		{version: ">=1.0.0 <1.2.0", want: "1.0.0"},
		{version: "1.1", wantErr: `can't get a valid version for 1 subchart(s): "demo" (repository`},
		{version: "1.2.3", want: "1.2.3"},
		{version: "^1.4.0", want: "v1.4.0"},
		{version: "not a range", wantErr: `dependency "demo" has an invalid version/constraint format`},
	} {
		t.Run(tt.version, func(t *testing.T) {
			server, _ := newRangeIndexServer(t)
			request := Request{Repository: server.URL, Name: "demo", Version: tt.version, Kind: RepositoryHTTP, VersionPolicy: VersionHelmDependency}
			result, err := (DefaultAcquirer{Client: server.Client()}).Acquire(context.Background(), request, Options{CacheDir: t.TempDir()})
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Acquire() error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Acquire() error = %v", err)
			}
			if result.Version != tt.want || chartDescription(t, result.ChartDir) != tt.want {
				t.Fatalf("result = %#v, want %s", result, tt.want)
			}
		})
	}
}

type fakeOCITagPuller struct {
	tags     []string
	pulled   []string
	tagCalls int
	archive  []byte
}

func (puller *fakeOCITagPuller) Pull(_ context.Context, request Request, _ Options) ([]byte, error) {
	puller.pulled = append(puller.pulled, request.Version)
	return puller.archive, nil
}

func (puller *fakeOCITagPuller) Tags(context.Context, Request, Options) ([]string, error) {
	puller.tagCalls++
	return puller.tags, nil
}

// TestDefaultAcquirerResolvesOCITargetRevisionRanges pins a range on a
// schemeless Helm OCI repository: the repo-server lists the registry tags
// and pulls the highest one that matches (versions.MaxVersion).
func TestDefaultAcquirerResolvesOCITargetRevisionRanges(t *testing.T) {
	puller := &fakeOCITagPuller{
		tags:    []string{"2.0.0", "1.2.3", "1.2.0"},
		archive: chartArchive(t, "demo", map[string]string{"Chart.yaml": "apiVersion: v2\nname: demo\nversion: 1.2.3\n"}),
	}
	request := Request{Repository: "registry.example.test/charts", Name: "demo", Version: "1.2.*", Kind: RepositoryOCI, VersionPolicy: VersionArgoTargetRevision}
	result, err := (DefaultAcquirer{OCIPuller: puller}).Acquire(context.Background(), request, Options{CacheDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if result.Version != "1.2.3" || len(puller.pulled) != 1 || puller.pulled[0] != "1.2.3" {
		t.Fatalf("result = %#v, pulled = %v, want 1.2.3", result, puller.pulled)
	}
}

// TestDefaultAcquirerUsesExplicitOCIDependencyVersions pins that a
// Chart.yaml dependency on an OCI repository without a Chart.lock uses a
// version that parses as semver as an explicit tag, as resolver.Resolve
// does (helm v4.3.1 internal/resolver/resolver.go:135-147): "v1.2.3" and
// "1.2" are pulled as written, without a tag list. A range lists the tags.
func TestDefaultAcquirerUsesExplicitOCIDependencyVersions(t *testing.T) {
	for _, tt := range []struct {
		version, pulled string
		tagCalls        int
	}{
		{version: "v1.2.3", pulled: "v1.2.3"},
		{version: "1.2", pulled: "1.2"},
		{version: "~1.2.0", pulled: "1.2.3", tagCalls: 1},
	} {
		t.Run(tt.version, func(t *testing.T) {
			puller := &fakeOCITagPuller{
				tags:    []string{"2.0.0", "1.2.3"},
				archive: chartArchive(t, "demo", map[string]string{"Chart.yaml": "apiVersion: v2\nname: demo\nversion: 1.2.3\n"}),
			}
			request := Request{Repository: "registry.example.test/charts", Name: "demo", Version: tt.version, Kind: RepositoryOCI, VersionPolicy: VersionHelmDependency}
			result, err := (DefaultAcquirer{OCIPuller: puller}).Acquire(context.Background(), request, Options{CacheDir: t.TempDir()})
			if err != nil {
				t.Fatalf("Acquire() error = %v", err)
			}
			if result.Version != tt.pulled || len(puller.pulled) != 1 || puller.pulled[0] != tt.pulled || puller.tagCalls != tt.tagCalls {
				t.Fatalf("result = %#v, pulled = %v, tag calls = %d, want %s with %d tag calls", result, puller.pulled, puller.tagCalls, tt.pulled, tt.tagCalls)
			}
		})
	}
}

// TestDefaultAcquirerResolvesPartialVersionsAgain pins that a partial
// targetRevision such as "1.2" on an HTTP repository is not a fixed pin:
// the cache entry is the version that helm pull takes, so the next online
// run picks up a new 1.2.x. A partial version in a hand-edited Chart.lock
// takes the version that equals it, 1.2.0, as helm dependency build does. A
// Kustomize helmCharts "1.2" (VersionAsWritten) never reads those entries.
func TestDefaultAcquirerResolvesPartialVersionsAgain(t *testing.T) {
	server, requests := newRangeIndexServer(t)
	cacheDir := t.TempDir()
	acquirer := DefaultAcquirer{Client: server.Client()}
	request := Request{Repository: server.URL, Name: "demo", Version: "1.2", Kind: RepositoryHTTP, VersionPolicy: VersionArgoTargetRevision}
	for range 2 {
		result, err := acquirer.Acquire(context.Background(), request, Options{CacheDir: cacheDir})
		if err != nil {
			t.Fatalf("Acquire() error = %v", err)
		}
		if result.Version != "1.2.3" {
			t.Fatalf("result version = %q, want 1.2.3", result.Version)
		}
	}
	if requests["/index.yaml"] != 2 {
		t.Fatalf("index requests = %d, want one per online run", requests["/index.yaml"])
	}
	locked := request
	locked.VersionPolicy = VersionHelmLocked
	// helm dependency build takes the entry whose version equals 1.2 as
	// semver (findVersionedEntry), which is 1.2.0.
	if result, err := acquirer.Acquire(context.Background(), locked, Options{CacheDir: cacheDir}); err != nil || result.Version != "1.2.0" || chartDescription(t, result.ChartDir) != "1.2.0" {
		t.Fatalf("locked Acquire() = %#v, %v, want a hand-edited lock version 1.2 resolved to 1.2.0", result, err)
	}
	kustomize := request
	kustomize.VersionPolicy = VersionAsWritten
	if _, err := acquirer.Acquire(context.Background(), kustomize, Options{CacheDir: cacheDir, Offline: true}); err == nil || !strings.Contains(err.Error(), "offline cache miss for chart demo 1.2") {
		t.Fatalf("Kustomize Acquire() error = %v, want an offline cache miss", err)
	}
}

// TestDefaultAcquirerMarksOldVersionListsStale pins that an offline resolve
// from a version list older than the chart cache max age still resolves,
// and reports the version list stale with its fetch time, as an expired
// chart cache entry is reported. The fresh chart entry itself is not
// stale. A version list of unknown age is stale under a max age too.
// Without a max age nothing expires. Every offline resolve records its use
// in the list, so an age prune keeps a list that offline runs still use.
func TestDefaultAcquirerMarksOldVersionListsStale(t *testing.T) {
	server, _ := newRangeIndexServer(t)
	cacheDir := t.TempDir()
	acquirer := DefaultAcquirer{Client: server.Client()}
	request := Request{Repository: server.URL, Name: "demo", Version: "1.2.*", Kind: RepositoryHTTP, VersionPolicy: VersionArgoTargetRevision}
	if _, err := acquirer.Acquire(context.Background(), request, Options{CacheDir: cacheDir}); err != nil {
		t.Fatalf("online Acquire() error = %v", err)
	}
	record, ok := readVersionsRecord(cacheDir, request)
	if !ok || record.FetchedAt.IsZero() {
		t.Fatalf("version record = %#v, %v, want one with a fetch time", record, ok)
	}
	for _, tt := range []struct {
		name      string
		fetchedAt time.Time
		maxAge    time.Duration
		stale     bool
	}{
		{name: "fresh", fetchedAt: time.Now().UTC(), maxAge: time.Hour},
		{name: "old", fetchedAt: time.Now().Add(-2 * time.Hour).UTC(), maxAge: time.Hour, stale: true},
		{name: "unknown age", maxAge: time.Hour, stale: true},
		{name: "no max age", fetchedAt: time.Now().Add(-2 * time.Hour).UTC()},
	} {
		t.Run(tt.name, func(t *testing.T) {
			record.FetchedAt = tt.fetchedAt
			writeVersionsRecord(cacheDir, request, record, nil)
			result, err := acquirer.Acquire(context.Background(), request, Options{CacheDir: cacheDir, Offline: true, MaxAge: tt.maxAge})
			if err != nil {
				t.Fatalf("offline Acquire() error = %v", err)
			}
			if result.Version != "1.2.3" || result.Stale || result.VersionListStale != tt.stale {
				t.Fatalf("result = %#v, want 1.2.3 with a stale version list %v", result, tt.stale)
			}
			if tt.stale && !result.VersionListFetchedAt.Equal(tt.fetchedAt) {
				t.Fatalf("version list fetched at = %v, want %v", result.VersionListFetchedAt, tt.fetchedAt)
			}
			used, ok := readVersionsRecord(cacheDir, request)
			if !ok || time.Since(used.UsedAt) > time.Minute || !used.FetchedAt.Equal(tt.fetchedAt) {
				t.Fatalf("version record = %#v, want a fresh use and the old fetch time", used)
			}
		})
	}
}

// TestVersionListUseKeepsANewerList pins that recording the use of a
// version list never overwrites a newer list that another writer stored at
// the same time: the use re-reads the list under the file lock.
func TestVersionListUseKeepsANewerList(t *testing.T) {
	cacheDir := t.TempDir()
	request := Request{Repository: "https://charts.example.test", Name: "demo", Kind: RepositoryHTTP}
	old := chartVersionsRecord{Found: true, Versions: []recordedChartVersion{{Version: "1.0.0", HasURL: true}}, FetchedAt: time.Unix(100, 0).UTC()}
	newer := chartVersionsRecord{Found: true, Versions: []recordedChartVersion{{Version: "2.0.0", HasURL: true}}, FetchedAt: time.Unix(200, 0).UTC()}
	for range 20 {
		writeVersionsRecord(cacheDir, request, old, nil)
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				touchVersionsRecord(cacheDir, request, nil)
			})
		}
		writeVersionsRecord(cacheDir, request, newer, nil)
		wg.Wait()
		got, ok := readVersionsRecord(cacheDir, request)
		if !ok || !got.FetchedAt.Equal(newer.FetchedAt) || len(got.Versions) != 1 || got.Versions[0].Version != "2.0.0" {
			t.Fatalf("version list = %#v, want the newer list kept", got)
		}
	}
}

// TestOfflineRangeOnAReadOnlyCache pins that an offline range resolves from
// a read-only chart cache: the use of the version list cannot be recorded,
// and that does not fail the render.
func TestOfflineRangeOnAReadOnlyCache(t *testing.T) {
	server, _ := newRangeIndexServer(t)
	cacheDir := t.TempDir()
	acquirer := DefaultAcquirer{Client: server.Client()}
	request := Request{Repository: server.URL, Name: "demo", Version: "1.2.*", Kind: RepositoryHTTP, VersionPolicy: VersionArgoTargetRevision}
	if _, err := acquirer.Acquire(context.Background(), request, Options{CacheDir: cacheDir}); err != nil {
		t.Fatal(err)
	}
	var dirs []string
	_ = filepath.Walk(cacheDir, func(path string, info os.FileInfo, err error) error {
		if err == nil && info.IsDir() {
			dirs = append(dirs, path)
		}
		return nil
	})
	for _, dir := range dirs {
		if err := os.Chmod(dir, 0o555); err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		for _, dir := range dirs {
			_ = os.Chmod(dir, 0o755)
		}
	}()
	result, err := acquirer.Acquire(context.Background(), request, Options{CacheDir: cacheDir, Offline: true})
	if err != nil || result.Version != "1.2.3" {
		t.Fatalf("offline Acquire() = %#v, %v, want 1.2.3", result, err)
	}
}

// TestDefaultAcquirerFetchesLockedVersionsLikeHelm pins how helm dependency
// build finds the archive of a locked version (findVersionedEntry and
// versionEquals, helm v4.3.1 pkg/downloader/manager.go:781-806): the first
// entry with an archive URL whose version equals it as semver, so "v1.2.0"
// takes 1.2.0, and an entry without a URL is skipped, unlike helm pull.
func TestDefaultAcquirerFetchesLockedVersionsLikeHelm(t *testing.T) {
	for _, tt := range []struct {
		version, want, wantErr string
	}{
		{version: "1.2.3", want: "1.2.3"},
		{version: "v1.2.0", want: "1.2.0"},
		{version: "1.4.0", want: "v1.4.0"},
		{version: "1.1.0", wantErr: "chart demo version 1.1.0 not found in repository index"},
		{version: "not-semver", want: "not-semver"},
	} {
		t.Run(tt.version, func(t *testing.T) {
			server, _ := newRangeIndexServer(t)
			request := Request{Repository: server.URL, Name: "demo", Version: tt.version, Kind: RepositoryHTTP, VersionPolicy: VersionHelmLocked}
			result, err := (DefaultAcquirer{Client: server.Client()}).Acquire(context.Background(), request, Options{CacheDir: t.TempDir()})
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Acquire() error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || chartDescription(t, result.ChartDir) != tt.want {
				t.Fatalf("Acquire() = %#v, %v, want the archive of %s", result, err, tt.want)
			}
		})
	}
}
