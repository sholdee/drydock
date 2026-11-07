package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
)

// writeHelmFileParameterSymlinkApp writes a chart at charts/demo whose file
// parameters read links/chain.txt, a symlink chain to links/target.txt, and,
// when withDirLink is set, links/dir-link/inner.txt through the symlinked
// directory links/dir-link. It returns the Application.
func writeHelmFileParameterSymlinkApp(t *testing.T, root string, withDirLink bool) argoappv1.Application {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "charts", "demo", "Chart.yaml"), "apiVersion: v2\nname: demo\nversion: 0.1.0\n")
	writeTestFile(t, filepath.Join(root, "charts", "demo", "values.yaml"), "chain: unset\ninner: unset\n")
	writeTestFile(t, filepath.Join(root, "charts", "demo", "templates", "cm.yaml"),
		"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\ndata:\n  chain: {{ .Values.chain | quote }}\n  inner: {{ .Values.inner | quote }}\n")
	writeTestFile(t, filepath.Join(root, "links", "target.txt"), "from-target\n")
	writeTestFile(t, filepath.Join(root, "unrelated", "README.md"), "not a render input\n")
	symlinkBoundsLink(t, "target.txt", filepath.Join(root, "links", "file-link.txt"))
	symlinkBoundsLink(t, "file-link.txt", filepath.Join(root, "links", "chain.txt"))
	parameters := []argoappv1.HelmFileParameter{{Name: "chain", Path: "../../links/chain.txt"}}
	if withDirLink {
		writeTestFile(t, filepath.Join(root, "links", "real", "inner.txt"), "from-inner\n")
		symlinkBoundsLink(t, "real", filepath.Join(root, "links", "dir-link"))
		parameters = append(parameters, argoappv1.HelmFileParameter{Name: "inner", Path: "../../links/dir-link/inner.txt"})
	}
	return argoappv1.Application{
		Name: "demo", Namespace: "argocd",
		Spec: argoappv1.ApplicationSpec{
			Source: &argoappv1.ApplicationSource{
				RepoURL: "https://git.example.test/org/repo.git", Path: "charts/demo", TargetRevision: "main",
				Helm: &argoappv1.ApplicationSourceHelm{FileParameters: parameters},
			},
			Destination: argoappv1.ApplicationDestination{Namespace: "default"},
		},
	}
}

// TestRenderInputCoverageHelmFileParameterSymlinks pins that the persistent
// render cache digest covers the file that a file parameter reads through a
// symlink chain outside the chart. The coverage probe writes through each
// symlink, so the fixture holds no symlink to a directory.
func TestRenderInputCoverageHelmFileParameterSymlinks(t *testing.T) {
	root := t.TempDir()
	application := writeHelmFileParameterSymlinkApp(t, root, false)
	assertRenderInputCoverage(t, root, application)
}

// TestWithSelectionOnlyPathsOwnsHelmFileParameterSymlinks pins that
// changed-only selection owns the symlinks that a file parameter path passes
// through and the file it reads, also through a symlinked directory.
func TestWithSelectionOnlyPathsOwnsHelmFileParameterSymlinks(t *testing.T) {
	root := t.TempDir()
	application := writeHelmFileParameterSymlinkApp(t, root, true)
	got := withSelectionOnlyPaths(context.Background(), root, nil, selfRepoRefs{}, sourceTypeGates{}, []ApplicationSelectionInput{{Application: application}}).inputs
	for _, changed := range []string{"links/target.txt", "links/file-link.txt", "links/chain.txt", "links/dir-link", "links/real/inner.txt"} {
		if selected, _ := SelectChangedApplicationInputs(got, []string{changed}); len(selected) != 1 {
			t.Errorf("change to %s selected %d Applications, want the Helm source (paths %v)", changed, len(selected), got[0].Paths)
		}
	}
	if selected, _ := SelectChangedApplicationInputs(got, []string{"unrelated/README.md"}); len(selected) != 0 {
		t.Errorf("an unrelated change selected the Helm source")
	}
}

// TestWithSelectionOnlyPathsOwnsOverrideFileInputs pins that changed-only
// selection reads a source with its .argocd-source*.yaml files merged in,
// as the render and the persistent cache key do. The Helm Application sets
// its value files and file parameters only in its per-Application override
// file, and the Kustomize Application adds a component only in
// .argocd-source.yaml.
func TestWithSelectionOnlyPathsOwnsOverrideFileInputs(t *testing.T) {
	root := t.TempDir()
	helmApp := writeHelmFileParameterSymlinkApp(t, root, true)
	helmApp.Spec.Source.Helm = nil
	writeTestFile(t, filepath.Join(root, "values", "extra.yaml"), "chain: from-values\n")
	writeTestFile(t, filepath.Join(root, "charts", "demo", ".argocd-source-demo.yaml"), `helm:
  valueFiles:
    - ../../values/extra.yaml
  fileParameters:
    - name: inner
      path: ../../links/dir-link/inner.txt
`)
	writeTestFile(t, filepath.Join(root, "kustomize", "app", "kustomization.yaml"), "resources:\n  - cm.yaml\n")
	writeTestFile(t, filepath.Join(root, "kustomize", "app", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: kustomize\n")
	writeTestFile(t, filepath.Join(root, "kustomize", "app", ".argocd-source.yaml"), "kustomize:\n  components:\n    - ../component\n")
	writeTestFile(t, filepath.Join(root, "kustomize", "component", "kustomization.yaml"), "apiVersion: kustomize.config.k8s.io/v1alpha1\nkind: Component\n")
	kustomizeApp := argoappv1.Application{
		Name: "kustomize", Namespace: "argocd",
		Spec: argoappv1.ApplicationSpec{
			Source:      &argoappv1.ApplicationSource{RepoURL: "https://git.example.test/org/repo.git", Path: "kustomize/app", TargetRevision: "main"},
			Destination: argoappv1.ApplicationDestination{Namespace: "default"},
		},
	}
	got := withSelectionOnlyPaths(context.Background(), root, nil, selfRepoRefs{}, sourceTypeGates{}, []ApplicationSelectionInput{{Application: helmApp}, {Application: kustomizeApp}}).inputs
	for changed, want := range map[string]string{
		"values/extra.yaml":                      "demo",
		"links/real/inner.txt":                   "demo",
		"links/dir-link":                         "demo",
		"kustomize/component/kustomization.yaml": "kustomize",
	} {
		selected, _ := SelectChangedApplicationInputs(got, []string{changed})
		if len(selected) != 1 || selected[0].Name != want {
			t.Errorf("change to %s selected %d Applications, want %s (paths %v and %v)", changed, len(selected), want, got[0].Paths, got[1].Paths)
		}
	}
}

// TestWithSelectionOnlyPathsOwnsSmokeOverrideFileParameters pins the case
// of the parity smoke: parity-helm-file-parameter-symlinks-override reads
// file-parameter-links/targets/message.txt only through the file parameters
// of its override file.
func TestWithSelectionOnlyPathsOwnsSmokeOverrideFileParameters(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "testdata", "argocd-parity", "repo"))
	if err != nil {
		t.Fatal(err)
	}
	application := argoappv1.Application{
		Name: "parity-helm-file-parameter-symlinks-override", Namespace: "argocd",
		Spec: argoappv1.ApplicationSpec{
			Source: &argoappv1.ApplicationSource{
				RepoURL: "git://argocd-parity-git.argocd-parity.svc.cluster.local/repo.git", Path: "charts/helm-file-parameter-symlinks", TargetRevision: "HEAD",
			},
			Destination: argoappv1.ApplicationDestination{Name: "in-cluster", Namespace: "parity-helm-file-parameter-symlinks-override"},
		},
	}
	got := withSelectionOnlyPaths(context.Background(), root, nil, selfRepoRefs{}, sourceTypeGates{}, []ApplicationSelectionInput{{Application: application}}).inputs
	for _, changed := range []string{"file-parameter-links/targets/message.txt", "file-parameter-links/targets/inner.txt", "file-parameter-links/chain.txt"} {
		if selected, _ := SelectChangedApplicationInputs(got, []string{changed}); len(selected) != 1 {
			t.Errorf("change to %s selected %d Applications, want 1 (paths %v)", changed, len(selected), got[0].Paths)
		}
	}
}

// TestRenderInputCoverageHelmFileParameterSymlinkRetarget pins that the
// persistent render cache digest covers each symlink of a file parameter
// chain: pointing one of them at another file changes the render, so the
// symlink itself must be a digest input.
func TestRenderInputCoverageHelmFileParameterSymlinkRetarget(t *testing.T) {
	for _, link := range []string{"links/chain.txt", "links/file-link.txt"} {
		t.Run(link, func(t *testing.T) {
			root := t.TempDir()
			application := writeHelmFileParameterSymlinkApp(t, root, false)
			writeTestFile(t, filepath.Join(root, "links", "other.txt"), "from-other\n")
			provider := renderCoverageFixtureProvider(root)
			plan := mustPlan(t, application)
			prepared, _, err := preparePlanSourcesForRender(context.Background(), application, provider, plan)
			if err != nil {
				t.Fatalf("preparePlanSourcesForRender() error = %v", err)
			}
			digestPaths, _, err := localInputDigestPathsForSource(context.Background(), prepared, prepared.Sources[0], provider)
			if err != nil {
				t.Fatalf("localInputDigestPathsForSource() error = %v", err)
			}
			baseline, err := RenderApplication(context.Background(), application, provider)
			if err != nil {
				t.Fatalf("baseline render error = %v", err)
			}
			if err := os.Remove(filepath.Join(root, filepath.FromSlash(link))); err != nil {
				t.Fatal(err)
			}
			symlinkBoundsLink(t, "other.txt", filepath.Join(root, filepath.FromSlash(link)))
			retargeted, err := RenderApplication(context.Background(), application, renderCoverageFixtureProvider(root))
			if err != nil {
				t.Fatalf("retargeted render error = %v", err)
			}
			if renderOutcomeSignature(t, baseline, nil) == renderOutcomeSignature(t, retargeted, nil) {
				t.Fatalf("retargeting %s did not change the render", link)
			}
			if !digestPathsCover(digestPaths, link) {
				t.Fatalf("retargeting %s changes the render but the digest paths %#v do not cover it", link, digestPaths)
			}
		})
	}
}

// TestWithSelectionOnlyPathsReadsTheInstanceNameOverride pins that
// selection reads the per-Application override file of an Application
// outside the controller namespace by its instance name,
// .argocd-source-<namespace>_<name>.yaml, and not the bare-name file, as the
// repo-server does.
func TestWithSelectionOnlyPathsReadsTheInstanceNameOverride(t *testing.T) {
	root := t.TempDir()
	application := writeHelmFileParameterSymlinkApp(t, root, false)
	application.Namespace = "tenant"
	application.Spec.Source.Helm = nil
	writeTestFile(t, filepath.Join(root, "shared", "instance.txt"), "instance\n")
	writeTestFile(t, filepath.Join(root, "shared", "bare.txt"), "bare\n")
	writeTestFile(t, filepath.Join(root, "charts", "demo", ".argocd-source-tenant_demo.yaml"), "helm:\n  fileParameters:\n    - name: chain\n      path: ../../shared/instance.txt\n")
	writeTestFile(t, filepath.Join(root, "charts", "demo", ".argocd-source-demo.yaml"), "helm:\n  fileParameters:\n    - name: chain\n      path: ../../shared/bare.txt\n")
	got := withSelectionOnlyPaths(context.Background(), root, nil, selfRepoRefs{}, sourceTypeGates{}, []ApplicationSelectionInput{{Application: application}}).inputs
	if selected, _ := SelectChangedApplicationInputs(got, []string{"shared/instance.txt"}); len(selected) != 1 {
		t.Errorf("change to shared/instance.txt selected %d Applications, want 1 (paths %v)", len(selected), got[0].Paths)
	}
	if selected, _ := SelectChangedApplicationInputs(got, []string{"shared/bare.txt"}); len(selected) != 0 {
		t.Errorf("change to shared/bare.txt selected the Application, but the bare-name override is not read (paths %v)", got[0].Paths)
	}
}

// TestWithSelectionOnlyPathsOwnsTheFileHelmReads pins that selection owns
// the file that helm --set-file reads after it drops a backslash:
// ../../shared/a\b.txt reads shared/ab.txt.
func TestWithSelectionOnlyPathsOwnsTheFileHelmReads(t *testing.T) {
	root := t.TempDir()
	application := writeHelmFileParameterSymlinkApp(t, root, false)
	writeTestFile(t, filepath.Join(root, "shared", "ab.txt"), "without-backslash\n")
	application.Spec.Source.Helm.FileParameters = []argoappv1.HelmFileParameter{{Name: "chain", Path: `../../shared/a\b.txt`}}
	got := withSelectionOnlyPaths(context.Background(), root, nil, selfRepoRefs{}, sourceTypeGates{}, []ApplicationSelectionInput{{Application: application}}).inputs
	if selected, _ := SelectChangedApplicationInputs(got, []string{"shared/ab.txt"}); len(selected) != 1 {
		t.Errorf("change to shared/ab.txt selected %d Applications, want 1 (paths %v)", len(selected), got[0].Paths)
	}
}
