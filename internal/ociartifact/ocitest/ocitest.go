// Package ocitest provides hermetic OCI registry fixtures for drydock tests:
// an httptest-mounted distribution server plus artifact push helpers shaped
// like the artifacts the Argo CD OCI client consumes (helm chart artifacts,
// plain-manifest artifacts, and guard-violating variants). Test-only.
package ocitest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"log"
	"maps"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/registry"
	specs "github.com/opencontainers/image-spec/specs-go"
	imagev1 "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/memory"
	"oras.land/oras-go/v2/registry/remote"
)

const (
	helmConfigMediaType = "application/vnd.cncf.helm.config.v1+json"
	helmLayerMediaType  = "application/vnd.cncf.helm.chart.content.v1.tar+gzip"
	plainLayerMediaType = "application/vnd.oci.image.layer.v1.tar+gzip"
	plainConfigType     = "application/vnd.oci.empty.v1+json"
)

// Registry is a hermetic OCI distribution registry on a loopback port. Every
// registry wraps the distribution handler with mutable middleware: optional
// Basic auth (EnableBasicAuth) and a /tags/list wire-request counter
// (TagListRequests).
type Registry struct {
	Server *httptest.Server
	Host   string

	mu              sync.Mutex
	authEnabled     bool
	authUsername    string
	authPassword    string
	tagListRequests int
	// pushTLS, when set, is used by the push helpers instead of plain HTTP
	// (TLS/mTLS registries).
	pushTLS *tls.Config
}

// StartRegistry starts an in-process distribution registry. It is closed with
// the test.
func StartRegistry(t *testing.T) *Registry {
	t.Helper()
	return newRegistry(t, httptest.NewServer)
}

// StartTLSRegistry starts an https loopback registry with the httptest
// self-signed server certificate. Clients trust it via CAFilePath
// (--oci-ca-file) or --oci-insecure-skip-verify; either flag also disables
// drydock's loopback plain-HTTP default so the client actually negotiates
// TLS against this server.
func StartTLSRegistry(t *testing.T) *Registry {
	t.Helper()
	reg := newRegistry(t, httptest.NewTLSServer)
	pool := x509.NewCertPool()
	pool.AddCert(reg.Server.Certificate())
	reg.pushTLS = &tls.Config{RootCAs: pool}
	return reg
}

// StartMTLSRegistry starts an https loopback registry that requires and
// verifies a client certificate (tls.RequireAndVerifyClientCert). It returns
// PEM file paths for the accepted client cert/key pair
// (--oci-client-cert-file/--oci-client-key-file); the push helpers present
// the same pair.
func StartMTLSRegistry(t *testing.T) (reg *Registry, certFile, keyFile string) {
	t.Helper()
	certFile, keyFile = GenerateClientCertFiles(t)
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		t.Fatalf("read client cert: %v", err)
	}
	clientPool := x509.NewCertPool()
	if !clientPool.AppendCertsFromPEM(certPEM) {
		t.Fatal("client cert PEM did not parse")
	}
	reg = newRegistry(t, func(handler http.Handler) *httptest.Server {
		server := httptest.NewUnstartedServer(handler)
		server.TLS = &tls.Config{ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: clientPool}
		server.StartTLS()
		return server
	})
	serverPool := x509.NewCertPool()
	serverPool.AddCert(reg.Server.Certificate())
	clientCert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		t.Fatalf("load client key pair: %v", err)
	}
	reg.pushTLS = &tls.Config{RootCAs: serverPool, Certificates: []tls.Certificate{clientCert}}
	return reg, certFile, keyFile
}

func newRegistry(t *testing.T, start func(http.Handler) *httptest.Server) *Registry {
	t.Helper()
	reg := &Registry{}
	inner := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	server := start(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		reg.serve(inner, w, req)
	}))
	t.Cleanup(server.Close)
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse registry URL %q: %v", server.URL, err)
	}
	reg.Server = server
	reg.Host = parsed.Host
	return reg
}

// EnableBasicAuth requires the given Basic credentials on every subsequent
// request. Seed artifacts BEFORE enabling: the push helpers are
// uncredentialed.
func (r *Registry) EnableBasicAuth(username, password string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.authEnabled = true
	r.authUsername = username
	r.authPassword = password
}

// TagListRequests reports how many wire requests hit a /tags/list path.
func (r *Registry) TagListRequests() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.tagListRequests
}

// CAFilePath writes the server certificate as a PEM file for --oci-ca-file.
func (r *Registry) CAFilePath(t *testing.T) string {
	t.Helper()
	cert := r.Server.Certificate()
	if cert == nil {
		t.Fatal("registry has no TLS certificate (plain-HTTP registry)")
	}
	data := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	path := filepath.Join(t.TempDir(), "registry-ca.pem")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write CA file: %v", err)
	}
	return path
}

// GenerateClientCertFiles writes a self-signed client-auth cert/key PEM pair
// (the cert is its own CA, so it verifies against a ClientCAs pool holding
// itself). The cert PEM also doubles as generic valid-PEM-certificate input
// for CA-file validation tests.
func GenerateClientCertFiles(t *testing.T) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate client key: %v", err)
	}
	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "drydock-ocitest-client"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create client cert: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal client key: %v", err)
	}
	dir := t.TempDir()
	certFile = filepath.Join(dir, "client-cert.pem")
	keyFile = filepath.Join(dir, "client-key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("write client cert: %v", err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatalf("write client key: %v", err)
	}
	return certFile, keyFile
}

func (r *Registry) serve(inner http.Handler, w http.ResponseWriter, req *http.Request) {
	if strings.HasSuffix(req.URL.Path, "/tags/list") {
		r.mu.Lock()
		r.tagListRequests++
		r.mu.Unlock()
	}
	r.mu.Lock()
	enabled, username, password := r.authEnabled, r.authUsername, r.authPassword
	r.mu.Unlock()
	if enabled {
		gotUser, gotPass, ok := req.BasicAuth()
		if !ok || gotUser != username || gotPass != password {
			writeUnauthorized(w, req)
			return
		}
	}
	inner.ServeHTTP(w, req)
}

// writeUnauthorized answers 401. The Www-Authenticate: Basic header is
// mandatory: the ORAS auth client only retries with credentials after parsing
// it (oras-go v2.6.1 auth/client.go:308-323,380-390). The errcode-shaped JSON
// body ECHOES the received Authorization header raw AND base64-decoded so a
// leaked credential is observable in client error text — redaction tests bite
// on real leak-shaped output instead of passing vacuously.
func writeUnauthorized(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Www-Authenticate", `Basic realm="drydock-ocitest"`)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	message := "authentication required"
	if authorization := req.Header.Get("Authorization"); authorization != "" {
		message += "; received authorization " + authorization
		if value, found := strings.CutPrefix(authorization, "Basic "); found {
			if decoded, err := base64.StdEncoding.DecodeString(value); err == nil {
				message += " decoded " + string(decoded)
			}
		}
	}
	type errcodeError struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	body := struct {
		Errors []errcodeError `json:"errors"`
	}{Errors: []errcodeError{{Code: "UNAUTHORIZED", Message: message}}}
	data, err := json.Marshal(body)
	if err != nil {
		return
	}
	_, _ = w.Write(data)
}

// RepoURL returns the oci:// URL for a repository on this registry.
func (r *Registry) RepoURL(name string) string {
	return "oci://" + r.Host + "/" + name
}

type layerSpec struct {
	mediaType string
	data      []byte
}

// HelmChartSpec describes a helm chart artifact fixture. Files are relative
// to the chart directory; Chart.yaml, values.yaml, and a marker template are
// generated unless overridden.
type HelmChartSpec struct {
	Name    string
	Version string
	Files   map[string]string
}

func (spec HelmChartSpec) files() map[string]string {
	name := spec.Name
	if name == "" {
		name = "demo"
	}
	version := spec.Version
	if version == "" {
		version = "1.0.0"
	}
	files := map[string]string{
		"Chart.yaml":  "apiVersion: v2\nname: " + name + "\nversion: " + version + "\n",
		"values.yaml": "message: from-defaults\n",
		"templates/configmap.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n" +
			"  name: {{ .Release.Name }}-demo\ndata:\n" +
			"  message: {{ .Values.message | quote }}\n" +
			"  marker: oci-artifact-content\n",
	}
	maps.Copy(files, spec.Files)
	return files
}

// PushHelmChartArtifact pushes a helm chart artifact (helm config media type,
// single helm content layer whose tgz holds exactly one top-level chart
// directory — what makes `path: .` land on the chart root) and returns the
// manifest digest.
func PushHelmChartArtifact(t *testing.T, reg *Registry, repoName, tag string, spec HelmChartSpec) string {
	t.Helper()
	name := spec.Name
	if name == "" {
		name = "demo"
	}
	tgz := buildTgz(t, name+"/", spec.files())
	return pushArtifact(t, reg, repoName, tag, helmConfigMediaType, []layerSpec{{mediaType: helmLayerMediaType, data: tgz}})
}

// PushPlainManifestsArtifact pushes a generic single-content-layer artifact
// whose tgz entries land at the extraction root.
func PushPlainManifestsArtifact(t *testing.T, reg *Registry, repoName, tag string, files map[string]string) string {
	t.Helper()
	tgz := buildTgz(t, "", files)
	return pushArtifact(t, reg, repoName, tag, plainConfigType, []layerSpec{{mediaType: plainLayerMediaType, data: tgz}})
}

// PushDisallowedMediaTypeArtifact pushes an artifact whose content layer uses
// a tar-suffixed media type outside the allowlist.
func PushDisallowedMediaTypeArtifact(t *testing.T, reg *Registry, repoName, tag string) string {
	t.Helper()
	tgz := buildTgz(t, "", map[string]string{"payload.yaml": "kind: Nope\n"})
	return pushArtifact(t, reg, repoName, tag, plainConfigType, []layerSpec{{mediaType: "application/vnd.drydock.test.blocked.tar+gzip", data: tgz}})
}

// PushCorruptContentLayerArtifact pushes an artifact whose single allowlisted
// content layer is not valid gzip: fetch and image-tar save succeed, layer
// extraction fails — the shape that exercises post-save failure handling.
func PushCorruptContentLayerArtifact(t *testing.T, reg *Registry, repoName, tag string) string {
	t.Helper()
	return pushArtifact(t, reg, repoName, tag, plainConfigType, []layerSpec{{mediaType: plainLayerMediaType, data: []byte("not-a-gzip-stream")}})
}

// PushMultiContentLayerArtifact pushes an artifact with two allowlisted
// content layers, violating the exactly-one-content-layer guard.
func PushMultiContentLayerArtifact(t *testing.T, reg *Registry, repoName, tag string) string {
	t.Helper()
	first := buildTgz(t, "", map[string]string{"first.yaml": "kind: First\n"})
	second := buildTgz(t, "", map[string]string{"second.yaml": "kind: Second\n"})
	return pushArtifact(t, reg, repoName, tag, plainConfigType, []layerSpec{
		{mediaType: plainLayerMediaType, data: first},
		{mediaType: plainLayerMediaType, data: second},
	})
}

// TagAlias tags an already-pushed reference (tag or digest) with another tag,
// aliasing both to one manifest digest.
func TagAlias(t *testing.T, reg *Registry, repoName, existingRef, newTag string) {
	t.Helper()
	repo := remoteRepo(t, reg, repoName)
	if _, err := oras.Tag(t.Context(), repo, existingRef, newTag); err != nil {
		t.Fatalf("tag %s as %s: %v", existingRef, newTag, err)
	}
}

func remoteRepo(t *testing.T, reg *Registry, repoName string) *remote.Repository {
	t.Helper()
	repo, err := remote.NewRepository(reg.Host + "/" + repoName)
	if err != nil {
		t.Fatalf("new repository %s/%s: %v", reg.Host, repoName, err)
	}
	if reg.pushTLS != nil {
		repo.Client = &http.Client{Transport: &http.Transport{TLSClientConfig: reg.pushTLS.Clone()}}
		return repo
	}
	repo.PlainHTTP = true
	return repo
}

func pushArtifact(t *testing.T, reg *Registry, repoName, tag, configMediaType string, layers []layerSpec) string {
	t.Helper()
	ctx := t.Context()
	store := memory.New()

	configBytes := []byte("{}")
	configDesc := content.NewDescriptorFromBytes(configMediaType, configBytes)
	pushBlob(t, ctx, store, configDesc, configBytes)

	layerDescs := make([]imagev1.Descriptor, 0, len(layers))
	for _, layer := range layers {
		desc := content.NewDescriptorFromBytes(layer.mediaType, layer.data)
		pushBlob(t, ctx, store, desc, layer.data)
		layerDescs = append(layerDescs, desc)
	}

	manifest := imagev1.Manifest{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: imagev1.MediaTypeImageManifest,
		Config:    configDesc,
		Layers:    layerDescs,
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	manifestDesc := content.NewDescriptorFromBytes(imagev1.MediaTypeImageManifest, manifestBytes)
	pushBlob(t, ctx, store, manifestDesc, manifestBytes)
	if err := store.Tag(ctx, manifestDesc, tag); err != nil {
		t.Fatalf("tag manifest %s: %v", tag, err)
	}

	repo := remoteRepo(t, reg, repoName)
	if _, err := oras.Copy(ctx, store, tag, repo, tag, oras.DefaultCopyOptions); err != nil {
		t.Fatalf("push artifact %s/%s:%s: %v", reg.Host, repoName, tag, err)
	}
	return manifestDesc.Digest.String()
}

func pushBlob(t *testing.T, ctx context.Context, store *memory.Store, desc imagev1.Descriptor, data []byte) {
	t.Helper()
	if err := store.Push(ctx, desc, bytes.NewReader(data)); err != nil {
		t.Fatalf("push blob %s: %v", desc.MediaType, err)
	}
}

// buildTgz builds a deterministic tgz whose entries are prefixed with prefix
// (use "name/" for helm charts' single top-level chart directory, "" for
// root-level plain manifests).
func buildTgz(t *testing.T, prefix string, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	if prefix != "" {
		if err := tw.WriteHeader(&tar.Header{Name: prefix, Mode: 0o755, Typeflag: tar.TypeDir}); err != nil {
			t.Fatalf("write tar dir header: %v", err)
		}
	}
	for _, name := range names {
		data := []byte(files[name])
		if err := tw.WriteHeader(&tar.Header{Name: prefix + name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatalf("write tar header %s: %v", name, err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatalf("write tar data %s: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buf.Bytes()
}
