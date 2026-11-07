package chart

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/sholdee/drydock/internal/filelock"
)

// TestLoadHelmIndexSortsLikeHelm pins that drydock orders the entries of a
// loaded index exactly as helm does: the same unstable
// sort.Sort(sort.Reverse(...)) on the same input order, after helm drops the
// entries it cannot load. Among semver-equal versions such as 1.2.0,
// v1.2.0, 1.2.0+a, and 1.2.0+b, every lookup then picks the entry that helm
// picks, for many index sizes and positions.
func TestLoadHelmIndexSortsLikeHelm(t *testing.T) {
	for n := 5; n < 300; n += 7 {
		versions := helmOrderVersions(n)
		var index strings.Builder
		index.WriteString("apiVersion: v1\nentries:\n  demo:\n")
		for _, version := range versions {
			fmt.Fprintf(&index, "    - name: demo\n      version: %q\n      urls: [demo-%s.tgz]\n", version, version)
		}
		loaded, err := loadHelmIndex([]byte(index.String()))
		if err != nil {
			t.Fatal(err)
		}
		// helm's own order: drop what does not load, then the same sort.
		var valid upstreamChartVersions
		for _, version := range versions {
			if _, err := semver.NewVersion(version); err == nil {
				valid = append(valid, version)
			}
		}
		sort.Sort(sort.Reverse(valid))
		got := make([]string, 0, len(loaded["demo"]))
		for _, entry := range loaded["demo"] {
			got = append(got, entry.Version)
		}
		if strings.Join(got, ",") != strings.Join(valid, ",") {
			t.Fatalf("n=%d: order = %v, want helm's %v", n, got, valid)
		}
		assertHelmLookupsPick(t, n, loaded["demo"], firstHelmEqual(valid, "1.2.0"))
	}
}

// helmOrderVersions returns n versions 1.0.0, 1.1.0, ..., with the
// semver-equal variants of 1.2.0 and a version that does not parse put in
// at one third.
func helmOrderVersions(n int) []string {
	versions := make([]string, 0, n+4)
	for i := range n {
		if i == n/3 {
			versions = append(versions, "1.2.0+a", "v1.2.0", "1.2.0+b", "not-semver")
		}
		versions = append(versions, fmt.Sprintf("1.%d.0", i))
	}
	return versions
}

// firstHelmEqual returns the first version of sorted that equals version
// as helm's versionEquals reads it.
func firstHelmEqual(sorted []string, version string) string {
	for _, candidate := range sorted {
		if helmVersionEquals(version, candidate) {
			return candidate
		}
	}
	return ""
}

// assertHelmLookupsPick requires every lookup that reads a loaded index
// (locked, helm pull, dependency range) to pick want among the
// semver-equal 1.2.0 entries.
func assertHelmLookupsPick(t *testing.T, n int, entries []repositoryChartVersion, want string) {
	t.Helper()
	locked, err := helmFindVersionedEntry(entries, "demo", "v1.2")
	if err != nil || locked.Version != want {
		t.Fatalf("n=%d: locked entry = %q, %v, want helm's %q", n, locked.Version, err, want)
	}
	pulled, err := helmIndexGet(entries, "demo", "~1.2.0")
	if err != nil || pulled.Version != want {
		t.Fatalf("n=%d: helm pull entry = %q, %v, want helm's %q", n, pulled.Version, err, want)
	}
	dependency, err := resolveHelmDependencyVersion(Request{Name: "demo", Version: "~1.2.0", Kind: RepositoryHTTP}, recordFromEntries(entries))
	if err != nil || dependency != want {
		t.Fatalf("n=%d: dependency version = %q, %v, want helm's %q", n, dependency, err, want)
	}
}

func recordFromEntries(entries []repositoryChartVersion) chartVersionsRecord {
	record := chartVersionsRecord{Found: true}
	for _, entry := range entries {
		record.Versions = append(record.Versions, recordedChartVersion{Version: entry.Version, HasURL: len(entry.URLs) > 0})
	}
	return record
}

// TestLoadHelmIndexDropsInvalidEntries pins that loading an index drops the
// entries that helm drops (Metadata.Validate): a version that is not semver,
// a missing name, and a bad chart type. An index without an apiVersion
// fails as in helm.
func TestLoadHelmIndexDropsInvalidEntries(t *testing.T) {
	loaded, err := loadHelmIndex([]byte(`apiVersion: v1
entries:
  demo:
    - {name: demo, version: not-semver, urls: [a.tgz]}
    - {version: 1.0.0, urls: [b.tgz]}
    - {name: demo, version: 1.1.0, type: plugin, urls: [c.tgz]}
    - {name: demo, version: 1.2.0, urls: [d.tgz]}
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded["demo"]; len(got) != 1 || got[0].Version != "1.2.0" {
		t.Fatalf("entries = %#v, want only 1.2.0", got)
	}
	if _, err := loadHelmIndex([]byte("entries: {}\n")); err == nil || err.Error() != "no API version specified" {
		t.Fatalf("loadHelmIndex() error = %v, want helm's no API version error", err)
	}
}

// TestVersionListUseIsSkippedWhileLocked pins that recording a version list
// is best effort: while another writer holds the lock past the timeout, the
// write is skipped instead of failing or waiting forever.
func TestVersionListUseIsSkippedWhileLocked(t *testing.T) {
	previous := versionsRecordLockTimeout
	versionsRecordLockTimeout = 100 * time.Millisecond
	defer func() { versionsRecordLockTimeout = previous }()
	cacheDir := t.TempDir()
	request := Request{Repository: "https://charts.example.test", Name: "demo", Kind: RepositoryHTTP}
	path, err := versionsRecordPath(cacheDir, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	held, err := filelock.Acquire(context.Background(), filepath.Join(filepath.Dir(path), ".lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	writeVersionsRecord(cacheDir, request, chartVersionsRecord{Found: true}, nil)
	if _, ok := readVersionsRecord(cacheDir, request); ok {
		t.Fatal("version list written while another writer held the lock")
	}
}

// TestLoadHelmIndexDecodesYAMLStrictly pins that a YAML index decodes as
// helm decodes it (jsonOrYamlUnmarshal, helm v4.3.1
// pkg/repo/v1/index.go:410-415): a field that helm does not know fails a
// YAML index, while the fields of helm's types, also the deprecated ones,
// load. A JSON index decodes leniently, as in helm.
func TestLoadHelmIndexDecodesYAMLStrictly(t *testing.T) {
	known := `apiVersion: v1
generated: "2026-01-01T00:00:00Z"
serverInfo: {contextPath: /charts}
annotations: {team: a}
entries:
  demo:
    - name: demo
      version: 1.2.0
      urls: [demo-1.2.0.tgz]
      created: "2026-01-01T00:00:00Z"
      digest: abc
      checksum: def
      engine: gotpl
      tillerVersion: ">=2.0.0"
      url: demo-1.2.0.tgz
`
	if loaded, err := loadHelmIndex([]byte(known)); err != nil || len(loaded["demo"]) != 1 {
		t.Fatalf("loadHelmIndex(known fields) = %v, %v, want one entry", loaded, err)
	}
	unknown := strings.Replace(known, "      digest: abc\n", "      digest: abc\n      mirror: yes\n", 1)
	if _, err := loadHelmIndex([]byte(unknown)); err == nil || !strings.Contains(err.Error(), `unknown field "mirror"`) {
		t.Fatalf("loadHelmIndex(unknown field) error = %v, want helm's strict decode error", err)
	}
	jsonIndex := `{"apiVersion":"v1","entries":{"demo":[{"name":"demo","version":"1.2.0","urls":["a.tgz"],"mirror":"yes"}]}}`
	if loaded, err := loadHelmIndex([]byte(jsonIndex)); err != nil || len(loaded["demo"]) != 1 {
		t.Fatalf("loadHelmIndex(JSON) = %v, %v, want one entry", loaded, err)
	}
}
