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
	"encoding/json"
	"io"
	"log"
	"maps"
	"net/http/httptest"
	"net/url"
	"sort"
	"testing"

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

// Registry is a hermetic OCI distribution registry on a loopback port.
type Registry struct {
	Server *httptest.Server
	Host   string
}

// StartRegistry starts an in-process distribution registry. It is closed with
// the test.
func StartRegistry(t *testing.T) *Registry {
	t.Helper()
	server := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(server.Close)
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse registry URL %q: %v", server.URL, err)
	}
	return &Registry{Server: server, Host: parsed.Host}
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
