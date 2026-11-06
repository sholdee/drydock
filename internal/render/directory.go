package render

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	argoglob "github.com/argoproj/argo-cd/v3/util/glob"
	imagev1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/manifestyaml"
	"github.com/sholdee/drydock/internal/pathsafety"
	xunicode "golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

type DirectoryRenderer struct{}

const (
	argocdSkipFileRenderingMarker = "+argocd:skip-file-rendering"
	drydockCacheMetadataDirName   = ".drydock-cache"

	// DirectoryExtensionFilterDisabledCode identifies the warning for a
	// directory source that sets disableExtensionFilter with neither include
	// nor exclude. Argo CD's repo-server logs the same condition as a warning
	// and renders every file (getPotentiallyValidManifests,
	// reposerver/repository/repository.go at v3.6.1).
	DirectoryExtensionFilterDisabledCode = "directory.extension-filter-disabled"
)

// directoryManifestFile is Argo CD's built-in directory extension filter
// (reposerver/repository/repository.go manifestFile at v3.6.1), matched
// case-sensitively against the file name: deploy.YAML is not a manifest.
var directoryManifestFile = regexp.MustCompile(`^.*\.(yaml|yml|json|jsonnet)$`)

// Render follows the three stages in which Argo CD v3.6.1 reads a directory
// source, and fails at the same stage as Argo CD:
//
//  1. The repo-server's findManifests (reposerver/repository/repository.go:
//     2005-2051) reads every file, in walk order, into one list of objects.
//     A file that fails here fails the source at once.
//  2. GenerateManifests (repository.go:1817-1852) then splits each object
//     that is a list into its items, drops a null list, and adds the
//     tracking metadata to each manifest it emits. A list item that is not
//     an object, or tracking metadata that cannot be set, fails the source
//     here, after every file was read.
//  3. The controller reads each emitted manifest back
//     (unmarshalManifests, controller/state.go:562-571) and fails on the
//     first one without a kind.
//
// So a file that fails in an earlier stage is reported before a file that
// fails in a later stage, whatever their order in the directory.
//
//nolint:gocyclo // The walk keeps file filtering, decoding, and path provenance in one pass.
func (DirectoryRenderer) Render(ctx context.Context, source ResolvedSource, opts RenderOptions) ([]Manifest, []diagnostic.Diagnostic, error) {
	root, err := sourceRoot(source)
	if err != nil {
		return nil, nil, err
	}
	diags := directoryRenderDiagnostics(opts)
	var objects []directoryObject

	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			if shouldSkipDirectoryCandidate(root, path, entry, opts) {
				return filepath.SkipDir
			}
			return nil
		}
		// disableExtensionFilter skips the built-in extension filter, leaving
		// include and exclude as the only filters (Argo CD v3.6.1
		// getPotentiallyValidManifestFile).
		if !opts.DirectoryDisableExtensionFilter && !isManifestFile(path) {
			return nil
		}
		if !directoryManifestIncluded(root, path, opts) {
			return nil
		}

		manifestPath, pathErr := relativeManifestPath(source.RepoRoot, path)
		if pathErr != nil {
			return pathErr
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		data, err = decodeDirectoryFileText(manifestPath, data)
		if err != nil {
			return err
		}
		if bytes.Contains(data, []byte(argocdSkipFileRenderingMarker)) {
			return nil
		}
		var fileObjects []directoryObject
		switch {
		case isJsonnetFile(path):
			fileObjects, err = renderJsonnetFile(root, source.RepoRoot, path, manifestPath, opts)
		case isJSONSuffixFile(path):
			fileObjects, err = directoryJSONFileObjects(manifestPath, data)
		default:
			fileObjects, err = directoryStreamObjects(manifestPath, data)
		}
		if err != nil {
			return err
		}
		objects = append(objects, fileObjects...)
		return nil
	})
	if err != nil {
		return nil, diags, err
	}
	targets, err := generateDirectoryManifests(objects, opts)
	if err != nil {
		return nil, diags, err
	}
	if err := checkDirectoryManifestKinds(targets); err != nil {
		return nil, diags, err
	}
	out := make([]Manifest, 0, len(targets))
	for _, target := range targets {
		out = append(out, target.manifest)
	}
	return out, diags, nil
}

// directoryObject is one object that the repo-server's findManifests reads
// from a directory file.
type directoryObject struct {
	path string
	// label names the object in errors, such as "apps/a.yaml document 1".
	label string
	// object is nil for a JSON null in the array that a Jsonnet file
	// returns. GenerateManifests skips it.
	object *unstructured.Unstructured
	// jsonKindless marks a ".json" object without a kind that the
	// repo-server kept because the content is also an OCI image manifest.
	jsonKindless bool
}

// directoryTarget is one manifest that GenerateManifests emits.
type directoryTarget struct {
	manifest     Manifest
	label        string
	listItem     bool
	jsonKindless bool
}

// generateDirectoryManifests is the manifest loop of GenerateManifests
// (reposerver/repository/repository.go:1817-1852 at v3.6.1). An object whose
// items field is an array is a list (Unstructured.IsList): it emits each item,
// and each item must be an object (Unstructured.EachListItem). An object whose
// items field is null and that has neither spec nor status emits nothing
// (isNullList). Every other object is emitted unchanged.
//
// The repo-server then adds the tracking metadata to each emitted manifest,
// and fails the source when it cannot. opts.TrackManifest does the same on a
// copy of each manifest, so that the source fails here, in the same order.
// The Application layer adds the tracking metadata it keeps later.
func generateDirectoryManifests(objects []directoryObject, opts RenderOptions) ([]directoryTarget, error) {
	var out []directoryTarget
	for _, obj := range objects {
		if obj.object == nil {
			continue
		}
		var targets []directoryTarget
		switch {
		case obj.object.IsList():
			items, _ := obj.object.Object["items"].([]any)
			for index, item := range items {
				label := fmt.Sprintf("%s list item /items/%d", obj.label, index)
				itemObject, ok := item.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("%s is not an object", label)
				}
				targets = append(targets, directoryTarget{
					manifest: Manifest{Path: obj.path, Object: &unstructured.Unstructured{Object: itemObject}, RootObject: obj.object},
					label:    label,
					listItem: true,
				})
			}
		case directoryNullList(obj.object):
			continue
		default:
			targets = []directoryTarget{{
				manifest:     Manifest{Path: obj.path, Object: obj.object},
				label:        obj.label,
				jsonKindless: obj.jsonKindless,
			}}
		}
		for _, target := range targets {
			if opts.TrackManifest != nil {
				if err := opts.TrackManifest(target.manifest.Object.DeepCopy()); err != nil {
					return nil, fmt.Errorf("%s: %w", target.label, err)
				}
			}
			out = append(out, target)
		}
	}
	return out, nil
}

// checkDirectoryManifestKinds fails on the first emitted manifest without a
// kind, as the controller does when it reads the manifests back
// (unmarshalManifests, controller/state.go:562-571 at v3.6.1, through
// v1alpha1.UnmarshalToUnstructured). See unstructuredDecodeKind for what
// counts as a kind.
func checkDirectoryManifestKinds(targets []directoryTarget) error {
	for _, target := range targets {
		if unstructuredDecodeKind(target.manifest.Object) != "" {
			continue
		}
		if target.jsonKindless {
			return directoryKindlessJSONError(target.manifest.Path, target.manifest.Object)
		}
		return directoryKindlessManifestError(target.label, target.manifest.Object)
	}
	return nil
}

// unstructuredDecodeKind is the kind that the Unstructured JSON decode
// checks (apimachinery v0.37.0 unstructured/helpers.go:388-391). The decode
// reads the parsed GroupVersionKind, not the raw kind field, and
// GroupVersionKind is empty when the apiVersion does not parse as a group
// version, such as an apiVersion with two or more slashes
// (unstructured.go:430-437, runtime/schema/group_version.go:213-226). A kind
// that is not a string also counts as missing.
func unstructuredDecodeKind(object *unstructured.Unstructured) string {
	return object.GroupVersionKind().Kind
}

// missingKindReason says why unstructuredDecodeKind found no kind.
func missingKindReason(object *unstructured.Unstructured) string {
	if object.GetKind() != "" {
		return fmt.Sprintf("has apiVersion %q, which is not a valid group version, so it has no kind", object.GetAPIVersion())
	}
	return "has no kind"
}

func directoryRenderDiagnostics(opts RenderOptions) []diagnostic.Diagnostic {
	if !opts.DirectoryDisableExtensionFilter || opts.DirectoryInclude != "" || opts.DirectoryExclude != "" {
		return nil
	}
	return []diagnostic.Diagnostic{{
		Code:     DirectoryExtensionFilterDisabledCode,
		Severity: diagnostic.SeverityWarning,
		Category: "directory",
		Message:  "directory.disableExtensionFilter is enabled without include or exclude; every file in the directory is read and considered as a manifest",
	}}
}

// decodeDirectoryFileText decodes a directory file's bytes the way the
// repo-server reads every directory file, through utfutil.OpenFile and
// utfutil.ReadFile with the utfutil.UTF8 hint (reposerver/repository at
// v3.6.1): UTF-8 unless a byte order mark says UTF-16LE or UTF-16BE, the
// byte order mark dropped, and invalid UTF-8 replaced with U+FFFD
// (unicode.BOMOverride over the x/text UTF-8 decoder, as utfutil.NewReader
// builds it). The skip-file-rendering marker and every decoder see the
// decoded text.
func decodeDirectoryFileText(manifestPath string, data []byte) ([]byte, error) {
	decoded, _, err := transform.Bytes(xunicode.BOMOverride(xunicode.UTF8.NewDecoder()), data)
	if err != nil {
		return nil, fmt.Errorf("%s: decode file text failed: %w", manifestPath, err)
	}
	return decoded, nil
}

// directoryStreamObjects reads a file outside the ".json" rule the way the
// repo-server's getObjsFromYAMLOrJSON and splitYAMLOrJSON do
// (reposerver/repository/repository.go:2091-2136 at v3.6.1). splitYAMLOrJSON
// decodes each document into an Unstructured, so it stops at the first decode
// error and at the first non-empty document that is not an object with a
// non-empty string kind, an empty mapping included. It skips null and empty
// documents. A document with a kind but no apiVersion decodes.
//
// When the stream stops, the file fails the source if an object decoded
// before the stop, or if the file text holds all of "apiVersion:", "kind:"
// and "metadata:". Otherwise the repo-server takes the file to be unrelated
// YAML and skips the whole file.
func directoryStreamObjects(manifestPath string, data []byte) ([]directoryObject, error) {
	decoder := manifestyaml.NewDecoder(bytes.NewReader(data))
	var objects []directoryObject
	for index := 0; ; index++ {
		value, err := decoder.Decode()
		if errors.Is(err, io.EOF) {
			return objects, nil
		}
		label := fmt.Sprintf("%s document %d", manifestPath, index)
		var stop error
		if err != nil {
			stop = fmt.Errorf("%s: decode YAML document failed: %w", label, err)
		} else if value == nil {
			continue
		} else if object, ok := value.(map[string]any); !ok {
			stop = fmt.Errorf("%s is not an object", label)
		} else if decoded := (&unstructured.Unstructured{Object: object}); unstructuredDecodeKind(decoded) == "" {
			if decoded.GetKind() != "" {
				stop = fmt.Errorf("%s %s", label, missingKindReason(decoded))
			} else {
				stop = fmt.Errorf("%s is missing kind", label)
			}
		} else {
			objects = append(objects, directoryObject{
				path:   manifestPath,
				label:  label,
				object: &unstructured.Unstructured{Object: object},
			})
			continue
		}
		if len(objects) > 0 || directoryFileHasManifestMarkers(data) {
			return nil, stop
		}
		return nil, nil
	}
}

// directoryFileHasManifestMarkers reports whether the file text holds all of
// "apiVersion:", "kind:" and "metadata:", the repo-server's test for a file
// that looks like a manifest.
func directoryFileHasManifestMarkers(data []byte) bool {
	return bytes.Contains(data, []byte("apiVersion:")) &&
		bytes.Contains(data, []byte("kind:")) &&
		bytes.Contains(data, []byte("metadata:"))
}

func directoryManifestIncluded(root, filePath string, opts RenderOptions) bool {
	rel, err := filepath.Rel(root, filePath)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	if opts.DirectoryExclude != "" && argoglob.Match(opts.DirectoryExclude, rel) {
		return false
	}
	if opts.DirectoryInclude != "" && !argoglob.Match(opts.DirectoryInclude, rel) {
		return false
	}
	return true
}

func shouldSkipDirectoryCandidate(root, path string, entry os.DirEntry, opts RenderOptions) bool {
	if path == root {
		return false
	}
	// Repository metadata is never a manifest; sourceRoot already refuses a
	// source path inside .git.
	if entry.Name() == drydockCacheMetadataDirName || strings.EqualFold(entry.Name(), ".git") {
		return true
	}
	return !opts.DirectoryRecurse
}

func isManifestFile(path string) bool {
	return directoryManifestFile.MatchString(filepath.Base(path))
}

// isJSONSuffixFile reports whether the repo-server's case-sensitive ".json"
// suffix check selects the single-object JSON rule for a file, whether or not
// the extension filter is disabled.
func isJSONSuffixFile(path string) bool {
	return strings.HasSuffix(filepath.Base(path), ".json")
}

// validateDirectoryJSONFile mirrors the repo-server's rule for a file whose
// name ends in ".json" (getObjsFromYAMLOrJSON,
// reposerver/repository/repository.go:2069-2091 at v3.6.1). The content,
// after any UTF-8 byte order mark, must decode as one JSON object with a
// kind, and nothing but whitespace may follow it. An empty file, YAML, a JSON
// array or scalar, and a syntax error all fail the source.
//
// When the object has no kind, or the content is JSON null, the repo-server
// tries the content as an OCI image manifest. If that also fails, the source
// fails with the first error. If it succeeds, the repo-server keeps the
// object that the failed decode left behind. validateDirectoryJSONFile then
// returns that object with kindless set, so the caller can treat it as the
// repo-server does.
func validateDirectoryJSONFile(manifestPath string, data []byte) (object *unstructured.Unstructured, kindless bool, err error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	object = &unstructured.Unstructured{}
	if decodeErr := decoder.Decode(object); decodeErr != nil {
		var ociManifest imagev1.Manifest
		if json.NewDecoder(bytes.NewReader(data)).Decode(&ociManifest) != nil {
			return nil, false, fmt.Errorf("%s: decode JSON object failed: %s", manifestPath, describeDirectoryJSONDecodeError(decodeErr))
		}
		kindless = true
	}
	if decoder.More() {
		return nil, false, fmt.Errorf("%s: found multiple objects; only single objects are allowed in JSON files", manifestPath)
	}
	return object, kindless, nil
}

// directoryJSONFileObjects reads a file whose name ends in ".json" as one
// object (see validateDirectoryJSONFile).
func directoryJSONFileObjects(manifestPath string, data []byte) ([]directoryObject, error) {
	object, kindless, err := validateDirectoryJSONFile(manifestPath, data)
	if err != nil {
		return nil, err
	}
	return []directoryObject{{
		path:         manifestPath,
		label:        manifestPath + " document 0",
		object:       object,
		jsonKindless: kindless,
	}}, nil
}

// directoryNullList mirrors the repo-server's isNullList
// (reposerver/repository/repository.go:1988-2000 at v3.6.1): an object with
// "items: null" and neither spec nor status emits nothing.
func directoryNullList(object *unstructured.Unstructured) bool {
	if _, ok := object.Object["spec"]; ok {
		return false
	}
	if _, ok := object.Object["status"]; ok {
		return false
	}
	items, ok := object.Object["items"]
	return ok && items == nil
}

// directoryKindlessJSONError is the source failure for a ".json" file that
// the repo-server accepts as an OCI image manifest and then emits as a
// manifest without a kind (GenerateManifests,
// reposerver/repository/repository.go:1817-1852 at v3.6.1). The controller
// cannot read that manifest back, so the Application fails with a
// ComparisonError that says "Object 'Kind' is missing" (unmarshalManifests,
// controller/state.go:562-571, through v1alpha1.UnmarshalToUnstructured).
// That message quotes the object but not the file, so drydock names the
// file instead.
func directoryKindlessJSONError(manifestPath string, object *unstructured.Unstructured) error {
	// The failed decode leaves no object for JSON null, or for an object
	// that the Unstructured decoder cannot hold.
	value := "JSON object"
	if object.Object == nil {
		value = "JSON value"
	}
	return fmt.Errorf("%s: %s %s, so Argo CD fails the Application with \"Object 'Kind' is missing\"", manifestPath, value, missingKindReason(object))
}

// directoryKindlessManifestError is the source failure for any other
// emitted manifest without a kind, in practice a list item. GenerateManifests
// (reposerver/repository/repository.go:1817-1852 at v3.6.1) splits every list
// into its items, adds the tracking metadata to each item, and emits it. The
// controller then fails on the item, as directoryKindlessJSONError describes.
// This holds for any list in a YAML, JSON, or Jsonnet file, with or without a
// kind of its own.
func directoryKindlessManifestError(label string, object *unstructured.Unstructured) error {
	return fmt.Errorf("%s %s, so Argo CD fails the Application with \"Object 'Kind' is missing\"", label, missingKindReason(object))
}

// describeDirectoryJSONDecodeError names why a ".json" file did not decode
// without echoing its content: Unstructured's missing-kind error quotes the
// whole document.
func describeDirectoryJSONDecodeError(err error) string {
	if errors.Is(err, io.EOF) {
		return "file is empty"
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return "unexpected end of JSON input"
	}
	if syntaxErr, ok := errors.AsType[*json.SyntaxError](err); ok {
		return fmt.Sprintf("invalid JSON at offset %d", syntaxErr.Offset)
	}
	if runtime.IsMissingKind(err) {
		return "JSON object is missing kind"
	}
	return "JSON value is not an object"
}

// isJsonnetFile mirrors the repo-server's case-sensitive ".jsonnet" suffix
// check, which applies whether or not the extension filter is disabled.
func isJsonnetFile(path string) bool {
	return strings.HasSuffix(filepath.Base(path), ".jsonnet")
}

func sourceRoot(source ResolvedSource) (string, error) {
	sourcePath, err := cleanSourcePath(source.Path)
	if err != nil {
		return "", err
	}
	if err := rejectSymlinkComponents(source.RepoRoot, sourcePath); err != nil {
		return "", err
	}
	return filepath.Join(source.RepoRoot, sourcePath), nil
}

func cleanSourcePath(path string) (string, error) {
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("source path %q must be relative", path)
	}

	clean := filepath.Clean(path)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("source path %q escapes repository root", path)
	}
	if pathsafety.RelEntersGit(clean) {
		return "", gitPathRefError("source path", path)
	}
	return clean, nil
}

func rejectSymlinkComponents(repoRoot, sourcePath string) error {
	if sourcePath == "." {
		return nil
	}

	current := filepath.Clean(repoRoot)
	for component := range strings.SplitSeq(sourcePath, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("source path %q includes symlink component %q", sourcePath, component)
		}
	}
	return nil
}

func relativeManifestPath(repoRoot, path string) (string, error) {
	rel, err := filepath.Rel(repoRoot, path)
	if err != nil || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return "", fmt.Errorf("manifest path %q escapes repository root %q", path, repoRoot)
	}
	return rel, nil
}
