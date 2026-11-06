package render

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"

	"github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	argoglob "github.com/argoproj/argo-cd/v3/util/glob"
	"github.com/argoproj/argo-cd/v3/util/io/files"
	"github.com/google/go-jsonnet"
	xunicode "golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// upstreamManifestFile is manifestFile from Argo CD v3.6.1
// reposerver/repository/repository.go:2003.
var upstreamManifestFile = regexp.MustCompile(`^.*\.(yaml|yml|json|jsonnet)$`)

// errUpstreamExceededMaxCombinedManifestFileSize is
// ErrExceededMaxCombinedManifestFileSize (repository.go:88).
var errUpstreamExceededMaxCombinedManifestFileSize = errors.New("exceeded max combined manifest file size")

type upstreamPotentiallyValidManifest struct {
	path     string
	fileInfo os.FileInfo
}

// upstreamGetPotentiallyValidManifestFile is getPotentiallyValidManifestFile
// from Argo CD v3.6.1 reposerver/repository/repository.go:2148-2216, with
// utfutil.ReadFile(path, utfutil.UTF8) replaced by the same byte order mark
// decode upstreamGetObjsFromYAMLOrJSON uses.
//
//nolint:gocyclo // A copy of upstream code keeps its shape.
func upstreamGetPotentiallyValidManifestFile(path string, f os.FileInfo, appPath, repoRoot, include, exclude string, disableExtensionFilter bool) (os.FileInfo, string, error) {
	relPath, err := filepath.Rel(appPath, path)
	if err != nil {
		return nil, "", fmt.Errorf("failed to get relative path of %q: %w", path, err)
	}
	if !disableExtensionFilter && !upstreamManifestFile.MatchString(f.Name()) {
		return nil, "", nil
	}
	relRealPath := relPath
	realFileInfo := f
	if files.IsSymlink(f) {
		realPath, ok, err := files.ResolveInbound(path, repoRoot)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, fmt.Sprintf("destination of symlink %q is missing", relPath), nil
			}
			return nil, "", fmt.Errorf("failed to evaluate symlink at %q: %w", relPath, err)
		}
		if !ok {
			return nil, "", fmt.Errorf("illegal filepath in symlink at %q", relPath)
		}
		realFileInfo, err = os.Stat(realPath)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, fmt.Sprintf("destination of symlink %q is missing at %q", relPath, realPath), nil
			}
			return nil, "", fmt.Errorf("failed to get file info for symlink at %q to %q: %w", relPath, realPath, err)
		}
		relRealPath, err = filepath.Rel(repoRoot, realPath)
		if err != nil {
			return nil, "", fmt.Errorf("failed to get relative path of %q: %w", realPath, err)
		}
	}
	if !realFileInfo.Mode().IsRegular() {
		return nil, fmt.Sprintf("ignoring symlink at %q to non-regular file %q", relPath, relRealPath), nil
	}
	if exclude != "" && argoglob.Match(exclude, relPath) {
		return nil, "", nil
	}
	if include != "" && !argoglob.Match(include, relPath) {
		return nil, "", nil
	}
	raw, rerr := os.ReadFile(path)
	if rerr != nil {
		return nil, "", fmt.Errorf("failed to read %q: %w", relPath, rerr)
	}
	out, _, err := transform.Bytes(xunicode.BOMOverride(xunicode.UTF8.NewDecoder()), raw)
	if err != nil {
		return nil, "", err
	}
	if bytes.Contains(out, []byte(argocdSkipFileRenderingMarker)) {
		return nil, "", nil
	}
	return realFileInfo, "", nil
}

// upstreamGetPotentiallyValidManifests is getPotentiallyValidManifests from
// Argo CD v3.6.1 reposerver/repository/repository.go:2225-2283 without the
// warning log.
func upstreamGetPotentiallyValidManifests(appPath, repoRoot string, recurse, disableExtensionFilter bool, include, exclude string, maxCombinedManifestFileSize int64) ([]upstreamPotentiallyValidManifest, error) {
	currentCombinedManifestFileSize := int64(0)
	var potentiallyValidManifests []upstreamPotentiallyValidManifest
	err := filepath.Walk(appPath, func(path string, f os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if f.IsDir() {
			if path != appPath && !recurse {
				return filepath.SkipDir
			}
			return nil
		}
		realFileInfo, _, err := upstreamGetPotentiallyValidManifestFile(path, f, appPath, repoRoot, include, exclude, disableExtensionFilter)
		if err != nil {
			return fmt.Errorf("invalid manifest file %q: %w", path, err)
		}
		if realFileInfo == nil {
			return nil
		}
		if !strings.HasSuffix(f.Name(), ".jsonnet") {
			currentCombinedManifestFileSize += realFileInfo.Size()
			if maxCombinedManifestFileSize != 0 && currentCombinedManifestFileSize > maxCombinedManifestFileSize {
				return errUpstreamExceededMaxCombinedManifestFileSize
			}
		}
		potentiallyValidManifests = append(potentiallyValidManifests, upstreamPotentiallyValidManifest{path: path, fileInfo: f})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return potentiallyValidManifests, nil
}

// upstreamFindManifestsOnDisk is findManifests (Argo CD v3.6.1
// reposerver/repository/repository.go:2005-2051) for a flat directory on
// disk, without extension-filter or include options. A Jsonnet file is
// evaluated with a plain go-jsonnet VM.
func upstreamFindManifestsOnDisk(appPath, repoRoot string, maxCombinedManifestFileSize int64) ([]*unstructured.Unstructured, error) {
	manifests, err := upstreamGetPotentiallyValidManifests(appPath, repoRoot, false, false, "", "", maxCombinedManifestFileSize)
	if err != nil {
		return nil, fmt.Errorf("failed to get potentially valid manifests: %w", err)
	}
	var objs []*unstructured.Unstructured
	for _, manifest := range manifests {
		name := manifest.fileInfo.Name()
		if strings.HasSuffix(name, ".jsonnet") {
			jsonStr, err := jsonnet.MakeVM().EvaluateFile(manifest.path)
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

// TestDirectoryRendererPrepassMatchesUpstream checks the repo-server's
// pre-pass over a directory source: it reads every candidate file, drops a
// file with the skip marker, skips a file that is not a regular file, and
// sums the file sizes against the limit before it decodes any file. So a
// read error or the size limit in a later file wins over a decode error in
// an earlier file.
//
//nolint:gocyclo // One table drives both sides.
func TestDirectoryRendererPrepassMatchesUpstream(t *testing.T) {
	const cm = "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cm\n"
	for _, tc := range []struct {
		name         string
		files        map[string]string
		limit        int64
		setup        func(t *testing.T, dir string)
		wantUpstream string
		wantDrydock  string
		wantNames    []string
	}{
		{
			name:         "size limit beats an earlier decode error",
			files:        map[string]string{"a.json": `{"apiVersion":"v1",`, "b.yaml": cm},
			limit:        40,
			wantUpstream: "exceeded max combined manifest file size",
			wantDrydock:  "apps/b.yaml: the directory source files are larger in sum than 40 bytes",
		},
		{
			name:      "sum equal to the limit passes",
			files:     map[string]string{"a.yaml": cm},
			limit:     int64(len(cm)),
			wantNames: []string{"cm"},
		},
		{
			name:         "sum one byte over the limit fails",
			files:        map[string]string{"a.yaml": cm},
			limit:        int64(len(cm)) - 1,
			wantUpstream: "exceeded max combined manifest file size",
			wantDrydock:  "apps/a.yaml: the directory source files are larger in sum",
		},
		{
			name:      "zero turns the limit off",
			files:     map[string]string{"a.yaml": cm},
			wantNames: []string{"cm"},
		},
		{
			name:      "a Jsonnet file does not count",
			files:     map[string]string{"a.jsonnet": `{apiVersion: "v1", kind: "ConfigMap", metadata: {name: "jsonnet"}, data: {pad: "` + strings.Repeat("x", 200) + `"}}`, "b.yaml": cm},
			limit:     int64(len(cm)),
			wantNames: []string{"cm", "jsonnet"},
		},
		{
			name:      "a file with the skip marker does not count",
			files:     map[string]string{"a.yaml": "# +argocd:skip-file-rendering\n" + strings.Repeat("#", 200) + "\n", "b.yaml": cm},
			limit:     int64(len(cm)),
			wantNames: []string{"cm"},
		},
		{
			name:  "the raw size counts, before the byte order mark is dropped",
			files: map[string]string{"a.yaml": "\xef\xbb\xbf" + cm},
			limit: int64(len(cm)) + 2,
			// The decoded text fits, but the raw file is one byte over.
			wantUpstream: "exceeded max combined manifest file size",
			wantDrydock:  "apps/a.yaml: the directory source files are larger in sum",
		},
		{
			name:  "a read error beats an earlier decode error",
			files: map[string]string{"a.json": `{"apiVersion":"v1",`, "b.yaml": cm},
			setup: func(t *testing.T, dir string) {
				if os.Geteuid() == 0 {
					t.Skip("root can read a file without read permission")
				}
				if err := os.Chmod(filepath.Join(dir, "b.yaml"), 0o000); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, "b.yaml"), 0o644) })
			},
			wantUpstream: `failed to read "b.yaml"`,
			wantDrydock:  "apps/b.yaml: read file failed",
		},
		{
			name:  "a named pipe is skipped",
			files: map[string]string{"b.yaml": cm},
			setup: func(t *testing.T, dir string) {
				if err := syscall.Mkfifo(filepath.Join(dir, "a.yaml"), 0o644); err != nil {
					t.Skipf("mkfifo: %v", err)
				}
			},
			wantNames: []string{"cm"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "apps")
			for name, content := range tc.files {
				writeFile(t, filepath.Join(dir, name), content)
			}
			if tc.setup != nil {
				tc.setup(t, dir)
			}
			upstreamObjs, upstreamErr := upstreamFindManifestsOnDisk(dir, root, tc.limit)
			if upstreamErr == nil {
				upstreamObjs, upstreamErr = upstreamApplicationObjects(upstreamObjs, "parity", "default")
			}
			result, _, drydockErr := (DirectoryRenderer{}).Render(t.Context(), ResolvedSource{RepoRoot: root, Path: "apps"}, RenderOptions{
				DirectoryMaxCombinedManifestsSize: tc.limit,
				TrackManifest:                     upstreamTrackManifest(v1alpha1.TrackingMethodAnnotation),
			})
			if tc.wantUpstream == "" {
				if upstreamErr != nil || drydockErr != nil {
					t.Fatalf("upstream error = %v, drydock error = %v, want success", upstreamErr, drydockErr)
				}
				got := directoryManifestNames(result)
				want := []string{}
				for _, obj := range upstreamObjs {
					want = append(want, obj.GetName())
				}
				if strings.Join(got, ",") != strings.Join(tc.wantNames, ",") || len(want) != len(got) {
					t.Fatalf("drydock rendered %v, upstream %d objects, want %v", got, len(want), tc.wantNames)
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
