package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/manifest"
)

// Discovery records a settings document by its manifest.DecodeDocumentRoots
// index, and the per-document loaders re-read the file at that index, so both
// must count documents alike.
func TestDocumentLoadersReadTheDocumentManifestDecodingIndexed(t *testing.T) {
	const argocdCM = `apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
data:
  resource.compareoptions: |
    ignoreAggregatedRoles: true
`
	const other = `apiVersion: v1
kind: ConfigMap
metadata:
  name: other
`
	for name, content := range map[string]string{
		"comment preamble":       "# settings\n---\n" + argocdCM,
		"consecutive separators": other + "---\n---\n" + argocdCM,
		"leading separators":     "---\n---\n---\n" + argocdCM,
		"comment-only document":  other + "---\n# nothing here\n---\n" + argocdCM,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.yaml")
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
			docs, err := manifest.DecodeDocumentRoots(path, strings.NewReader(content))
			if err != nil {
				t.Fatalf("DecodeDocumentRoots() error = %v", err)
			}
			index := -1
			for _, doc := range docs {
				if doc.Object.GetName() == "argocd-cm" {
					index = doc.Index
				}
			}
			if index < 0 {
				t.Fatalf("argocd-cm not decoded: %#v", docs)
			}
			settings, diags, err := config.LoadFromConfigMapDocument(path, index)
			if err != nil {
				t.Fatalf("LoadFromConfigMapDocument(%d) error = %v", index, err)
			}
			if len(diags) != 0 || !settings.CompareOptions.IgnoreAggregatedRoles {
				t.Fatalf("LoadFromConfigMapDocument(%d) = %#v, %#v; want the argocd-cm compare options", index, settings.CompareOptions, diags)
			}
		})
	}
}

// The whole-file loaders split a file with the same decoder, so they read a
// JSON stream kubectl applies, several objects back to back, as discovery
// does.
func TestWholeFileLoadersReadAJSONStream(t *testing.T) {
	const other = `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"other"}}`
	for name, tt := range map[string]struct {
		object string
		load   func(string) (config.ArgoSettings, []diagnostic.Diagnostic, error)
		found  func(config.ArgoSettings) bool
	}{
		"repository secret": {
			object: `{"apiVersion":"v1","kind":"Secret","metadata":{"name":"charts","labels":{"argocd.argoproj.io/secret-type":"repository"}},"stringData":{"name":"charts","type":"helm","url":"ghcr.io/example/charts"}}`,
			load:   config.LoadRepositorySecret,
			found: func(settings config.ArgoSettings) bool {
				_, ok := settings.HelmRepositories["ghcr.io/example/charts"]
				return ok
			},
		},
		"cluster secret": {
			object: `{"apiVersion":"v1","kind":"Secret","metadata":{"name":"in-cluster","labels":{"argocd.argoproj.io/secret-type":"cluster"}},"stringData":{"name":"in-cluster","server":"https://kubernetes.default.svc"}}`,
			load:   config.LoadClusterSecret,
			found: func(settings config.ArgoSettings) bool {
				_, ok := settings.Clusters["https://kubernetes.default.svc"]
				return ok
			},
		},
		"command parameters": {
			object: `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"argocd-cmd-params-cm"},"data":{"repo.server":"argocd-repo-server:8081"}}`,
			load:   config.LoadCommandParametersConfigMap,
			found: func(settings config.ArgoSettings) bool {
				return len(settings.CommandParameters) == 1 && settings.CommandParameters[0].Key == "repo.server"
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			if err := os.WriteFile(path, []byte(other+"\n"+tt.object+"\n"), 0o644); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
			settings, diags, err := tt.load(path)
			if err != nil {
				t.Fatalf("load error = %v", err)
			}
			if len(diags) != 0 || !tt.found(settings) {
				t.Fatalf("load = %#v, %#v; want the second object's settings", settings, diags)
			}
		})
	}
}
