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
	"testing"

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
// and the result carry the resolved version.
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
			want := tt.revision
			if !versions.IsVersion(tt.revision) {
				var err error
				if want, err = versions.MaxVersion(tt.revision, rangeIndexVersions, ""); err != nil {
					t.Fatalf("upstream MaxVersion() error = %v", err)
				}
			}
			if result.Version != want {
				t.Fatalf("result version = %q, want the repo-server's %q", result.Version, want)
			}
			if got, want := chartDescription(t, result.ChartDir), upstreamHelmIndexGet(t, want); got != want || got != tt.fetched {
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
	tags    []string
	pulled  []string
	archive []byte
}

func (puller *fakeOCITagPuller) Pull(_ context.Context, request Request, _ Options) ([]byte, error) {
	puller.pulled = append(puller.pulled, request.Version)
	return puller.archive, nil
}

func (puller *fakeOCITagPuller) Tags(context.Context, Request, Options) ([]string, error) {
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
