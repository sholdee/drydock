package main

import (
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	chartv2loader "helm.sh/helm/v4/pkg/chart/v2/loader"
	chartv2util "helm.sh/helm/v4/pkg/chart/v2/util"
	"helm.sh/helm/v4/pkg/registry"
)

type options struct {
	ChartDir string
	Ref      string
	CAFile   string
}

func main() {
	var opts options
	flag.StringVar(&opts.ChartDir, "chart-dir", "", "chart source directory to package and push")
	flag.StringVar(&opts.Ref, "ref", "", "target OCI repository without scheme, chart name, or tag (host:port/path)")
	flag.StringVar(&opts.CAFile, "ca-file", "", "PEM bundle trusted for the registry TLS certificate (required)")
	flag.Parse()

	digest, err := push(opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "argocd parity chart push: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(digest)
}

// push packages ChartDir and pushes it as <ref>/<chart name>:<chart version>,
// the reference shape `helm push` composes, and returns the manifest digest.
func push(opts options) (string, error) {
	if err := opts.validate(); err != nil {
		return "", err
	}
	client, err := registryClient(opts.CAFile)
	if err != nil {
		return "", err
	}

	loaded, err := chartv2loader.LoadDir(opts.ChartDir)
	if err != nil {
		return "", fmt.Errorf("--chart-dir %q: load chart: %w", opts.ChartDir, err)
	}
	tempDir, err := os.MkdirTemp("", "argocd-parity-chart-push-")
	if err != nil {
		return "", fmt.Errorf("create temporary directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(tempDir) }()

	archivePath, err := chartv2util.Save(loaded, tempDir)
	if err != nil {
		return "", fmt.Errorf("--chart-dir %q: package chart: %w", opts.ChartDir, err)
	}
	data, err := os.ReadFile(archivePath)
	if err != nil {
		return "", fmt.Errorf("read packaged chart %s: %w", filepath.Base(archivePath), err)
	}

	// Anonymous registry: an empty credentials file keeps any ambient Docker
	// or helm registry config out of the push.
	credentialsFile := filepath.Join(tempDir, "registry-config.json")
	if err := os.WriteFile(credentialsFile, []byte("{}\n"), 0o600); err != nil {
		return "", fmt.Errorf("write temporary registry credentials file: %w", err)
	}
	registryClient, err := registry.NewClient(
		registry.ClientOptHTTPClient(client),
		registry.ClientOptWriter(io.Discard),
		registry.ClientOptCredentialsFile(credentialsFile),
	)
	if err != nil {
		return "", fmt.Errorf("create Helm OCI registry client: %w", err)
	}

	ref := fmt.Sprintf("%s/%s:%s", strings.TrimSuffix(opts.Ref, "/"), loaded.Metadata.Name, loaded.Metadata.Version)
	result, err := registryClient.Push(data, ref)
	if err != nil {
		return "", fmt.Errorf("push chart to %s: %w", ref, err)
	}
	if result == nil || result.Manifest == nil {
		return "", fmt.Errorf("push chart to %s: registry returned no manifest descriptor", ref)
	}
	return result.Manifest.Digest, nil
}

func (o options) validate() error {
	if strings.TrimSpace(o.ChartDir) == "" {
		return fmt.Errorf("--chart-dir is required")
	}
	if strings.TrimSpace(o.Ref) == "" {
		return fmt.Errorf("--ref is required")
	}
	if strings.Contains(o.Ref, "://") {
		return fmt.Errorf("--ref %q must not include a scheme", o.Ref)
	}
	if strings.TrimSpace(o.CAFile) == "" {
		return fmt.Errorf("--ca-file is required; the parity registry serves a self-signed certificate")
	}
	return nil
}

// registryClient builds the HTTPS client helm's registry client pushes
// through. The pool holds only the fixture CA — the helper talks to nothing
// else — and plain HTTP is never enabled.
func registryClient(caFile string) (*http.Client, error) {
	caData, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("--ca-file: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caData) {
		return nil, fmt.Errorf("--ca-file %q contains no PEM certificates", caFile)
	}
	transport := registry.NewTransport(false)
	base, ok := transport.Base.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("cannot apply --ca-file: helm registry transport base is %T, want *http.Transport", transport.Base)
	}
	base.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}
	return &http.Client{Transport: transport}, nil
}
