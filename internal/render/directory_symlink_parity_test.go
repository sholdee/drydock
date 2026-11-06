package render

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/argoproj/argo-cd/v3/util/io/files"
	"github.com/google/go-jsonnet"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// upstreamConfinedImporter is confinedImporter from Argo CD v3.6.1
// reposerver/repository/repository.go:2330-2397.
type upstreamConfinedImporter struct {
	*jsonnet.FileImporter
	repoRoot string
}

func (i *upstreamConfinedImporter) Import(importedFrom, importedPath string) (jsonnet.Contents, string, error) {
	dir, _ := filepath.Split(importedFrom)
	foundAt, err := i.locate(dir, importedPath)
	if err != nil {
		return jsonnet.Contents{}, "", err
	}
	if err := i.validate(foundAt, importedPath); err != nil {
		return jsonnet.Contents{}, "", err
	}
	return i.FileImporter.Import(importedFrom, importedPath)
}

func (i *upstreamConfinedImporter) locate(dir, importedPath string) (string, error) {
	if filepath.IsAbs(importedPath) {
		if _, err := os.Lstat(importedPath); err != nil {
			if os.IsNotExist(err) {
				return "", fmt.Errorf("couldn't open import %q: no match locally or in the Jsonnet library paths", importedPath)
			}
			return "", err
		}
		return importedPath, nil
	}
	bases := make([]string, 0, len(i.JPaths)+1)
	bases = append(bases, dir)
	for _, jpath := range slices.Backward(i.JPaths) {
		bases = append(bases, jpath)
	}
	for _, base := range bases {
		absPath := filepath.Join(base, importedPath)
		if _, err := os.Lstat(absPath); err == nil {
			return absPath, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
	}
	return "", fmt.Errorf("couldn't open import %q: no match locally or in the Jsonnet library paths", importedPath)
}

func (i *upstreamConfinedImporter) validate(foundAt, importedPath string) error {
	resolved, ok, err := files.ResolveInbound(foundAt, i.repoRoot)
	if err != nil {
		return fmt.Errorf("failed to resolve jsonnet import %q: %w", importedPath, err)
	}
	if !ok {
		return fmt.Errorf("jsonnet import %q resolves outside repository root", importedPath)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return fmt.Errorf("failed to stat jsonnet import %q: %w", importedPath, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("jsonnet import %q is not a regular file", importedPath)
	}
	return nil
}

// upstreamFindManifestsWithOptions is findManifests (Argo CD v3.6.1
// reposerver/repository/repository.go:2005-2051) with the directory
// options, a Jsonnet VM whose library path is appPath and whose importer is
// the confined importer (makeJsonnetVM, repository.go:2285-2323), and the
// size limit of the pre-pass. repoRoot must have no symlinks in it, as the
// repo-server's checkout root has none.
func upstreamFindManifestsWithOptions(appPath, repoRoot string, recurse bool, include, exclude string, limit int64) ([]*unstructured.Unstructured, error) {
	manifests, err := upstreamGetPotentiallyValidManifests(appPath, repoRoot, recurse, false, include, exclude, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to get potentially valid manifests: %w", err)
	}
	var objs []*unstructured.Unstructured
	for _, manifest := range manifests {
		name := manifest.fileInfo.Name()
		if strings.HasSuffix(name, ".jsonnet") {
			vm := jsonnet.MakeVM()
			vm.Importer(&upstreamConfinedImporter{
				FileImporter: &jsonnet.FileImporter{JPaths: []string{appPath}},
				repoRoot:     repoRoot,
			})
			jsonStr, err := vm.EvaluateFile(manifest.path)
			if err != nil {
				return nil, fmt.Errorf("Failed to evaluate jsonnet %q: %w", name, err)
			}
			fileObjs, err := upstreamJsonnetObjects(jsonStr, name)
			if err != nil {
				return nil, err
			}
			objs = append(objs, fileObjs...)
			continue
		}
		data, err := os.ReadFile(manifest.path)
		if err != nil {
			return nil, err
		}
		fileObjs, err := upstreamGetObjsFromYAMLOrJSON(data, name)
		if err != nil {
			return nil, err
		}
		objs = append(objs, fileObjs...)
	}
	return objs, nil
}

func symlinkParityConfigMap(name string) string {
	return "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: " + name + "\n"
}

// writeSymlinkParityTree builds the repository that every symlink parity
// case starts from. The source directory is apps. Each symlink in it points
// into targets, outside the source directory.
func writeSymlinkParityTree(t *testing.T, root string) {
	t.Helper()
	writeFile(t, filepath.Join(root, "apps", "plain.yaml"), symlinkParityConfigMap("plain"))
	writeFile(t, filepath.Join(root, "targets", "linked-target.yaml"), symlinkParityConfigMap("linked"))
	writeFile(t, filepath.Join(root, "targets", "excluded-target.yaml"), symlinkParityConfigMap("excluded"))
	writeFile(t, filepath.Join(root, "targets", "filtered-target.yaml"), symlinkParityConfigMap("filtered"))
	writeFile(t, filepath.Join(root, "targets", "marker-target.yaml"), "# +argocd:skip-file-rendering\n"+symlinkParityConfigMap("marker"))
	writeFile(t, filepath.Join(root, "targets", "dir", "in-dir.yaml"), symlinkParityConfigMap("in-dir"))
	writeFile(t, filepath.Join(root, "targets", "chain-end.yaml"), symlinkParityConfigMap("chain"))
	// A relative import in a file reached through a symlink resolves
	// against the directory of the symlink, so apps/lib.libsonnet wins over
	// targets/jsonnet/lib.libsonnet.
	writeFile(t, filepath.Join(root, "targets", "jsonnet", "main.jsonnet"), "local lib = import 'lib.libsonnet';\n{apiVersion: 'v1', kind: 'ConfigMap', metadata: {name: lib.name}}\n")
	writeFile(t, filepath.Join(root, "targets", "jsonnet", "lib.libsonnet"), "{name: 'jsonnet-target-dir'}\n")
	writeFile(t, filepath.Join(root, "apps", "lib.libsonnet"), "{name: 'jsonnet-link-dir'}\n")
	writeFile(t, filepath.Join(root, "targets", "jsonnet", "shared.libsonnet"), "{name: 'jsonnet-linked-import'}\n")
	writeFile(t, filepath.Join(root, "apps", "imports.jsonnet"), "local shared = import 'shared.libsonnet';\n{apiVersion: 'v1', kind: 'ConfigMap', metadata: {name: shared.name}}\n")

	symlink(t, "../targets/linked-target.yaml", filepath.Join(root, "apps", "linked.yaml"))
	symlink(t, "../targets/excluded-target.yaml", filepath.Join(root, "apps", "excluded.yaml"))
	symlink(t, "../targets/filtered-target.yaml", filepath.Join(root, "apps", "filtered.txt"))
	symlink(t, "../targets/missing.yaml", filepath.Join(root, "apps", "dangling.yaml"))
	symlink(t, "../targets/dir", filepath.Join(root, "apps", "dir.yaml"))
	symlink(t, "../targets/dir", filepath.Join(root, "apps", "linked-dir"))
	symlink(t, "../targets/marker-target.yaml", filepath.Join(root, "apps", "marker.yaml"))
	symlink(t, "chain-end.yaml", filepath.Join(root, "targets", "chain-mid.yaml"))
	symlink(t, "../targets/chain-mid.yaml", filepath.Join(root, "apps", "chain.yaml"))
	symlink(t, "../targets/jsonnet/main.jsonnet", filepath.Join(root, "apps", "linked.jsonnet"))
	symlink(t, "../targets/jsonnet/shared.libsonnet", filepath.Join(root, "apps", "shared.libsonnet"))
}

// TestDirectoryRendererSymlinksMatchUpstream renders directory sources that
// hold file symlinks with drydock and with copies of the Argo CD v3.6.1
// repo-server code, and requires the same objects or the same failure. The
// repo-server follows a file symlink whose target is a regular file inside
// the repository, filters it by its own name and path, counts the target
// size toward the limit, and evaluates a Jsonnet file through the symlink.
// It skips a symlink whose target is missing or is a directory, never walks
// into a symlinked directory, and fails on a symlink that leaves the
// repository or cannot be resolved.
//
//nolint:gocyclo // One table drives both sides.
func TestDirectoryRendererSymlinksMatchUpstream(t *testing.T) {
	for _, tc := range []struct {
		name         string
		recurse      bool
		include      string
		exclude      string
		limit        int64
		setup        func(t *testing.T, root string)
		wantNames    []string
		wantUpstream string
		wantDrydock  string
	}{
		{
			name:    "in-repository file symlinks render and are filtered by their own path",
			exclude: "excluded.yaml",
			wantNames: []string{
				"chain", "jsonnet-link-dir", "jsonnet-linked-import", "linked", "plain",
			},
		},
		{
			name:      "recurse never walks into a symlinked directory",
			recurse:   true,
			exclude:   "excluded.yaml",
			wantNames: []string{"chain", "jsonnet-link-dir", "jsonnet-linked-import", "linked", "plain"},
		},
		{
			name:      "include matches the symlink path, not the target path",
			include:   "linked*",
			wantNames: []string{"jsonnet-link-dir", "linked"},
		},
		{
			name:    "the target size counts toward the limit",
			exclude: "{excluded.yaml,chain.yaml}",
			// plain.yaml and linked.yaml fit only if the symlink counts as
			// its own short size.
			limit:        int64(len(symlinkParityConfigMap("plain")) + len(symlinkParityConfigMap("linked")) - 1),
			wantUpstream: "exceeded max combined manifest file size",
			wantDrydock:  "the directory source files are larger in sum than",
		},
		{
			name: "a symlink outside the repository fails even when exclude matches it",
			setup: func(t *testing.T, root string) {
				outside := filepath.Join(t.TempDir(), "outside.yaml")
				writeFile(t, outside, symlinkParityConfigMap("outside"))
				symlink(t, outside, filepath.Join(root, "apps", "zz-outside.yaml"))
			},
			exclude:      "zz-outside.yaml",
			wantUpstream: `illegal filepath in symlink at "zz-outside.yaml"`,
			wantDrydock:  `apps/zz-outside.yaml: illegal filepath in symlink at "zz-outside.yaml"`,
		},
		{
			name: "a symlink outside the repository that the extension filter drops is skipped",
			setup: func(t *testing.T, root string) {
				symlink(t, "/etc/hosts", filepath.Join(root, "apps", "zz-outside.txt"))
			},
			exclude:   "excluded.yaml",
			wantNames: []string{"chain", "jsonnet-link-dir", "jsonnet-linked-import", "linked", "plain"},
		},
		{
			name: "a symlink loop fails",
			setup: func(t *testing.T, root string) {
				symlink(t, "zz-loop.yaml", filepath.Join(root, "apps", "zz-loop.yaml"))
			},
			wantUpstream: `failed to evaluate symlink at "zz-loop.yaml"`,
			wantDrydock:  `apps/zz-loop.yaml: failed to evaluate symlink at "zz-loop.yaml"`,
		},
		{
			name: "a Jsonnet import through a symlink outside the repository fails",
			setup: func(t *testing.T, root string) {
				outside := filepath.Join(t.TempDir(), "outside.libsonnet")
				writeFile(t, outside, "{name: 'outside'}\n")
				if err := os.Remove(filepath.Join(root, "apps", "shared.libsonnet")); err != nil {
					t.Fatal(err)
				}
				symlink(t, outside, filepath.Join(root, "apps", "shared.libsonnet"))
			},
			wantUpstream: `jsonnet import "shared.libsonnet" resolves outside repository root`,
			wantDrydock:  `jsonnet import "shared.libsonnet" resolves outside repository root`,
		},
		{
			name: "a Jsonnet import of a symlink whose target is missing fails",
			setup: func(t *testing.T, root string) {
				if err := os.Remove(filepath.Join(root, "targets", "jsonnet", "shared.libsonnet")); err != nil {
					t.Fatal(err)
				}
			},
			wantUpstream: `failed to resolve jsonnet import "shared.libsonnet"`,
			wantDrydock:  `failed to resolve jsonnet import "shared.libsonnet"`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeSymlinkParityTree(t, root)
			if tc.setup != nil {
				tc.setup(t, root)
			}
			// The repo-server's checkout root has no symlinks in it. A test
			// temporary directory can, on macOS, so the upstream side gets
			// the resolved root and drydock the root as given.
			realRoot, err := filepath.EvalSymlinks(root)
			if err != nil {
				t.Fatal(err)
			}
			upstreamObjs, upstreamErr := upstreamFindManifestsWithOptions(filepath.Join(realRoot, "apps"), realRoot, tc.recurse, tc.include, tc.exclude, tc.limit)
			if upstreamErr == nil {
				upstreamObjs, upstreamErr = upstreamApplicationObjects(upstreamObjs, "parity", "default")
			}
			result, _, drydockErr := (DirectoryRenderer{}).Render(t.Context(), ResolvedSource{RepoRoot: root, Path: "apps"}, RenderOptions{
				DirectoryRecurse:                  tc.recurse,
				DirectoryInclude:                  tc.include,
				DirectoryExclude:                  tc.exclude,
				DirectoryMaxCombinedManifestsSize: tc.limit,
				TrackManifest:                     upstreamTrackManifest(v1alpha1.TrackingMethodAnnotation),
			})
			if tc.wantUpstream == "" {
				if upstreamErr != nil || drydockErr != nil {
					t.Fatalf("upstream error = %v, drydock error = %v, want success", upstreamErr, drydockErr)
				}
				var upstreamNames []string
				for _, obj := range upstreamObjs {
					upstreamNames = append(upstreamNames, obj.GetName())
				}
				sort.Strings(upstreamNames)
				got := directoryManifestNames(result)
				if strings.Join(upstreamNames, ",") != strings.Join(tc.wantNames, ",") {
					t.Fatalf("upstream rendered %v, want %v", upstreamNames, tc.wantNames)
				}
				if strings.Join(got, ",") != strings.Join(tc.wantNames, ",") {
					t.Fatalf("drydock rendered %v, want %v", got, tc.wantNames)
				}
				return
			}
			if upstreamErr == nil || !strings.Contains(upstreamErr.Error(), tc.wantUpstream) {
				t.Fatalf("upstream error = %v, want %q", upstreamErr, tc.wantUpstream)
			}
			if drydockErr == nil || !strings.Contains(drydockErr.Error(), tc.wantDrydock) {
				t.Fatalf("drydock error = %v, want %q", drydockErr, tc.wantDrydock)
			}
		})
	}
}

// TestDirectoryRendererRejectsSymlinkIntoGitDirectory pins a drydock rule
// on top of the repo-server's: a file symlink whose target is in a .git
// directory fails the source. The repo-server would read its own clone's
// metadata there, which drydock cannot reproduce, and a .git directory can
// hold credentials.
func TestDirectoryRendererRejectsSymlinkIntoGitDirectory(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git", "config.yaml"), symlinkParityConfigMap("git"))
	symlink(t, "../.git/config.yaml", filepath.Join(root, "apps", "git.yaml"))
	_, _, err := (DirectoryRenderer{}).Render(t.Context(), ResolvedSource{RepoRoot: root, Path: "apps"}, RenderOptions{})
	if err == nil || !strings.Contains(err.Error(), `apps/git.yaml: symlink at "git.yaml" points into a .git directory`) {
		t.Fatalf("Render() error = %v, want the .git symlink error", err)
	}
}
