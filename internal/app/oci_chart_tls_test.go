package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/sholdee/drydock/internal/chart"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/ociartifact"
	"github.com/sholdee/drydock/internal/ociartifact/ocitest"
)

// The wiring pin for the --oci-* TLS flags on OCI Helm chart pulls: the
// builder must hand the TLS client to OCIPuller ONLY. DefaultAcquirer.Client
// is shared with the HTTP(S) Helm repository path (index.yaml plus the chart
// archive, Helm dependency resolution, Kustomize helmCharts), so setting it
// would apply the OCI CA pool and --oci-insecure-skip-verify to every https://
// Helm repository in the run.
func TestLocalProviderBuildsOCIOnlyTLSAcquirer(t *testing.T) {
	reg := ocitest.StartTLSRegistry(t)
	for _, tt := range []struct {
		name        string
		credentials ociartifact.Credentials
		wantPuller  bool
	}{
		{name: "ca file", credentials: ociartifact.Credentials{CAFile: reg.CAFilePath(t)}, wantPuller: true},
		{name: "insecure skip verify", credentials: ociartifact.Credentials{InsecureSkipVerify: true}, wantPuller: true},
		{name: "no TLS flags", credentials: ociartifact.Credentials{}, wantPuller: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			acquirer := buildRequestChartAcquirer(t, tt.credentials)
			if acquirer.Client != nil {
				t.Fatal("DefaultAcquirer.Client is set: the OCI TLS client leaked onto the shared HTTP(S) Helm client")
			}
			if (acquirer.OCIPuller != nil) != tt.wantPuller {
				t.Fatalf("DefaultAcquirer.OCIPuller = %#v, want non-nil = %v", acquirer.OCIPuller, tt.wantPuller)
			}
		})
	}
}

// Drives the builder-constructed acquirer at an HTTP(S) Helm repository: it
// must still fail verification. This is the assertion that goes red the moment
// anyone sets the shared Client field — the fetchOCIChart fallback would
// otherwise make the leaking form indistinguishable in a render-level test.
//
// The index entry uses a RELATIVE archive URL so the leaking wiring stays
// inside the fixture server: under the correct wiring the index GET fails at
// x509 and the archive is never fetched, and under the leaking wiring the
// failure is the fixture's non-archive body rather than a DNS lookup of a
// bogus hostname, which would make the regression depend on the host resolver
// and report a misleading cause.
func TestBuilderOCITLSAcquirerStillRejectsUntrustedHTTPHelmRepository(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".tgz") {
			w.Header().Set("Content-Type", "application/gzip")
			_, _ = w.Write([]byte("the shared client reached the repository"))
			return
		}
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write([]byte("apiVersion: v1\nentries:\n  demo:\n  - version: 1.0.0\n    urls:\n    - demo-1.0.0.tgz\n"))
	}))
	defer server.Close()
	acquirer := buildRequestChartAcquirer(t, ociartifact.Credentials{InsecureSkipVerify: true})

	_, err := acquirer.Acquire(t.Context(), chart.Request{
		Repository: server.URL,
		Name:       "demo",
		Version:    "1.0.0",
		Kind:       chart.RepositoryHTTP,
	}, chart.Options{CacheDir: t.TempDir()})
	if err == nil {
		t.Fatal("Acquire() error = nil: --oci-insecure-skip-verify reached an HTTP(S) Helm repository")
	}
	if !strings.Contains(err.Error(), "x509") {
		t.Fatalf("Acquire() error = %v, want x509 verification failure: the shared DefaultAcquirer.Client carried --oci-insecure-skip-verify to the HTTP(S) Helm repository", err)
	}
}

// Render-level check: a chart-only Application with a scheme-less repoURL and
// a nested chart name renders from a self-signed registry with the CA flags
// and fails without them.
func TestBuildRendersNestedOCIChartOverTLSWithOCICredentials(t *testing.T) {
	reg := ocitest.StartTLSRegistry(t)
	ocitest.PushHelmChartArtifact(t, reg, "parity/nested/demo", "1.0.0", ocitest.HelmChartSpec{Name: "demo", Version: "1.0.0"})
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "nested-chart.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: nested-chart
  namespace: argocd
spec:
  source:
    repoURL: `+reg.Host+`
    chart: parity/nested/demo
    targetRevision: "1.0.0"
  destination:
    name: in-cluster
    namespace: default
`)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{
		Path:           root,
		ChartCacheDir:  t.TempDir(),
		OCICredentials: ociartifact.Credentials{CAFile: reg.CAFilePath(t)},
	})
	if err != nil {
		t.Fatalf("Build() with --oci-ca-file error = %v", err)
	}
	manifest := assertManifestNamed(t, result.Manifests, "nested-chart-demo")
	if marker, _, _ := unstructured.NestedString(manifest.Object.Object, "data", "marker"); marker != "oci-artifact-content" {
		t.Fatalf("marker = %q, want the pushed chart's template output", marker)
	}

	_, err = Orchestrator{}.Build(context.Background(), BuildRequest{
		Path:          root,
		ChartCacheDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("Build() without --oci-ca-file error = nil, want x509 verification failure")
	}
	if !strings.Contains(err.Error(), "x509") {
		t.Fatalf("Build() without --oci-ca-file error = %v, want x509 verification failure", err)
	}
}

func buildRequestChartAcquirer(t *testing.T, credentials ociartifact.Credentials) chart.DefaultAcquirer {
	t.Helper()
	request := BuildRequest{Path: t.TempDir(), OCICredentials: credentials}
	provider, cleanup, err := newLocalProvider(context.Background(), Orchestrator{}, request.Path, config.ArgoSettings{}, request, nil, "drydock-oci-chart-tls-test-*")
	if err != nil {
		t.Fatalf("newLocalProvider() error = %v", err)
	}
	t.Cleanup(cleanup)
	acquirer, ok := provider.chartAcquirer.(chart.DefaultAcquirer)
	if !ok {
		t.Fatalf("chartAcquirer = %T, want chart.DefaultAcquirer", provider.chartAcquirer)
	}
	return acquirer
}
