package render

import (
	"context"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
)

// Kustomize reads every path in a kustomization exactly as written: "base "
// names the directory "base ", not "base". These tests pin that drydock
// checks, copies and digests that same name. A check of the trimmed name
// validates a file kustomize never reads, so a padded ref naming a symlink
// to an outside directory, or a padded directory holding a remote ref,
// used to pass validation and render outside content — under the default
// LoadRestrictionsRootOnly too, which confines file loads but neither
// kustomization directories nor http(s) fetches. Every escaping row points
// at a sibling temporary directory holding a marker, and every remote row
// at a local test server that counts requests.

// paddedDirField is a kustomization field whose ref names a kustomization
// directory.
type paddedDirField struct {
	name string
	// component reports whether the field takes a Component.
	component bool
	// kustomization returns the listing kustomization naming ref.
	kustomization func(ref string) string
}

var paddedDirFields = []paddedDirField{
	{name: "resources", kustomization: func(ref string) string {
		return "resources:\n  - cm.yaml\n  - " + strconv.Quote(ref) + "\n"
	}},
	{name: "bases", kustomization: func(ref string) string {
		return "resources:\n  - cm.yaml\nbases:\n  - " + strconv.Quote(ref) + "\n"
	}},
	{name: "components", component: true, kustomization: func(ref string) string {
		return "resources:\n  - cm.yaml\ncomponents:\n  - " + strconv.Quote(ref) + "\n"
	}},
}

// header returns the kind header of a directory the field
// names.
func (f paddedDirField) header() string {
	if f.component {
		return "apiVersion: kustomize.config.k8s.io/v1alpha1\nkind: Component\n"
	}
	return ""
}

// paddedJSONPatch adds data.leaked carrying marker.
func paddedJSONPatch(marker string) string {
	return "- op: add\n  path: /data/leaked\n  value: " + marker + "\n"
}

// writePaddedDirTarget writes, at dir, a kustomization directory of the
// field's kind whose ConfigMap carries marker.
func writePaddedDirTarget(t *testing.T, field paddedDirField, dir, name, marker string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "kustomization.yaml"), field.header()+"resources:\n  - cm.yaml\n")
	writeFile(t, filepath.Join(dir, "cm.yaml"), decodeTestConfigMap(name, marker))
}

func (f referentFixture) renderWith(prepared bool, opts RenderOptions) ([]Manifest, error) {
	if prepared {
		opts.Kustomize = &argoappv1.ApplicationSourceKustomize{NamePrefix: "p-"}
	}
	manifests, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: f.root, Path: "apps/demo"}, opts)
	return manifests, err
}

// TestKustomizePaddedDirectoryRefsCannotEscapeRepository covers the fields
// naming a kustomization directory, under both load restrictors and on
// both render paths: a padded name that is a symlink to an outside
// directory (also a name of spaces alone), a padded directory whose
// kustomization lists an http resource, and a padded directory whose
// kustomization lists a transformer reading an http patch. Each fails
// before anything outside is read or fetched.
func TestKustomizePaddedDirectoryRefsCannotEscapeRepository(t *testing.T) {
	escapes := []struct{ name, wantErr string }{
		{name: "symlink", wantErr: "includes symlink component"},
		{name: "blank symlink", wantErr: "includes symlink component"},
		{name: "remote resource", wantErr: "offline cache miss"},
		{name: "remote transformer referent", wantErr: "is a remote ref"},
	}
	for _, field := range paddedDirFields {
		for _, escape := range escapes {
			for _, restrictor := range referentRestrictors {
				for _, prepared := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/%s/prepared=%v", field.name, escape.name, restrictor.name, prepared), func(t *testing.T) {
						fixture := newReferentFixture(t)
						_, requests := referentServer(t, "")
						opts := RenderOptions{BuildOptions: restrictor.options, OfflineRemoteResources: true, RemoteResourceCacheDir: t.TempDir()}
						var ref string
						switch escape.name {
						case "symlink", "blank symlink":
							target := filepath.Join(fixture.outside, "target")
							writePaddedDirTarget(t, field, target, "outside", referentOutsideMarker)
							ref = "linked "
							if escape.name == "blank symlink" {
								ref = " "
							}
							symlink(t, target, filepath.Join(fixture.listing, ref))
						case "remote resource":
							var server *httptest.Server
							server, requests = referentServer(t, decodeTestConfigMap("outside", referentOutsideMarker))
							ref = "remote "
							writeFile(t, filepath.Join(fixture.listing, ref, "kustomization.yaml"), field.header()+"resources:\n  - "+server.URL+"/cm.yaml\n")
						case "remote transformer referent":
							var server *httptest.Server
							server, requests = referentServer(t, paddedJSONPatch(referentOutsideMarker))
							ref = "xf "
							writeFile(t, filepath.Join(fixture.listing, ref, "kustomization.yaml"), field.header()+"resources:\n  - sub.yaml\ntransformers:\n  - config.yaml\n")
							writeFile(t, filepath.Join(fixture.listing, ref, "sub.yaml"), decodeTestConfigMap("sub", "safe"))
							writeFile(t, filepath.Join(fixture.listing, ref, "config.yaml"), "apiVersion: builtin\nkind: PatchTransformer\nmetadata:\n  name: remote\npath: "+server.URL+"/patch.yaml\ntarget:\n  kind: ConfigMap\n")
						}
						writeFile(t, filepath.Join(fixture.listing, "kustomization.yaml"), field.kustomization(ref))

						manifests, err := fixture.renderWith(prepared, opts)
						assertNoRequests(t, requests, err)
						assertOutsideNotRead(t, manifests, err)
						if err == nil || !strings.Contains(err.Error(), escape.wantErr) {
							t.Fatalf("Render() error = %v, want error containing %q", err, escape.wantErr)
						}
					})
				}
			}
		}
	}
}

// paddedFileField is a kustomization field whose ref names a file.
type paddedFileField struct {
	name string
	// file is the base name of the file the ref names.
	file string
	// content returns the file's content carrying marker.
	content func(marker string) string
	// kustomization returns the listing kustomization naming ref.
	kustomization func(ref string) string
}

var paddedFileFields = []paddedFileField{
	{
		name: "resources file", file: "extra.yaml",
		content: func(marker string) string { return decodeTestConfigMap("extra", marker) },
		kustomization: func(ref string) string {
			return "resources:\n  - cm.yaml\n  - " + strconv.Quote(ref) + "\n"
		},
	},
	{
		name: "patches.path", file: "patch.yaml",
		content: paddedJSONPatch,
		kustomization: func(ref string) string {
			return "resources:\n  - cm.yaml\npatches:\n  - path: " + strconv.Quote(ref) + "\n    target:\n      kind: ConfigMap\n      name: demo\n"
		},
	},
	{
		name: "patchesStrategicMerge", file: "smp.yaml",
		content: referentConfigMapPatch,
		kustomization: func(ref string) string {
			return "resources:\n  - cm.yaml\npatchesStrategicMerge:\n  - " + strconv.Quote(ref) + "\n"
		},
	},
	{
		name: "configMapGenerator.files", file: "data.txt",
		content: func(marker string) string { return marker },
		kustomization: func(ref string) string {
			// A key= prefix keeps the ConfigMap key valid; the path after it
			// is read as written.
			return "resources:\n  - cm.yaml\nconfigMapGenerator:\n  - name: generated\n    options:\n      disableNameSuffixHash: true\n    files:\n      - " + strconv.Quote("data="+ref) + "\n"
		},
	},
	{
		name: "configMapGenerator.envs", file: "vars.env",
		content: func(marker string) string { return "leaked=" + marker + "\n" },
		kustomization: func(ref string) string {
			return "resources:\n  - cm.yaml\nconfigMapGenerator:\n  - name: generated\n    options:\n      disableNameSuffixHash: true\n    envs:\n      - " + strconv.Quote(ref) + "\n"
		},
	},
}

// TestKustomizePaddedFileRefsCannotEscapeRepository covers the fields
// naming a file under LoadRestrictionsNone (RootOnly already confines
// kustomize's file loads) on both render paths: a padded name that is a
// symlink to an outside file. It also covers a patchesStrategicMerge entry
// ending in a newline: kustomize reads it as a path because its resource
// factory rejects it as patch content, so a check that took every
// multi-line entry for inline content skipped it.
func TestKustomizePaddedFileRefsCannotEscapeRepository(t *testing.T) {
	for _, field := range paddedFileFields {
		for _, prepared := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/symlink/prepared=%v", field.name, prepared), func(t *testing.T) {
				fixture := newReferentFixture(t)
				target := filepath.Join(fixture.outside, field.file)
				writeFile(t, target, field.content(referentOutsideMarker))
				ref := "linked-" + field.file + " "
				symlink(t, target, filepath.Join(fixture.listing, ref))
				writeFile(t, filepath.Join(fixture.listing, "kustomization.yaml"), field.kustomization(ref))

				manifests, err := fixture.render(prepared, referentLoadRestrictionsNone)
				assertOutsideNotRead(t, manifests, err)
				if err == nil || !strings.Contains(err.Error(), "includes symlink component") {
					t.Fatalf("Render() error = %v, want symlink error", err)
				}
			})
		}
	}
	for _, prepared := range []bool{false, true} {
		t.Run(fmt.Sprintf("patchesStrategicMerge/trailing newline/prepared=%v", prepared), func(t *testing.T) {
			fixture := newReferentFixture(t)
			writeFile(t, filepath.Join(fixture.outside, "smp.yaml\n"), referentConfigMapPatch(referentOutsideMarker))
			ref := strings.Repeat("../", 64) + strings.TrimPrefix(filepath.ToSlash(filepath.Join(fixture.outside, "smp.yaml")), "/") + "\n"
			writeFile(t, filepath.Join(fixture.listing, "kustomization.yaml"), "resources:\n  - cm.yaml\npatchesStrategicMerge:\n  - "+strconv.Quote(ref)+"\n")

			manifests, err := fixture.render(prepared, referentLoadRestrictionsNone)
			assertOutsideNotRead(t, manifests, err)
			if err == nil || !strings.Contains(err.Error(), "escapes repository root") {
				t.Fatalf("Render() error = %v, want escape error", err)
			}
		})
	}
}

// paddedInRepoCase is an in-repository file or directory whose name really
// ends in a space, next to a decoy with the trimmed name.
type paddedInRepoCase struct {
	name string
	// write writes the padded target (inside marker) and the decoy (decoy
	// marker) under base, and returns the listing kustomization naming the
	// padded target by ref.
	write func(t *testing.T, base, ref string) string
	// target is the padded target's name; dir marks a directory.
	target string
	dir    bool
	// chart adds a local Helm chart rendering .Values.value.
	chart bool
}

const paddedDecoyMarker = "decoy-marker"

func paddedInRepoCases() []paddedInRepoCase {
	var cases []paddedInRepoCase
	for _, field := range paddedDirFields {
		cases = append(cases, paddedInRepoCase{
			name: field.name, target: "base ", dir: true,
			write: func(t *testing.T, base, ref string) string {
				writePaddedDirTarget(t, field, filepath.Join(base, "base "), "padded", referentInsideMarker)
				writePaddedDirTarget(t, field, filepath.Join(base, "base"), "padded", paddedDecoyMarker)
				return field.kustomization(ref)
			},
		})
	}
	for _, field := range paddedFileFields {
		cases = append(cases, paddedInRepoCase{
			name: field.name, target: field.file + " ",
			write: func(t *testing.T, base, ref string) string {
				writeFile(t, filepath.Join(base, field.file+" "), field.content(referentInsideMarker))
				writeFile(t, filepath.Join(base, field.file), field.content(paddedDecoyMarker))
				return field.kustomization(ref)
			},
		})
	}
	return append(cases, paddedInRepoCase{
		name: "helmCharts.valuesFile", target: "values.yaml ", chart: true,
		write: func(t *testing.T, base, ref string) string {
			writeFile(t, filepath.Join(base, "values.yaml "), "value: "+referentInsideMarker+"\n")
			writeFile(t, filepath.Join(base, "values.yaml"), "value: "+paddedDecoyMarker+"\n")
			return "helmCharts:\n  - name: chart\n    releaseName: demo\n    valuesFile: " + strconv.Quote(ref) + "\n"
		},
	})
}

// paddedInRepoRuns reports whether a case runs at location under the
// restrictor. A directory outside the listing directory is walked and
// copied as a graph node, which the listing location covers; RootOnly
// confines kustomize's file loads, helmCharts values files included, to the
// listing directory.
func paddedInRepoRuns(tc paddedInRepoCase, location string, rootOnly bool) bool {
	if location != "shared" {
		return true
	}
	return !tc.dir && !rootOnly
}

// TestKustomizePaddedRefsNameTheFilesKustomizeReads pins the legitimate
// use: a padded ref naming an in-repository file or directory whose name
// really has the space. Next to it sits a decoy with the trimmed name and
// other content. Render reads the padded one on both paths — the prepared
// workspace copies it, also from outside the listing directory — and the
// digest and changed-only selection name it, not the decoy, so editing it
// rotates the cache key.
func TestKustomizePaddedRefsNameTheFilesKustomizeReads(t *testing.T) {
	for _, tc := range paddedInRepoCases() {
		for _, location := range []string{"listing", "shared"} {
			for _, restrictor := range referentRestrictors {
				if !paddedInRepoRuns(tc, location, restrictor.options == nil) {
					continue
				}
				for _, prepared := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/%s/prepared=%v", tc.name, location, restrictor.name, prepared), func(t *testing.T) {
						fixture := newReferentFixture(t)
						base, ref, relBase := fixture.listing, tc.target, "apps/demo"
						if location == "shared" {
							base, ref, relBase = filepath.Join(fixture.root, "shared"), "../../shared/"+tc.target, "shared"
						}
						writeFile(t, filepath.Join(fixture.listing, "kustomization.yaml"), tc.write(t, base, ref))
						if tc.chart {
							writeValueChart(t, filepath.Join(fixture.listing, "charts", "chart"))
						}

						manifests, err := fixture.render(prepared, restrictor.options)
						if err != nil {
							t.Fatalf("Render() error = %v", err)
						}
						if !renderedContains(t, manifests, referentInsideMarker) || renderedContains(t, manifests, paddedDecoyMarker) {
							t.Fatalf("Render() did not read exactly the padded target: %#v", manifests)
						}
						assertPaddedInputsNamed(t, fixture.root, relBase+"/"+tc.target, relBase+"/"+strings.TrimSpace(tc.target))
					})
				}
			}
		}
	}
}

// assertPaddedInputsNamed fails unless the digest and changed-only
// selection of apps/demo name padded and not decoy.
func assertPaddedInputsNamed(t *testing.T, root, padded, decoy string) {
	t.Helper()
	digest, err := KustomizeInputDigestPaths(context.Background(), ResolvedSource{RepoRoot: root, Path: "apps/demo"}, RenderOptions{})
	if err != nil {
		t.Fatalf("KustomizeInputDigestPaths() error = %v", err)
	}
	digestPaths := make([]string, 0, len(digest))
	for _, path := range digest {
		digestPaths = append(digestPaths, path.Path)
	}
	selection, err := KustomizeSelectionPaths(context.Background(), root, "apps/demo", nil)
	if err != nil {
		t.Fatalf("KustomizeSelectionPaths() error = %v", err)
	}
	for name, paths := range map[string][]string{"digest": digestPaths, "selection": selection} {
		if !slices.Contains(paths, padded) || slices.Contains(paths, decoy) {
			t.Fatalf("%s paths = %q, want %q and not %q", name, paths, padded, decoy)
		}
	}
}
