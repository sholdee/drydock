package render

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/chart"
	chartv2 "helm.sh/helm/v4/pkg/chart/v2"
	chartv2loader "helm.sh/helm/v4/pkg/chart/v2/loader"
	"helm.sh/helm/v4/pkg/provenance"
	"sigs.k8s.io/yaml"
)

// upstreamHelmHashReq is resolver.HashReq (helm v4.3.1
// internal/resolver/resolver.go:213-220), copied because helm keeps it
// internal. `helm dependency update` writes it as the Chart.lock digest.
func upstreamHelmHashReq(req, lock []*chartv2.Dependency) (string, error) {
	data, err := json.Marshal([2][]*chartv2.Dependency{req, lock})
	if err != nil {
		return "", err
	}
	s, err := provenance.Digest(bytes.NewBuffer(data))
	return "sha256:" + s, err
}

// writeHelmChartLock writes chartDir/Chart.lock with the dependencies of the
// YAML list lockDependencies and the digest that `helm dependency update`
// writes for them and chartDir/Chart.yaml.
func writeHelmChartLock(t *testing.T, chartDir, lockDependencies string) {
	t.Helper()
	parent, err := chartv2loader.LoadDir(chartDir)
	if err != nil {
		t.Fatalf("load chart %s: %v", chartDir, err)
	}
	var locked []*chartv2.Dependency
	if err := yaml.Unmarshal([]byte(lockDependencies), &locked); err != nil {
		t.Fatalf("decode lock dependencies: %v", err)
	}
	digest, err := upstreamHelmHashReq(parent.Metadata.Dependencies, locked)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(chartDir, "Chart.lock"), "dependencies:"+lockDependencies+"digest: "+digest+"\ngenerated: \"2026-01-01T00:00:00Z\"\n")
}

// namedChartAcquirer serves each dependency from its own chart directory,
// by chart name.
type namedChartAcquirer struct {
	chartDirs map[string]string
	requests  []chart.Request
}

func (acquirer *namedChartAcquirer) Acquire(_ context.Context, request chart.Request, _ chart.Options) (chart.Result, error) {
	acquirer.requests = append(acquirer.requests, request)
	return chart.Result{
		ChartDir:   acquirer.chartDirs[request.Name],
		Repository: request.Repository,
		Name:       request.Name,
		Version:    request.Version,
		Kind:       request.Kind,
	}, nil
}

func configMapTemplate(name string) string {
	return "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: " + name + "\n"
}

// writeHelmDependencyParent writes a parent chart at root/parent with the
// Chart.yaml dependencies of the YAML list dependencies, and the charts
// that the fake acquirer serves under root/acquired.
func writeHelmDependencyParent(t *testing.T, root, dependencies string) string {
	t.Helper()
	parent := filepath.Join(root, "parent")
	writeFile(t, filepath.Join(parent, "Chart.yaml"), "apiVersion: v2\nname: parent\nversion: 0.1.0\ndependencies:"+dependencies)
	writeFile(t, filepath.Join(parent, "templates", "parent.yaml"), configMapTemplate("parent-config"))
	writeNamedTestChart(t, filepath.Join(root, "acquired", "postgres"), "postgres", "0.12.4", configMapTemplate("fetched-postgres"))
	writeNamedTestChart(t, filepath.Join(root, "acquired", "redis"), "redis", "1.0.0", configMapTemplate("fetched-redis"))
	return parent
}

func renderHelmDependencyParent(t *testing.T, root string, acquirer chart.Acquirer) ([]Manifest, error) {
	t.Helper()
	result, _, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "parent",
		Chart:    "parent",
	}, RenderOptions{
		AppName:       "demo",
		ChartAcquirer: acquirer,
	})
	return result, err
}

// TestHelmDependencyBuildRefetchesVendoredCharts pins `helm dependency
// build` with a Chart.lock when one dependency is missing (Manager.Build and
// downloadAll, helm v4.3.1 pkg/downloader/manager.go:89-155 and 239-345):
// it fetches every locked dependency at its locked version, also the one
// that is vendored as an archive of another version, and removes the
// archives that it did not fetch, also one that Chart.yaml does not name.
// A dependency without a repository stays.
func TestHelmDependencyBuildRefetchesVendoredCharts(t *testing.T) {
	root := t.TempDir()
	parent := writeHelmDependencyParent(t, root, `
  - name: postgres
    version: '>=0.12.0'
    repository: oci://registry.example.test/charts
  - name: redis
    version: 1.0.0
    repository: https://charts.example.test
  - name: local
    version: 0.1.0
`)
	writeHelmChartLock(t, parent, `
  - name: postgres
    repository: oci://registry.example.test/charts
    version: 0.12.4
  - name: redis
    repository: https://charts.example.test
    version: 1.0.0
  - name: local
    repository: ""
    version: 0.1.0
`)
	writeArchivedTestChart(t, filepath.Join(parent, "charts"), "postgres", "0.12.5", configMapTemplate("vendored-postgres"))
	writeArchivedTestChart(t, filepath.Join(parent, "charts"), "orphan", "1.0.0", configMapTemplate("vendored-orphan"))
	writeNamedTestChart(t, filepath.Join(parent, "charts", "local"), "local", "0.1.0", configMapTemplate("vendored-local"))
	acquirer := &namedChartAcquirer{chartDirs: map[string]string{
		"postgres": filepath.Join(root, "acquired", "postgres"),
		"redis":    filepath.Join(root, "acquired", "redis"),
	}}

	result, err := renderHelmDependencyParent(t, root, acquirer)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(acquirer.requests) != 2 {
		t.Fatalf("chart requests = %#v, want postgres and redis", acquirer.requests)
	}
	for _, request := range acquirer.requests {
		if request.VersionPolicy != chart.VersionHelmLocked {
			t.Fatalf("chart request = %#v, want the locked version policy", request)
		}
	}
	for _, name := range []string{"fetched-postgres", "fetched-redis", "vendored-local", "parent-config"} {
		if !containsManifest(result, "ConfigMap", name) {
			t.Fatalf("manifests = %#v, want %s", manifestNames(result), name)
		}
	}
	for _, name := range []string{"vendored-postgres", "vendored-orphan"} {
		if containsManifest(result, "ConfigMap", name) {
			t.Fatalf("manifests = %#v, want no %s", manifestNames(result), name)
		}
	}
	if _, err := os.Stat(filepath.Join(parent, "charts", "postgres-0.12.5.tgz")); err != nil {
		t.Fatalf("the source checkout changed: %v", err)
	}
}

// TestHelmDependencyBuildWithoutLockResolvesRanges pins `helm dependency
// build` without a Chart.lock (Manager.Update): each Chart.yaml version is
// resolved against its repository, so the request carries the version as
// written under the dependency policy, and an http:// repository works.
func TestHelmDependencyBuildWithoutLockResolvesRanges(t *testing.T) {
	root := t.TempDir()
	writeHelmDependencyParent(t, root, `
  - name: redis
    version: ~1.0.0
    repository: http://charts.example.test
`)
	acquirer := &namedChartAcquirer{chartDirs: map[string]string{"redis": filepath.Join(root, "acquired", "redis")}}
	result, err := renderHelmDependencyParent(t, root, acquirer)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(acquirer.requests) != 1 || acquirer.requests[0].Version != "~1.0.0" || acquirer.requests[0].VersionPolicy != chart.VersionHelmDependency {
		t.Fatalf("chart requests = %#v, want redis ~1.0.0 under the dependency policy", acquirer.requests)
	}
	if !containsManifest(result, "ConfigMap", "fetched-redis") {
		t.Fatalf("manifests = %#v, want fetched-redis", manifestNames(result))
	}
}

// TestHelmDependencyBuildFailsLikeHelm pins the `helm dependency build`
// errors that the repo-server reports when a dependency is missing: a
// Chart.lock that does not match Chart.yaml, and a locked http://
// repository, which the repo-server never adds (getHelmDependencyRepos adds
// only https and oci). Nothing is fetched. A vendored chart of another
// version is not missing, so nothing fails then.
func TestHelmDependencyBuildFailsLikeHelm(t *testing.T) {
	for _, tt := range []struct {
		name    string
		chart   string
		lock    string
		staleTo string
		vendor  bool
		wantErr string
	}{
		{
			name: "stale lock",
			chart: `
  - name: redis
    version: ~1.0.0
    repository: https://charts.example.test
`,
			lock: `
  - name: redis
    repository: https://charts.example.test
    version: 1.0.0
`,
			staleTo: "~1.1.0",
			wantErr: "the lock file (Chart.lock) is out of sync with the dependencies file (Chart.yaml). Please update the dependencies with 'helm dependency update'",
		},
		{
			name: "locked http repository",
			chart: `
  - name: redis
    version: ~1.0.0
    repository: http://charts.example.test
`,
			lock: `
  - name: redis
    repository: http://charts.example.test
    version: 1.0.0
`,
			wantErr: "no repository definition for http://charts.example.test. Please add the missing repos via 'helm repo add'",
		},
		{
			name: "vendored chart with a stale lock",
			chart: `
  - name: redis
    version: ~1.0.0
    repository: http://charts.example.test
`,
			lock: `
  - name: redis
    repository: http://charts.example.test
    version: 1.0.0
`,
			staleTo: "~1.1.0",
			vendor:  true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			parent := writeHelmDependencyParent(t, root, tt.chart)
			writeHelmChartLock(t, parent, tt.lock)
			if tt.staleTo != "" {
				data, err := os.ReadFile(filepath.Join(parent, "Chart.yaml"))
				if err != nil {
					t.Fatal(err)
				}
				writeFile(t, filepath.Join(parent, "Chart.yaml"), strings.Replace(string(data), "~1.0.0", tt.staleTo, 1))
			}
			if tt.vendor {
				writeNamedTestChart(t, filepath.Join(parent, "charts", "redis"), "redis", "0.9.0", configMapTemplate("vendored-redis"))
			}
			acquirer := &namedChartAcquirer{chartDirs: map[string]string{"redis": filepath.Join(root, "acquired", "redis")}}
			result, err := renderHelmDependencyParent(t, root, acquirer)
			if len(acquirer.requests) != 0 {
				t.Fatalf("chart requests = %#v, want none", acquirer.requests)
			}
			if tt.wantErr == "" {
				if err != nil || !containsManifest(result, "ConfigMap", "vendored-redis") {
					t.Fatalf("Render() = %v, %v, want vendored-redis", manifestNames(result), err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Render() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

// TestHelmDependencyBuildKeepsArchivesOfOtherCharts pins that fetching the
// dependency redis removes only the archives whose chart is redis, as
// Manager.safeMoveDeps decides by the chart inside the archive. The vendored
// redis-cluster-1.0.0.tgz of a file:// dependency, which drydock keeps,
// stays although its file name starts with redis-. The missing postgres
// starts the build.
func TestHelmDependencyBuildKeepsArchivesOfOtherCharts(t *testing.T) {
	root := t.TempDir()
	parent := writeHelmDependencyParent(t, root, `
  - name: redis
    version: 1.0.0
    repository: https://charts.example.test
  - name: redis-cluster
    version: 1.0.0
    repository: file://../redis-cluster
  - name: postgres
    version: 0.12.4
    repository: oci://registry.example.test/charts
`)
	writeArchivedTestChart(t, filepath.Join(parent, "charts"), "redis-cluster", "1.0.0", configMapTemplate("vendored-redis-cluster"))
	writeArchivedTestChart(t, filepath.Join(parent, "charts"), "redis", "0.9.0", configMapTemplate("vendored-redis"))
	acquirer := &namedChartAcquirer{chartDirs: map[string]string{
		"redis":    filepath.Join(root, "acquired", "redis"),
		"postgres": filepath.Join(root, "acquired", "postgres"),
	}}
	result, err := renderHelmDependencyParent(t, root, acquirer)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(acquirer.requests) != 2 {
		t.Fatalf("chart requests = %#v, want redis and postgres", acquirer.requests)
	}
	for _, name := range []string{"fetched-redis", "fetched-postgres", "vendored-redis-cluster"} {
		if !containsManifest(result, "ConfigMap", name) {
			t.Fatalf("manifests = %#v, want %s", manifestNames(result), name)
		}
	}
	if containsManifest(result, "ConfigMap", "vendored-redis") {
		t.Fatalf("manifests = %#v, want the outdated redis archive removed", manifestNames(result))
	}
}

// TestHelmDependencyBuildChecksDependenciesWithoutRepository pins how
// `helm dependency build` checks a dependency without a repository, with
// helm's text (checked against the helm 4.3.1 binary): without a
// Chart.lock, charts/<name> must exist as a path, so a vendored archive
// does not count, and the vendored directory must match the version
// constraint.
func TestHelmDependencyBuildChecksDependenciesWithoutRepository(t *testing.T) {
	for _, tt := range []struct {
		name    string
		vendor  func(t *testing.T, parent string)
		wantErr string
	}{
		{
			name:    "missing",
			vendor:  func(*testing.T, string) {},
			wantErr: "directory charts/sub not found",
		},
		{
			name: "archive",
			vendor: func(t *testing.T, parent string) {
				writeArchivedTestChart(t, filepath.Join(parent, "charts"), "sub", "2.0.0", configMapTemplate("vendored-sub"))
			},
			wantErr: "directory charts/sub not found",
		},
		{
			name: "other version",
			vendor: func(t *testing.T, parent string) {
				writeNamedTestChart(t, filepath.Join(parent, "charts", "sub"), "sub", "1.0.0", configMapTemplate("vendored-sub"))
			},
			wantErr: "dependency sub at version 1.0.0 does not satisfy the constraint 2.0.0",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			parent := writeHelmDependencyParent(t, root, `
  - name: sub
    version: 2.0.0
  - name: redis
    version: 1.0.0
    repository: https://charts.example.test
`)
			tt.vendor(t, parent)
			acquirer := &namedChartAcquirer{chartDirs: map[string]string{"redis": filepath.Join(root, "acquired", "redis")}}
			_, err := renderHelmDependencyParent(t, root, acquirer)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Render() error = %v, want %q", err, tt.wantErr)
			}
			if len(acquirer.requests) != 0 {
				t.Fatalf("chart requests = %#v, want none", acquirer.requests)
			}
		})
	}
}
