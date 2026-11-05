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
	"github.com/sholdee/drydock/internal/manifest"
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

//nolint:gocyclo // Directory rendering keeps walk, filtering, decode, and path provenance in one pass.
func (DirectoryRenderer) Render(ctx context.Context, source ResolvedSource, opts RenderOptions) ([]Manifest, []diagnostic.Diagnostic, error) {
	root, err := sourceRoot(source)
	if err != nil {
		return nil, nil, err
	}
	var out []Manifest
	diags := directoryRenderDiagnostics(opts)
	// kindlessJSONErr holds the first ".json" file whose object has no kind.
	// Argo CD's repo-server emits that object and the controller fails on it
	// later, so any file that fails in the repo-server is reported first.
	var kindlessJSONErr error

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
		if isJsonnetFile(path) {
			rendered, err := renderJsonnetFile(root, source.RepoRoot, path, manifestPath, opts)
			if err != nil {
				return err
			}
			out = append(out, rendered...)
			return nil
		}
		kindlessList := false
		if isJSONSuffixFile(path) {
			object, kindless, err := validateDirectoryJSONFile(manifestPath, data)
			if err != nil {
				return err
			}
			if kindless {
				switch {
				case object.IsList():
					kindlessList = true
				case directoryNullList(object):
					return nil
				default:
					if kindlessJSONErr == nil {
						kindlessJSONErr = directoryKindlessJSONError(manifestPath, object)
					}
					return nil
				}
			}
		} else if stream := directoryStreamFailure(manifestPath, data); stream.err != nil {
			if !stream.partial && !directoryDecodeFailureIsFatal(stream.yielded, data) {
				return nil
			}
			if _, rootsErr := manifest.DecodeDocumentRoots(manifestPath, bytes.NewReader(data)); rootsErr != nil {
				return rootsErr
			}
			return stream.err
		}
		if !kindlessList {
			roots, err := manifest.DecodeDocumentRoots(manifestPath, bytes.NewReader(data))
			if err != nil {
				return err
			}
			hasCandidate, err := classifyDirectoryRoots(roots)
			if err != nil {
				return err
			}
			if !hasCandidate {
				return nil
			}
		}
		docs, err := manifest.DecodeDocuments(manifestPath, bytes.NewReader(data))
		if err != nil {
			return err
		}

		for _, doc := range docs {
			// A kind-less ".json" list is never emitted itself, so only its
			// items are checked.
			if !kindlessList && doc.RootObject != nil && doc.RootObject != doc.Object {
				if _, err := classifyDirectoryDocument(Manifest{Path: doc.Path, Object: doc.RootObject}); err != nil {
					return err
				}
			}
			rendered := NewDocumentManifest(doc)
			include, err := classifyDirectoryDocument(rendered)
			if err != nil {
				return err
			}
			if !include {
				continue
			}
			out = append(out, rendered)
		}
		return nil
	})
	if err == nil {
		err = kindlessJSONErr
	}
	return out, diags, err
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

func classifyDirectoryRoots(docs []manifest.Document) (bool, error) {
	hasCandidate := false
	for _, doc := range docs {
		include, err := classifyDirectoryDocument(Manifest{Path: doc.Path, Object: doc.Object})
		if err != nil {
			return false, err
		}
		if include {
			hasCandidate = true
		}
	}
	return hasCandidate, nil
}

func classifyDirectoryDocument(manifest Manifest) (bool, error) {
	if manifest.Object == nil {
		return false, nil
	}
	apiVersion := manifest.Object.GetAPIVersion()
	kind := manifest.Object.GetKind()
	if apiVersion == "" && kind == "" {
		return false, nil
	}
	if apiVersion == "" {
		return false, fmt.Errorf("%s document is missing apiVersion", manifest.Path)
	}
	if kind == "" {
		return false, fmt.Errorf("%s document is missing kind", manifest.Path)
	}
	return true, nil
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

// directoryDecodeFailureIsFatal mirrors how getObjsFromYAMLOrJSON
// (reposerver/repository/repository.go at v3.6.1) treats a file outside the
// ".json" rule whose YAML/JSON stream stops with an error. The source fails
// when the stream yielded an object before the failure, or when the raw
// content holds all of "apiVersion:", "kind:" and "metadata:"; any other file
// is taken to be unrelated YAML and skipped.
func directoryDecodeFailureIsFatal(yielded bool, data []byte) bool {
	if yielded {
		return true
	}
	return bytes.Contains(data, []byte("apiVersion:")) &&
		bytes.Contains(data, []byte("kind:")) &&
		bytes.Contains(data, []byte("metadata:"))
}

// directoryStreamResult is where the repo-server's splitYAMLOrJSON stops
// reading a file outside the ".json" rule.
type directoryStreamResult struct {
	// err is what stops the stream; nil when every document decodes.
	err error
	// yielded reports that an object decoded before err.
	yielded bool
	// partial reports that err is a document with an apiVersion but no
	// kind, which drydock fails whatever the repo-server does with the file
	// (a recorded divergence: the repo-server skips such a file when it
	// stops there with nothing decoded and not every manifest marker).
	partial bool
}

// directoryStreamFailure walks a file outside the ".json" rule the way the
// repo-server's splitYAMLOrJSON does. splitYAMLOrJSON decodes each document
// into an Unstructured, so it stops at the first decode error and at the
// first non-empty document that is not an object with a non-empty string
// kind, an empty mapping included; null and empty documents are skipped.
func directoryStreamFailure(manifestPath string, data []byte) directoryStreamResult {
	decoder := manifestyaml.NewDecoder(bytes.NewReader(data))
	var result directoryStreamResult
	for index := 0; ; index++ {
		value, err := decoder.Decode()
		if errors.Is(err, io.EOF) {
			return result
		}
		if err != nil {
			result.err = fmt.Errorf("%s document %d: decode YAML document failed: %w", manifestPath, index, err)
			return result
		}
		if value == nil {
			continue
		}
		object, ok := value.(map[string]any)
		if !ok {
			result.err = fmt.Errorf("%s document %d is not an object", manifestPath, index)
			return result
		}
		if kind, _ := object["kind"].(string); kind == "" {
			result.err = fmt.Errorf("%s document %d is missing kind", manifestPath, index)
			apiVersion, _ := object["apiVersion"].(string)
			result.partial = apiVersion != ""
			return result
		}
		result.yielded = true
	}
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
	return fmt.Errorf("%s: %s has no kind, so Argo CD fails the Application with \"Object 'Kind' is missing\"", manifestPath, value)
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
