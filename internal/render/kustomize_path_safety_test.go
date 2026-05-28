package render

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/kustomize/api/types"
)

func TestKustomizeRendererIgnoresUnrelatedCallerSymlinkInWorkspaceCopy(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/example/remote.git//base?ref=v1.2.3
`)
	outside := filepath.Join(t.TempDir(), "outside.yaml")
	writeFile(t, outside, "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: outside\n")
	symlink(t, outside, filepath.Join(root, "unrelated-link.yaml"))
	remoteRepo := t.TempDir()
	writeFile(t, filepath.Join(remoteRepo, "base", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cm.yaml
`)
	writeFile(t, filepath.Join(remoteRepo, "base", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: remote-base
`)
	acquirer := &fakeRemoteAcquirer{path: remoteRepo}

	manifests, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "app",
	}, RenderOptions{
		RemoteResourceAcquirer: acquirer,
		RemoteResourceCacheDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if !containsManifest(manifests, "ConfigMap", "remote-base") {
		t.Fatalf("rendered manifests = %#v, want remote base ConfigMap", manifests)
	}
}
func TestKustomizeRendererRejectsCallerGeneratedDirSymlink(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/example/remote.git//base?ref=v1.2.3
`)
	outside := t.TempDir()
	symlink(t, outside, filepath.Join(root, "app", ".drydock"))
	remoteRepo := t.TempDir()
	writeFile(t, filepath.Join(remoteRepo, "base", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cm.yaml
`)
	writeFile(t, filepath.Join(remoteRepo, "base", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: remote-base
`)
	acquirer := &fakeRemoteAcquirer{path: remoteRepo}

	_, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "app",
	}, RenderOptions{
		RemoteResourceAcquirer: acquirer,
		RemoteResourceCacheDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("Render() error = nil, want generated symlink rejection")
	}
	if !strings.Contains(err.Error(), "generated kustomize path") || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Render() error = %v, want generated symlink rejection", err)
	}
	entries, readErr := os.ReadDir(outside)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("outside symlink target entries = %d, want 0", len(entries))
	}
}
func TestKustomizeRendererIgnoresUnrelatedRemoteSymlinkOutsideGraph(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/example/remote.git//base?ref=v1.2.3
`)
	remoteRepo := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.yaml")
	writeFile(t, outside, "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: outside\n")
	symlink(t, outside, filepath.Join(remoteRepo, "unrelated-link.yaml"))
	writeFile(t, filepath.Join(remoteRepo, "base", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cm.yaml
`)
	writeFile(t, filepath.Join(remoteRepo, "base", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: remote-base
`)
	acquirer := &fakeRemoteAcquirer{path: remoteRepo}

	manifests, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "app",
	}, RenderOptions{
		RemoteResourceAcquirer: acquirer,
		RemoteResourceCacheDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if !containsManifest(manifests, "ConfigMap", "remote-base") {
		t.Fatalf("rendered manifests = %#v, want remote base ConfigMap", manifests)
	}
}
func TestKustomizeRendererRejectsRemoteGeneratedDirSymlink(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/example/outer.git//base?ref=v1.2.3
`)
	outerRepo := t.TempDir()
	writeFile(t, filepath.Join(outerRepo, "base", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/example/inner.git//base?ref=v2.0.0
`)
	outside := t.TempDir()
	symlink(t, outside, filepath.Join(outerRepo, "base", ".drydock"))
	innerRepo := t.TempDir()
	writeFile(t, filepath.Join(innerRepo, "base", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cm.yaml
`)
	writeFile(t, filepath.Join(innerRepo, "base", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: inner-base
`)
	acquirer := &fakeRemoteAcquirer{paths: map[string]string{
		"https://github.com/example/outer.git": outerRepo,
		"https://github.com/example/inner.git": innerRepo,
	}}

	_, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "app",
	}, RenderOptions{
		RemoteResourceAcquirer: acquirer,
		RemoteResourceCacheDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("Render() error = nil, want generated symlink rejection")
	}
	if !strings.Contains(err.Error(), "generated kustomize path") || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Render() error = %v, want generated symlink rejection", err)
	}
	entries, readErr := os.ReadDir(outside)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("outside symlink target entries = %d, want 0", len(entries))
	}
}
func TestKustomizeRendererRejectsRemoteBoundaryEscape(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/example/remote.git//base?ref=v1.2.3
`)
	remoteRepo := t.TempDir()
	writeFile(t, filepath.Join(remoteRepo, "base", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ../../outside.yaml
`)
	acquirer := &fakeRemoteAcquirer{path: remoteRepo}

	_, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "app",
	}, RenderOptions{
		RemoteResourceAcquirer: acquirer,
		RemoteResourceCacheDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("Render() error = nil, want remote boundary escape rejection")
	}
	if !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("Render() error = %v, want escape rejection", err)
	}
}
func TestKustomizeRendererRejectsRemotePathBearingBoundaryEscape(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/example/remote.git//base?ref=v1.2.3
`)
	writeFile(t, filepath.Join(root, "app", "caller-patch.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: remote
  labels:
    leaked: "true"
`)
	remoteRepo := t.TempDir()
	writeFile(t, filepath.Join(remoteRepo, "base", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cm.yaml
patches:
  - path: ../../../../caller-patch.yaml
    target:
      version: v1
      kind: ConfigMap
      name: remote
`)
	writeFile(t, filepath.Join(remoteRepo, "base", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: remote
`)
	acquirer := &fakeRemoteAcquirer{path: remoteRepo}

	_, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "app",
	}, RenderOptions{
		RemoteResourceAcquirer: acquirer,
		RemoteResourceCacheDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("Render() error = nil, want remote path-bearing boundary escape rejection")
	}
	if !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("Render() error = %v, want escape rejection", err)
	}
}
func TestKustomizeRendererRejectsRemoteHelmBoundaryEscapes(t *testing.T) {
	for _, tt := range []struct {
		name          string
		kustomization string
		want          string
	}{
		{
			name: "chartHome",
			kustomization: `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
helmGlobals:
  chartHome: ../../../../caller-charts
helmCharts:
  - name: demo
    releaseName: demo
`,
			want: "escapes",
		},
		{
			name: "chart name",
			kustomization: `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
helmGlobals:
  chartHome: .
helmCharts:
  - name: ../../../../caller-chart
    releaseName: demo
`,
			want: "escapes",
		},
		{
			name: "nameTemplate",
			kustomization: `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
helmCharts:
  - name: demo
    repo: https://charts.example.test
    version: 1.2.3
    nameTemplate: demo-{{ randAlpha 5 }}
`,
			want: "nameTemplate",
		},
		{
			name: "valuesFile",
			kustomization: `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
helmCharts:
  - name: demo
    releaseName: demo
    valuesFile: ../../../../caller-values.yaml
`,
			want: "escapes",
		},
		{
			name: "additionalValuesFiles",
			kustomization: `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
helmCharts:
  - name: demo
    releaseName: demo
    additionalValuesFiles:
      - ../../../../caller-values.yaml
`,
			want: "escapes",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/example/remote.git//base?ref=v1.2.3
`)
			remoteRepo := t.TempDir()
			writeFile(t, filepath.Join(remoteRepo, "base", "kustomization.yaml"), tt.kustomization)
			acquirer := &fakeRemoteAcquirer{path: remoteRepo}

			_, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
				RepoRoot: root,
				Path:     "app",
			}, RenderOptions{
				RemoteResourceAcquirer: acquirer,
				RemoteResourceCacheDir: t.TempDir(),
			})
			if err == nil {
				t.Fatalf("Render() error = nil, want %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Render() error = %v, want %q", err, tt.want)
			}
		})
	}
}
func TestKustomizeRendererRejectsRemoteHelmValueRefsWithoutLeakingSecrets(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
		opts RenderOptions
	}{
		{
			name: "local",
			body: `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
helmCharts:
  - name: demo
    releaseName: demo
    valuesFile: https://user:secret@example.test/values.yaml?token=secret
`,
		},
		{
			name: "remote",
			body: `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/example/remote.git//base?ref=v1.2.3
`,
			opts: RenderOptions{RemoteResourceAcquirer: &fakeRemoteAcquirer{}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), tt.body)
			if tt.name == "remote" {
				remoteRepo := t.TempDir()
				writeFile(t, filepath.Join(remoteRepo, "base", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
helmCharts:
  - name: demo
    releaseName: demo
    additionalValuesFiles:
      - https://user:secret@example.test/values.yaml?token=secret
`)
				tt.opts.RemoteResourceAcquirer = &fakeRemoteAcquirer{path: remoteRepo}
			}

			_, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
				RepoRoot: root,
				Path:     "app",
			}, tt.opts)
			if err == nil {
				t.Fatal("Render() error = nil, want remote Helm values rejection")
			}
			if !strings.Contains(err.Error(), "remote") {
				t.Fatalf("Render() error = %v, want remote ref rejection", err)
			}
			for _, secret := range []string{"user", "secret", "token=secret"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("Render() error = %q leaked %q", err.Error(), secret)
				}
			}
		})
	}
}
func TestKustomizeRendererRejectsRemoteSymlinkedGraphEntry(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/example/remote.git//base?ref=v1.2.3
`)
	remoteRepo := t.TempDir()
	writeFile(t, filepath.Join(remoteRepo, "base", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - linked.yaml
`)
	outside := filepath.Join(t.TempDir(), "outside.yaml")
	writeFile(t, outside, "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: outside\n")
	symlink(t, outside, filepath.Join(remoteRepo, "base", "linked.yaml"))
	acquirer := &fakeRemoteAcquirer{path: remoteRepo}

	_, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "app",
	}, RenderOptions{
		RemoteResourceAcquirer: acquirer,
		RemoteResourceCacheDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("Render() error = nil, want symlink rejection")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Render() error = %v, want symlink rejection", err)
	}
}
func TestKustomizeRendererRejectsRemoteGitSubpathEscape(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/example/repo.git//../outside.yaml?ref=v1.2.3
`)
	acquirer := &fakeRemoteAcquirer{path: t.TempDir()}

	_, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "app",
	}, RenderOptions{
		RemoteResourceAcquirer: acquirer,
		RemoteResourceCacheDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("Render() error = nil, want subpath escape rejection")
	}
	if !strings.Contains(err.Error(), "escapes acquired repository") {
		t.Fatalf("Render() error = %v, want acquired repository escape error", err)
	}
}
func TestKustomizeRendererRejectsRemoteGitSymlinkedSubpath(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/example/repo.git//link.yaml?ref=v1.2.3
`)
	remoteRepo := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.yaml")
	writeFile(t, outside, "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: outside\n")
	symlink(t, outside, filepath.Join(remoteRepo, "link.yaml"))
	acquirer := &fakeRemoteAcquirer{path: remoteRepo}

	_, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "app",
	}, RenderOptions{
		RemoteResourceAcquirer: acquirer,
		RemoteResourceCacheDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("Render() error = nil, want symlink rejection")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Render() error = %v, want symlink error", err)
	}
}
func TestKustomizeRendererRejectsRemoteGitSymlinkedRoot(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/example/repo.git//base?ref=v1.2.3
`)
	remoteRepo := t.TempDir()
	writeFile(t, filepath.Join(remoteRepo, "base", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cm.yaml
`)
	writeFile(t, filepath.Join(remoteRepo, "base", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: remote-base
`)
	remoteLink := filepath.Join(t.TempDir(), "remote-link")
	symlink(t, remoteRepo, remoteLink)
	acquirer := &fakeRemoteAcquirer{path: remoteLink}

	_, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "app",
	}, RenderOptions{
		RemoteResourceAcquirer: acquirer,
		RemoteResourceCacheDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("Render() error = nil, want symlink root rejection")
	}
	if !strings.Contains(err.Error(), "symlinked repository root") {
		t.Fatalf("Render() error = %v, want symlinked repository root error", err)
	}
}
func TestKustomizeRendererRejectsRemoteResourceSymlink(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://raw.githubusercontent.com/example/repo/main/resource.yaml
`)
	outside := filepath.Join(t.TempDir(), "secret.yaml")
	writeFile(t, outside, "apiVersion: v1\nkind: Secret\nmetadata:\n  name: leaked\n")
	cacheFile := filepath.Join(t.TempDir(), "resource.yaml")
	symlink(t, outside, cacheFile)
	acquirer := &fakeRemoteAcquirer{path: cacheFile}

	_, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "app",
	}, RenderOptions{
		RemoteResourceAcquirer: acquirer,
		RemoteResourceCacheDir: t.TempDir(),
		OfflineRemoteResources: true,
	})
	if err == nil {
		t.Fatal("Render() error = nil, want symlink rejection")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Render() error = %v, want symlink rejection", err)
	}
}
func TestCopyRegularTreeSkipsGitFilesAndDirectories(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "dst")
	writeFile(t, filepath.Join(src, ".git"), "gitdir: ../main/.git/worktrees/demo\n")
	writeFile(t, filepath.Join(src, "nested", ".git", "config"), "[core]\n")
	writeFile(t, filepath.Join(src, "nested", "manifest.yaml"), "apiVersion: v1\nkind: ConfigMap\n")

	if err := copyRegularTree(src, dst); err != nil {
		t.Fatalf("copyRegularTree() error = %v", err)
	}
	assertPathMissing(t, filepath.Join(dst, ".git"))
	assertPathMissing(t, filepath.Join(dst, "nested", ".git"))
	if _, err := os.Stat(filepath.Join(dst, "nested", "manifest.yaml")); err != nil {
		t.Fatalf("copied manifest Stat() error = %v", err)
	}
}
func TestCopyPreparedKustomizeWorkspaceSkipsUnreferencedRepoTrees(t *testing.T) {
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "apps", "demo")
	writeFile(t, filepath.Join(sourceRoot, "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ../../components/namespace
  - cm.yaml
helmCharts:
  - name: demo
    repo: https://charts.example.test
    version: 1.2.3
`)
	writeFile(t, filepath.Join(sourceRoot, "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
`)
	writeFile(t, filepath.Join(root, "components", "namespace", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - namespace.yaml
`)
	writeFile(t, filepath.Join(root, "components", "namespace", "namespace.yaml"), `apiVersion: v1
kind: Namespace
metadata:
  name: demo
`)
	writeFile(t, filepath.Join(root, "hack", "large-unrelated-artifact.txt"), "not needed\n")
	_, graph, err := collectKustomizeGraphForPreparation(context.Background(), root, sourceRoot)
	if err != nil {
		t.Fatalf("collectKustomizeGraphForPreparation() error = %v", err)
	}

	dst := filepath.Join(t.TempDir(), "repo")
	if err := copyPreparedKustomizeWorkspaceTree(context.Background(), root, sourceRoot, dst, graph, RenderOptions{}); err != nil {
		t.Fatalf("copyPreparedKustomizeWorkspaceTree() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(dst, "apps", "demo", "kustomization.yaml")); err != nil {
		t.Fatalf("copied app kustomization Stat() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "components", "namespace", "namespace.yaml")); err != nil {
		t.Fatalf("copied component Stat() error = %v", err)
	}
	assertPathMissing(t, filepath.Join(dst, "hack"))
}

func TestCopyPreparedKustomizeWorkspaceHardlinksOnlyReadOnlyFiles(t *testing.T) {
	if !hardlinksSupported(t) {
		t.Skip("hardlinks unavailable")
	}
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "apps", "demo")
	writeFile(t, filepath.Join(sourceRoot, "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cm.yaml
`)
	writeFile(t, filepath.Join(sourceRoot, "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
`)
	_, graph, err := collectKustomizeGraphForPreparation(context.Background(), root, sourceRoot)
	if err != nil {
		t.Fatalf("collectKustomizeGraphForPreparation() error = %v", err)
	}

	dst := filepath.Join(t.TempDir(), "repo")
	if err := copyPreparedKustomizeWorkspaceTree(context.Background(), root, sourceRoot, dst, graph, RenderOptions{}); err != nil {
		t.Fatalf("copyPreparedKustomizeWorkspaceTree() error = %v", err)
	}

	assertSameFile(t, filepath.Join(sourceRoot, "cm.yaml"), filepath.Join(dst, "apps", "demo", "cm.yaml"))
	assertDifferentFile(t, filepath.Join(sourceRoot, "kustomization.yaml"), filepath.Join(dst, "apps", "demo", "kustomization.yaml"))
}

func hardlinksSupported(t *testing.T) bool {
	t.Helper()
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	if err := os.WriteFile(src, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	return os.Link(src, dst) == nil
}

func assertSameFile(t *testing.T, left, right string) {
	t.Helper()
	leftInfo, err := os.Stat(left)
	if err != nil {
		t.Fatal(err)
	}
	rightInfo, err := os.Stat(right)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(leftInfo, rightInfo) {
		t.Fatalf("%s and %s are different files, want same file", left, right)
	}
}

func assertDifferentFile(t *testing.T, left, right string) {
	t.Helper()
	leftInfo, err := os.Stat(left)
	if err != nil {
		t.Fatal(err)
	}
	rightInfo, err := os.Stat(right)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(leftInfo, rightInfo) {
		t.Fatalf("%s and %s are the same file, want different files", left, right)
	}
}
func TestKustomizeRendererRejectsSourcePathEscape(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "repo")
	outside := filepath.Join(parent, "outside")
	writeFile(t, filepath.Join(outside, "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: []
`)

	renderer := KustomizeRenderer{}
	result, diags, err := renderer.Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     ".." + string(filepath.Separator) + "outside",
	}, RenderOptions{})
	if err == nil {
		t.Fatal("Render() error = nil, want path escape error")
	}
	if !strings.Contains(err.Error(), "escapes repository root") {
		t.Fatalf("Render() error = %v, want escape error", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(result) != 0 {
		t.Fatalf("result = %#v, want no manifests", result)
	}
}
func TestKustomizeRendererRejectsKustomizationGraphEscapes(t *testing.T) {
	for _, tt := range []struct {
		name           string
		kustomization  string
		outsideRelPath string
	}{
		{
			name: "resource",
			kustomization: `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ../../../outside/cm.yaml
`,
			outsideRelPath: filepath.Join("outside", "cm.yaml"),
		},
		{
			name: "base",
			kustomization: `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
bases:
  - ../../../outside
`,
			outsideRelPath: filepath.Join("outside", "kustomization.yaml"),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			parent := t.TempDir()
			root := filepath.Join(parent, "repo")
			outside := filepath.Join(parent, tt.outsideRelPath)
			writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), tt.kustomization)
			writeFile(t, outside, `
apiVersion: v1
kind: ConfigMap
metadata:
  name: outside
`)

			result, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
				RepoRoot: root,
				Path:     filepath.Join("apps", "demo"),
			}, RenderOptions{})
			if err == nil {
				t.Fatal("Render() error = nil, want graph escape error")
			}
			if !strings.Contains(err.Error(), "escapes repository root") {
				t.Fatalf("Render() error = %v, want repository root escape error", err)
			}
			if len(diags) != 0 {
				t.Fatalf("diagnostics = %#v", diags)
			}
			if len(result) != 0 {
				t.Fatalf("result = %#v, want no manifests", result)
			}
		})
	}
}
func TestKustomizeRendererRejectsSymlinkedSourcePathComponent(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: []
`)
	symlink(t, outside, filepath.Join(root, "apps"))

	renderer := KustomizeRenderer{}
	result, diags, err := renderer.Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "apps",
	}, RenderOptions{})
	if err == nil {
		t.Fatal("Render() error = nil, want symlink error")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Render() error = %v, want symlink error", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(result) != 0 {
		t.Fatalf("result = %#v, want no manifests", result)
	}
}
func TestKustomizeRendererRejectsSymlinkedGraphEntries(t *testing.T) {
	for _, tt := range []struct {
		name          string
		kustomization string
		linkName      string
		targetFile    string
		targetDir     bool
	}{
		{
			name: "resource",
			kustomization: `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - linked.yaml
`,
			linkName:   "linked.yaml",
			targetFile: "cm.yaml",
		},
		{
			name: "base",
			kustomization: `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
bases:
  - linked-base
`,
			linkName:  "linked-base",
			targetDir: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			outside := t.TempDir()
			writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), tt.kustomization)
			target := outside
			if !tt.targetDir {
				target = filepath.Join(outside, tt.targetFile)
			}
			writeFile(t, filepath.Join(outside, "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: []
`)
			if tt.targetFile != "" {
				writeFile(t, filepath.Join(outside, tt.targetFile), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: outside
`)
			}
			symlink(t, target, filepath.Join(root, "app", tt.linkName))

			result, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
				RepoRoot: root,
				Path:     "app",
			}, RenderOptions{})
			if err == nil {
				t.Fatal("Render() error = nil, want symlink error")
			}
			if !strings.Contains(err.Error(), "symlink") {
				t.Fatalf("Render() error = %v, want symlink error", err)
			}
			if len(diags) != 0 {
				t.Fatalf("diagnostics = %#v", diags)
			}
			if len(result) != 0 {
				t.Fatalf("result = %#v, want no manifests", result)
			}
		})
	}
}
func TestKustomizeRendererRejectsRemoteRefs(t *testing.T) {
	for _, tt := range []struct {
		name          string
		kustomization string
	}{
		{
			name: "remote file component",
			kustomization: `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
components:
  - https://raw.githubusercontent.com/example/repo/main/component.yaml
`,
		},
		{
			name: "file URL resource",
			kustomization: `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - file:///tmp/repo//base
`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assertKustomizeRenderErrorContains(t, tt.kustomization, "remote")
		})
	}
}
func TestKustomizeRendererRejectsUnsupportedRemoteRefsWithoutLeakingSecrets(t *testing.T) {
	for name, body := range map[string]string{
		"malformed-resource-url": `resources:
  - https://user:secret@example.test/%zz.yaml?token=secret#fragment
`,
		"component": `components:
  - https://user:secret@example.test/component.yaml?token=secret
resources:
  - local.yaml
`,
		"malformed-component-url": `components:
  - https://user:secret@example.test/%zz.yaml?token=secret#fragment
resources:
  - local.yaml
`,
		"git-scp": `components:
  - git@github.com:org/repo.git//base?ref=main&token=secret
resources:
  - local.yaml
`,
		"schemeless-github": `components:
  - github.com/org/repo//base?ref=main&token=secret
resources:
  - local.yaml
`,
		"patch": `patches:
  - path: https://user:secret@example.test/patch.yaml?token=secret
resources:
  - local.yaml
`,
		"malformed-patch-url": `patches:
  - path: https://user:secret@example.test/%zz.yaml?token=secret#fragment
resources:
  - local.yaml
`,
		"schemeless-fragment": `components:
  - github.com/org/repo//base#token=secret
resources:
  - local.yaml
`,
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, "app", "local.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: local\n")
			writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\n"+body)

			_, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
				RepoRoot: root,
				Path:     "app",
			}, RenderOptions{})
			if err == nil {
				t.Fatal("Render() error = nil, want unsupported remote ref error")
			}
			message := err.Error()
			for _, leaked := range []string{"secret", "token=", "user:secret", "user:", "@example.test", "?token", "#fragment", "#token"} {
				if strings.Contains(message, leaked) {
					t.Fatalf("Render() error leaked %q: %s", leaked, message)
				}
			}
			if !strings.Contains(message, "remote Kustomize refs are unsupported") {
				t.Fatalf("Render() error = %v, want unsupported remote message", err)
			}
		})
	}
}
func TestParseKustomizeBuildOptionsSupportsArgoCDDefaults(t *testing.T) {
	settings, err := parseKustomizeBuildOptions([]string{"--enable-helm", "--load-restrictor=LoadRestrictionsNone"})
	if err != nil {
		t.Fatalf("parseKustomizeBuildOptions() error = %v", err)
	}
	if settings.LoadRestrictions != types.LoadRestrictionsNone {
		t.Fatalf("LoadRestrictions = %v, want LoadRestrictionsNone", settings.LoadRestrictions)
	}
}

func TestParseKustomizeBuildOptionsSupportsSplitLoadRestrictor(t *testing.T) {
	settings, err := parseKustomizeBuildOptions([]string{"--load-restrictor", "LoadRestrictionsNone"})
	if err != nil {
		t.Fatalf("parseKustomizeBuildOptions() error = %v", err)
	}
	if settings.LoadRestrictions != types.LoadRestrictionsNone {
		t.Fatalf("LoadRestrictions = %v, want LoadRestrictionsNone", settings.LoadRestrictions)
	}
}

func TestParseKustomizeBuildOptionsSupportsHelmAPIVersions(t *testing.T) {
	settings, err := parseKustomizeBuildOptions([]string{
		"--helm-api-versions",
		"example.io/v1/Foo,example.io/v1/Bar",
		"--helm-api-versions=other.io/v1/Baz",
	})
	if err != nil {
		t.Fatalf("parseKustomizeBuildOptions() error = %v", err)
	}
	want := []string{"example.io/v1/Foo", "example.io/v1/Bar", "other.io/v1/Baz"}
	if strings.Join(settings.APIVersions, ",") != strings.Join(want, ",") {
		t.Fatalf("APIVersions = %#v, want %#v", settings.APIVersions, want)
	}
}

func TestParseKustomizeBuildOptionsRejectsUnsupportedOptions(t *testing.T) {
	_, err := parseKustomizeBuildOptions([]string{"--enable-alpha-plugins"})
	if err == nil {
		t.Fatal("parseKustomizeBuildOptions() error = nil, want unsupported option error")
	}
	if !strings.Contains(err.Error(), "unsupported kustomize build option") {
		t.Fatalf("parseKustomizeBuildOptions() error = %v, want unsupported option message", err)
	}
}
