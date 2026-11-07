package render

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
)

// A component that spec.source.kustomize adds is walked with the plugin
// referent checks of the source walk, so a generators: or transformers:
// directory entry in it cannot make drydock fetch a remote resource or read
// a file outside the repository. Before, drydock built such a directory
// with krusty without any check.
func TestSourceComponentPluginDirectoriesAreChecked(t *testing.T) {
	// The files outside the repository: a data file, and a builtin
	// transformer whose label would carry the marker into the output.
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "outside.txt")
	if err := os.WriteFile(outside, []byte("outside-marker"), 0o600); err != nil {
		t.Fatal(err)
	}
	outsideTransformer := filepath.Join(outsideDir, "label.yaml")
	if err := os.WriteFile(outsideTransformer, []byte(strings.Replace(builtinLabelTransformer, `added: "true"`, `added: outside-marker`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		field   string
		dirKust string
		remote  bool
		options []string
	}{
		{name: "remote resource in a generators directory", field: "generators", remote: true},
		{name: "remote resource in a transformers directory", field: "transformers", remote: true, options: []string{"--enable-alpha-plugins"}},
		// A builtin transformer from a file outside the repository.
		{name: "file outside the repository in a transformers directory", field: "transformers", dirKust: "resources:\n  - " + outsideTransformer + "\n", options: []string{"--enable-alpha-plugins", "--load-restrictor", "LoadRestrictionsNone"}},
		// A skipped generator configuration made from a file outside the
		// repository, which a replacement copies into the output.
		{name: "file outside the repository in a generators directory", field: "generators", dirKust: "configMapGenerator:\n  - name: stolen\n    files:\n      - " + outside + "\n    options:\n      disableNameSuffixHash: true\n      annotations:\n        config.kubernetes.io/function: |\n          exec:\n            path: ./steal.sh\n", options: []string{"--enable-alpha-plugins", "--load-restrictor", "LoadRestrictionsNone"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			connections := listenForNoConnections(t)
			dirKust := tc.dirKust
			if tc.remote {
				dirKust = "resources:\n  - http://" + connections.addr + "/remote.yaml\n"
			}
			root := t.TempDir()
			appKustomization := "resources: [cm.yaml]\n"
			if tc.field == "generators" && !tc.remote {
				appKustomization += "replacements:\n  - source:\n      kind: ConfigMap\n      name: stolen\n      fieldPath: data.outside\\.txt\n    targets:\n      - select:\n          name: plain\n        fieldPaths: [data.a]\n        options: {create: true}\n"
			}
			writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), appKustomization)
			writeFile(t, filepath.Join(root, "app", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: plain\n")
			writeFile(t, filepath.Join(root, "comp", "kustomization.yaml"), "apiVersion: kustomize.config.k8s.io/v1alpha1\nkind: Component\n"+tc.field+": [plugins]\n")
			writeFile(t, filepath.Join(root, "comp", "plugins", "kustomization.yaml"), dirKust)
			caller := KustomizeCallerArgoCDAssumed
			if len(tc.options) != 0 {
				caller = KustomizeCallerArgoCD
			}
			manifests, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: root, Path: "app"}, RenderOptions{
				Kustomize:              &argoappv1.ApplicationSourceKustomize{Components: []string{"../comp"}},
				KustomizeCaller:        caller,
				BuildOptions:           tc.options,
				OfflineRemoteResources: true,
				RemoteResourceCacheDir: t.TempDir(),
			})
			for _, manifest := range manifests {
				if text, _ := json.Marshal(manifest.Object.Object); strings.Contains(string(text), "outside-marker") {
					t.Fatalf("Render() put the content of a file outside the repository in %s", text)
				}
			}
			if err == nil {
				t.Fatalf("Render() = %d manifests, want an error", len(manifests))
			}
			if got := connections.count.Load(); got != 0 {
				t.Fatalf("listener got %d connections, want none", got)
			}
		})
	}
}

type connectionCounter struct {
	addr  string
	count atomic.Int32
}

// listenForNoConnections starts a local listener that counts the
// connections it gets.
func listenForNoConnections(t *testing.T) *connectionCounter {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	counter := &connectionCounter{addr: listener.Addr().String()}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			counter.count.Add(1)
			_ = conn.Close()
		}
	}()
	return counter
}
